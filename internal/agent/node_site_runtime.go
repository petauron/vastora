package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/petauron/vastora/internal/nodesite"
	"github.com/petauron/vastora/internal/secret"
)

type nodeSiteJournal struct {
	Task        nodesite.Task `json:"task"`
	State       string        `json:"state"`
	Previous    []byte        `json:"previous"`
	Candidate   []byte        `json:"candidate"`
	Fingerprint string        `json:"fingerprint"`
	NotAfter    string        `json:"notAfter"`
}

type nodeSiteHost struct {
	manager                SystemdHostApplicationManager
	path, unit, executable string
	healthURL              string
}

func (s *Store) nodeSiteHost(ctx context.Context, t nodesite.Task) (nodeSiteHost, error) {
	var h nodeSiteHost
	if err := t.Validate(); err != nil {
		return h, err
	}
	installed, err := s.AppliedInstallation(ctx, t.AppKey)
	if err != nil || installed.ApplicationID != t.ApplicationID {
		return h, errors.New("agent: node site application identity changed")
	}
	var config struct {
		Hostname string `json:"hostname"`
		HTTPPort int    `json:"http_port"`
	}
	if json.Unmarshal(installed.Config, &config) != nil || config.Hostname != t.Hostname {
		return h, errors.New("agent: node site hostname differs from the installed application")
	}
	plan, err := planNativePackage(t.ApplicationID, installed.Manifest)
	if err != nil {
		return h, err
	}
	receipt, err := (PackageExecutor{StateDirectory: s.dataDir}).ReadResources(t.ApplicationID)
	if err != nil {
		return h, err
	}
	if err = inspectNativeOwnership(ctx, h.manager, t.ApplicationID, plan, receipt); err != nil {
		return h, err
	}
	if len(plan.Units) != 1 || plan.Units[0].LogicalName != "web" || plan.Units[0].Process.Executable != "daylight" {
		return h, errors.New("agent: unsupported node site runtime")
	}
	u := plan.Units[0]
	address := installed.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	if config.HTTPPort < 1 || config.HTTPPort > 65535 {
		return h, errors.New("agent: invalid private Daylight health port")
	}
	h.healthURL = "http://" + net.JoinHostPort(address, strconv.Itoa(config.HTTPPort)) + "/healthz"
	h.path = filepath.Join(u.ConfigDirectory, "config", "caddy.json")
	h.unit = u.Name
	h.executable = filepath.Join("/opt/vastora/packages", packageIdentity(t.ApplicationID), "artifacts", u.Process.Artifact, "daylight")
	r := resourceNamed(receipt, "file", "web:config:caddy.json")
	if r == nil || r.Path != h.path {
		return h, errors.New("agent: node site configuration is not owned")
	}
	return h, nil
}

func (s *Store) readNodeSiteJournal(ctx context.Context, id string) (nodeSiteJournal, error) {
	var j nodeSiteJournal
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM node_site_journal WHERE application_id=?`, id).Scan(&sealed); err != nil {
		return j, err
	}
	raw, err := secret.Open(s.key, sealed, []byte("node-site-journal:"+id))
	if err != nil {
		return j, err
	}
	if json.Unmarshal(raw, &j) != nil || j.Task.ApplicationID != id || j.Task.Validate() != nil {
		return j, errors.New("agent: invalid node site journal")
	}
	return j, nil
}
func (s *Store) saveNodeSiteJournal(ctx context.Context, j nodeSiteJournal) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, raw, []byte("node-site-journal:"+j.Task.ApplicationID))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO node_site_journal(application_id,sealed_state) VALUES(?,?) ON CONFLICT(application_id) DO UPDATE SET sealed_state=excluded.sealed_state`, j.Task.ApplicationID, sealed)
	return err
}
func (s *Store) rollbackNodeSite(ctx context.Context, h nodeSiteHost, j *nodeSiteJournal) error {
	current, err := captureApplicationConfiguration(h.path)
	if err != nil {
		return err
	}
	// Never overwrite an independent application configuration change.
	if !current.Exists || (!bytes.Equal(current.Data, j.Candidate) && !bytes.Equal(current.Data, j.Previous)) {
		return errors.New("agent: node site configuration changed; manual recovery required")
	}
	if err = writeHostFileAtomic(h.path, j.Previous, 0600); err != nil {
		return err
	}
	if err = h.manager.run(ctx, "systemctl", "restart", h.unit); err != nil {
		return errors.New("agent: node site rollback restart failed")
	}
	healthCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	for {
		req, e := http.NewRequestWithContext(healthCtx, http.MethodGet, h.healthURL, nil)
		if e != nil {
			return e
		}
		response, e := client.Do(req)
		healthy := e == nil && response.StatusCode == 204
		if response != nil {
			response.Body.Close()
		}
		if healthy {
			break
		}
		select {
		case <-healthCtx.Done():
			return errors.New("agent: restored Daylight health check failed")
		case <-time.After(250 * time.Millisecond):
		}
	}
	j.State = "rolled_back"
	return s.saveNodeSiteJournal(ctx, *j)
}

// recoverNodeSites restores an interrupted replacement before accepting work.
// A completed replacement is retained, including when its response was lost.
func (s *Store) recoverNodeSites(ctx context.Context) error {
	return s.recoverNodeSiteJournals(ctx, s.nodeSiteHost)
}

func (s *Store) recoverNodeSiteJournals(ctx context.Context, resolve func(context.Context, nodesite.Task) (nodeSiteHost, error)) error {
	rows, err := s.db.QueryContext(ctx, `SELECT application_id FROM node_site_journal`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		j, err := s.readNodeSiteJournal(ctx, id)
		if err != nil {
			return err
		}
		if j.State != "applying" {
			continue
		}
		h, err := resolve(ctx, j.Task)
		if err != nil {
			return err
		}
		if err = s.rollbackNodeSite(ctx, h, &j); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) executeNodeSite(ctx context.Context, t nodesite.Task) (nodesite.Result, error) {
	s.landingMutationMu.Lock()
	defer s.landingMutationMu.Unlock()
	h, err := s.nodeSiteHost(ctx, t)
	if err != nil {
		return nodesite.Result{}, err
	}
	return s.executeNodeSiteOnHost(ctx, t, h)
}

// The host is resolved and ownership-checked before entering the transaction.
func (s *Store) executeNodeSiteOnHost(ctx context.Context, t nodesite.Task, h nodeSiteHost) (nodesite.Result, error) {
	var err error
	result := nodesite.Result{Action: t.Action, Revision: t.Revision, Hostname: t.Hostname}
	j, jerr := s.readNodeSiteJournal(ctx, t.ApplicationID)
	if jerr != nil && !errors.Is(jerr, sql.ErrNoRows) {
		return result, jerr
	}
	if jerr == nil && j.State == "applying" {
		if err = s.rollbackNodeSite(ctx, h, &j); err != nil {
			return result, err
		}
	}
	if jerr == nil && j.Task.Revision > t.Revision {
		return result, errors.New("agent: stale node site revision")
	}
	switch t.Action {
	case "rollback":
		if jerr != nil || j.Task.Revision != t.Revision {
			return result, errors.New("agent: node site rollback revision unavailable")
		}
		err = s.rollbackNodeSite(ctx, h, &j)
		result.State = j.State
		return result, err
	case "inspect":
		if jerr != nil {
			return result, jerr
		}
		if j.Task.Revision != t.Revision || j.Task.Port != t.Port {
			return result, errors.New("agent: node site inspection revision is not installed")
		}
		result.State = j.State
		result.Fingerprint = j.Fingerprint
		result.NotAfter = j.NotAfter
		if j.State == "applied" {
			err = probeNodeSite(ctx, j.Task, j.Fingerprint)
		}
		return result, err
	}
	certificate, err := nodeSiteCertificate(t.Hostname, t.CertificatePEM, t.PrivateKeyPEM)
	if err != nil {
		return result, err
	}
	block, _ := pem.Decode([]byte(t.CertificatePEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return result, err
	}
	fingerprint := nodesite.Fingerprint(leaf.Raw)
	if jerr == nil && j.Task.Revision == t.Revision {
		if j.Fingerprint != fingerprint {
			return result, errors.New("agent: node site revision content changed")
		}
		if j.State == "applied" {
			result.State = "applied"
			result.Fingerprint = fingerprint
			result.NotAfter = j.NotAfter
			return result, probeNodeSite(ctx, t, fingerprint)
		}
		return result, errors.New("agent: node site revision was rolled back; request a new revision")
	}
	previous, err := captureApplicationConfiguration(h.path)
	if err != nil || !previous.Exists {
		return result, errors.New("agent: node site configuration unavailable")
	}
	candidate, err := nodeSiteConfiguration(previous.Data, t, certificate.CertificatePEM, certificate.PrivateKeyPEM)
	if err != nil {
		return result, err
	}
	// Validate the exact candidate using the installed, receipt-verified launcher.
	if h.manager.RunInputCommand != nil {
		err = h.manager.RunInputCommand(ctx, bytes.NewReader(candidate), h.executable, "validate", "--config", "/dev/stdin")
	} else {
		command := exec.CommandContext(ctx, h.executable, "validate", "--config", "/dev/stdin")
		command.Stdin = bytes.NewReader(candidate)
		err = command.Run()
	}
	if err != nil {
		return result, errors.New("agent: Daylight certificate configuration validation failed")
	}
	j = nodeSiteJournal{Task: t, State: "applying", Previous: previous.Data, Candidate: candidate, Fingerprint: fingerprint, NotAfter: leaf.NotAfter.UTC().Format(time.RFC3339Nano)}
	if err = s.saveNodeSiteJournal(ctx, j); err != nil {
		return result, err
	}
	if err = writeHostFileAtomic(h.path, candidate, 0600); err == nil {
		err = h.manager.run(ctx, "systemctl", "restart", h.unit)
	}
	if err == nil {
		err = probeNodeSite(ctx, t, fingerprint)
	}
	if err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		rollbackErr := s.rollbackNodeSite(recovery, h, &j)
		result.State = j.State
		return result, errors.Join(errors.New("agent: Daylight activation failed"), rollbackErr)
	}
	j.State = "applied"
	if err = s.saveNodeSiteJournal(ctx, j); err != nil {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		rollbackErr := s.rollbackNodeSite(recovery, h, &j)
		result.State = j.State
		return result, errors.Join(errors.New("agent: could not persist Daylight activation"), rollbackErr)
	}
	result.State = "applied"
	result.Fingerprint = fingerprint
	result.NotAfter = j.NotAfter
	return result, nil
}

func nodeSiteConfiguration(raw []byte, t nodesite.Task, chain, key string) ([]byte, error) {
	var config map[string]any
	if json.Unmarshal(raw, &config) != nil {
		return nil, errors.New("agent: invalid Daylight configuration")
	}
	admin, _ := config["admin"].(map[string]any)
	if admin["disabled"] != true {
		return nil, errors.New("agent: Daylight admin API must be disabled")
	}
	apps, _ := config["apps"].(map[string]any)
	httpApp, _ := apps["http"].(map[string]any)
	servers, _ := httpApp["servers"].(map[string]any)
	private, _ := servers["private"].(map[string]any)
	if private == nil {
		return nil, errors.New("agent: Daylight private server is unavailable")
	}
	// Preserve the signed page routes, private HTTP listener and existing public
	// listener. A separate loopback endpoint avoids the shared public 443 loop.
	origin := map[string]any{}
	for k, v := range private {
		origin[k] = v
	}
	origin["listen"] = []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(t.Port))}
	origin["automatic_https"] = map[string]any{"disable": true}
	origin["strict_sni_host"] = true
	origin["tls_connection_policies"] = []any{map[string]any{"match": map[string]any{"sni": []string{t.Hostname}}, "protocol_min": "tls1.3"}, map[string]any{"drop": true}}
	servers["vastora-node-site"] = origin
	// Certificate automation belongs exclusively to Center for this application.
	for _, v := range servers {
		if server, ok := v.(map[string]any); ok {
			server["automatic_https"] = map[string]any{"disable": true}
		}
	}
	apps["tls"] = map[string]any{"certificates": map[string]any{"load_pem": []any{map[string]any{"certificate": chain, "key": key}}}}
	return json.Marshal(config)
}

func probeNodeSite(ctx context.Context, t nodesite.Task, fingerprint string) error {
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var last error
	roots := x509.NewCertPool()
	block, _ := pem.Decode([]byte(t.CertificatePEM))
	if block == nil {
		return errors.New("agent: missing pinned node site certificate")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	roots.AddCert(leaf)
	for {
		dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 2 * time.Second}, Config: &tls.Config{RootCAs: roots, ServerName: t.Hostname, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}}}
		conn, err := dialer.DialContext(deadline, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(t.Port)))
		if err == nil {
			c := conn.(*tls.Conn)
			state := c.ConnectionState()
			conn.Close()
			if len(state.PeerCertificates) == 0 || nodesite.Fingerprint(state.PeerCertificates[0].Raw) != fingerprint || state.NegotiatedProtocol != "h2" {
				return errors.New("agent: Daylight did not load the requested certificate and HTTP/2")
			}
			return nil
		}
		last = err
		select {
		case <-deadline.Done():
			return fmt.Errorf("agent: Daylight TLS probe failed: %w", last)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
