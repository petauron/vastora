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
	"io"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/petauron/vastora/internal/nodesite"
	"github.com/petauron/vastora/internal/secret"
)

type nodeSiteJournal struct {
	Runtime     int           `json:"runtime,omitempty"`
	Task        nodesite.Task `json:"task"`
	State       string        `json:"state"`
	Previous    []byte        `json:"previous"`
	Candidate   []byte        `json:"candidate"`
	Fingerprint string        `json:"fingerprint"`
	NotAfter    string        `json:"notAfter"`
}
type nodeSiteHost struct{ site CaddySite }

func daylightKey(key string) bool {
	return key == "vastora-official/daylight" || key == "vastora-official/daylight-https"
}

func (s *Store) nodeSiteHost(ctx context.Context, task nodesite.Task) (nodeSiteHost, error) {
	if err := task.Validate(); err != nil {
		return nodeSiteHost{}, err
	}
	installed, err := s.AppliedInstallation(ctx, task.AppKey)
	if err != nil || installed.ApplicationID != task.ApplicationID {
		return nodeSiteHost{}, errors.New("agent: node site application identity changed")
	}
	return s.nodeSiteInstallationHost(ctx, installed)
}

func (s *Store) nodeSiteInstallationHost(ctx context.Context, installed AppliedInstallation) (nodeSiteHost, error) {
	var host nodeSiteHost
	if !daylightKey(installed.AppKey) {
		return host, errors.New("agent: website content must belong to Daylight")
	}
	var config struct {
		Hostname    string `json:"hostname"`
		HTTPPort    int    `json:"http_port"`
		HTTPSListen string `json:"https_listen"`
	}
	if json.Unmarshal(installed.Config, &config) != nil || !nodesite.ValidHostname(config.Hostname) {
		return host, errors.New("agent: invalid installed website identity")
	}
	if installed.AppKey == "vastora-official/daylight" && config.HTTPSListen != "" {
		return host, errors.New("agent: public HTTPS is not declared by this website package")
	}
	if installed.AppKey == "vastora-official/daylight-https" && config.HTTPSListen == "" {
		return host, errors.New("agent: exact public HTTPS listener is missing")
	}
	plan, err := planNativePackage(installed.ApplicationID, installed.Manifest)
	if err != nil {
		return host, err
	}
	receipt, err := (PackageExecutor{StateDirectory: s.dataDir}).ReadResources(installed.ApplicationID)
	if err != nil {
		return host, err
	}
	if err := inspectNativeOwnership(ctx, SystemdHostApplicationManager{}, installed.ApplicationID, plan, receipt); err != nil {
		return host, err
	}
	if len(plan.Units) != 1 || plan.Units[0].LogicalName != "web" || plan.Units[0].Process.Executable != "daylight" || plan.Units[0].Automatic || !slices.Contains(installed.Manifest.Runtime.RequiredCapabilities, "shared-caddy") || !slices.Contains(receipt.AuthorizedCapabilities, "shared-caddy") {
		return host, errors.New("agent: upgrade Daylight to the authorized shared Caddy content package first")
	}
	artifact := plan.Units[0].Process.Artifact
	root := filepath.Join("/opt/vastora/packages", packageIdentity(installed.ApplicationID), "artifacts", artifact)
	page := resourceNamed(receipt, "file", "artifact:"+artifact+":site/index.html")
	core := resourceNamed(receipt, "file", "artifact:"+artifact+":caddy")
	if page == nil || page.Path != filepath.Join(root, "site/index.html") || core == nil || core.Path != filepath.Join(root, "caddy") {
		return host, errors.New("agent: website content is not owned by its signed package")
	}
	content, err := readOwnedCaddyFile(page.Path, page.SHA256, 512<<10)
	if err != nil {
		return host, err
	}
	address := installed.ServiceAddress
	if address == "" {
		address = "127.0.0.1"
	}
	host.site = CaddySite{ApplicationID: installed.ApplicationID, Hostname: config.Hostname, Address: address, HTTPPort: config.HTTPPort, PublicListen: config.HTTPSListen, HTML: string(content), CaddyPath: core.Path, CaddySHA256: core.SHA256}
	return host, host.site.validate()
}

func (s *Store) readNodeSiteJournal(ctx context.Context, id string) (nodeSiteJournal, error) {
	var journal nodeSiteJournal
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM node_site_journal WHERE application_id=?`, id).Scan(&sealed); err != nil {
		return journal, err
	}
	raw, err := secret.Open(s.key, sealed, []byte("node-site-journal:"+id))
	if err != nil {
		return journal, err
	}
	if json.Unmarshal(raw, &journal) != nil || journal.Task.ApplicationID != id || journal.Task.Validate() != nil {
		return journal, errors.New("agent: invalid node site journal")
	}
	return journal, nil
}
func (s *Store) saveNodeSiteJournal(ctx context.Context, journal nodeSiteJournal) error {
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, raw, []byte("node-site-journal:"+journal.Task.ApplicationID))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO node_site_journal(application_id,sealed_state) VALUES(?,?) ON CONFLICT(application_id) DO UPDATE SET sealed_state=excluded.sealed_state`, journal.Task.ApplicationID, sealed)
	return err
}

func (s *Store) rollbackNodeSite(ctx context.Context, _ nodeSiteHost, journal *nodeSiteJournal) error {
	if journal.Runtime != 2 {
		return errors.New("agent: interrupted standalone website requires explicit migration review")
	}
	var previous CaddySite
	if json.Unmarshal(journal.Previous, &previous) != nil || previous.ApplicationID != journal.Task.ApplicationID {
		return errors.New("agent: invalid website recovery ownership")
	}
	current, err := s.readCaddySite(ctx, previous.ApplicationID)
	if err != nil || current.Site == nil {
		return errors.New("agent: website recovery state is unavailable")
	}
	// Certificate recovery must not undo a later verified content update.
	previous.HTML, previous.CaddyPath, previous.CaddySHA256 = current.Site.HTML, current.Site.CaddyPath, current.Site.CaddySHA256
	if err := s.updateCaddySite(ctx, previous.ApplicationID, &previous, func(ctx context.Context) error { return probeCaddySite(ctx, previous) }); err != nil {
		return err
	}
	journal.State = "rolled_back"
	return s.saveNodeSiteJournal(ctx, *journal)
}
func (s *Store) recoverNodeSites(ctx context.Context) error {
	if err := s.recoverDaylightHandovers(ctx); err != nil {
		return err
	}
	if err := s.restoreSharedCaddySites(ctx); err != nil {
		return err
	}
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
		if err := rows.Scan(&id); err != nil {
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
		journal, err := s.readNodeSiteJournal(ctx, id)
		if err != nil {
			return err
		}
		if journal.State != "applying" {
			continue
		}
		if journal.Runtime != 2 {
			return errors.New("agent: review the interrupted standalone website before migration")
		}
		host, err := resolve(ctx, journal.Task)
		if err != nil {
			return err
		}
		if err := s.rollbackNodeSite(ctx, host, &journal); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) executeNodeSite(ctx context.Context, task nodesite.Task) (nodesite.Result, error) {
	s.landingMutationMu.Lock()
	defer s.landingMutationMu.Unlock()
	host, err := s.nodeSiteHost(ctx, task)
	if err != nil {
		return nodesite.Result{}, err
	}
	if host.site.Hostname != task.Hostname {
		return nodesite.Result{}, errors.New("agent: website hostname differs from the installed package")
	}
	return s.executeNodeSiteOnHost(ctx, task, host)
}
func (s *Store) executeNodeSiteOnHost(ctx context.Context, task nodesite.Task, host nodeSiteHost) (nodesite.Result, error) {
	result := nodesite.Result{Action: task.Action, Revision: task.Revision, Hostname: task.Hostname}
	journal, journalErr := s.readNodeSiteJournal(ctx, task.ApplicationID)
	if journalErr != nil && !errors.Is(journalErr, sql.ErrNoRows) {
		return result, journalErr
	}
	if journalErr == nil && (journal.Task.Revision > task.Revision || journal.State == "applying") {
		return result, errors.New("agent: review the existing website revision first")
	}
	if task.Action == "rollback" {
		if journalErr != nil || journal.Task.Revision != task.Revision {
			return result, errors.New("agent: website rollback revision is unavailable")
		}
		err := s.rollbackNodeSite(ctx, host, &journal)
		result.State = journal.State
		return result, err
	}
	if task.Action == "inspect" {
		if journalErr != nil || journal.Task.Revision != task.Revision || journal.Task.Port != task.Port {
			return result, errors.New("agent: website inspection revision is not installed")
		}
		result.State, result.Fingerprint, result.NotAfter = journal.State, journal.Fingerprint, journal.NotAfter
		if journal.State != "applied" {
			return result, nil
		}
		state, err := s.readCaddySite(ctx, task.ApplicationID)
		if err != nil || state.Site == nil || state.State != "applied" {
			return result, errors.New("agent: website has not migrated to shared Caddy")
		}
		return result, probeCaddySite(ctx, *state.Site)
	}
	certificate, err := nodeSiteCertificate(task.Hostname, task.CertificatePEM, task.PrivateKeyPEM)
	if err != nil {
		return result, err
	}
	block, _ := pem.Decode([]byte(task.CertificatePEM))
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return result, err
	}
	fingerprint := nodesite.Fingerprint(leaf.Raw)
	if journalErr == nil && journal.Task.Revision == task.Revision && journal.Fingerprint != fingerprint {
		return result, errors.New("agent: node site revision content changed")
	}
	if journalErr == nil && journal.Runtime == 2 && journal.Task.Revision == task.Revision {
		if journal.State != "applied" {
			return result, errors.New("agent: request a new website revision after rollback")
		}
		result.State, result.Fingerprint, result.NotAfter = "applied", fingerprint, journal.NotAfter
		site := host.site
		site.TLSPort, site.Certificate = task.Port, certificate
		return result, probeCaddySite(ctx, site)
	}
	previous := host.site
	if state, err := s.readCaddySite(ctx, task.ApplicationID); err == nil && state.Site != nil {
		previous = *state.Site
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	candidate := host.site
	candidate.TLSPort, candidate.Certificate = task.Port, certificate
	previousBytes, _ := json.Marshal(previous)
	candidateBytes, _ := json.Marshal(candidate)
	journal = nodeSiteJournal{Runtime: 2, Task: task, State: "applying", Previous: previousBytes, Candidate: candidateBytes, Fingerprint: fingerprint, NotAfter: leaf.NotAfter.UTC().Format(time.RFC3339Nano)}
	if err := s.saveNodeSiteJournal(ctx, journal); err != nil {
		return result, err
	}
	err = s.updateCaddySite(ctx, task.ApplicationID, &candidate, func(ctx context.Context) error { return probeCaddySite(ctx, candidate) })
	if err != nil {
		state, readErr := s.readCaddySite(ctx, task.ApplicationID)
		if readErr == nil && state.State == "applied" {
			journal.State = "rolled_back"
			_ = s.saveNodeSiteJournal(context.WithoutCancel(ctx), journal)
		}
		result.State = journal.State
		return result, err
	}
	journal.State = "applied"
	if err := s.saveNodeSiteJournal(ctx, journal); err != nil {
		return result, uncertainTaskOutcome(err)
	}
	result.State, result.Fingerprint, result.NotAfter = "applied", fingerprint, journal.NotAfter
	return result, nil
}

func probeCaddySite(ctx context.Context, site CaddySite) error {
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true}
	url := "http://" + net.JoinHostPort(site.Address, strconv.Itoa(site.HTTPPort)) + "/"
	if site.TLSPort != 0 {
		roots := x509.NewCertPool()
		block, _ := pem.Decode([]byte(site.Certificate.CertificatePEM))
		if block == nil {
			return errors.New("agent: website probe certificate is missing")
		}
		leaf, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return err
		}
		roots.AddCert(leaf)
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: site.Hostname, MinVersion: tls.VersionTLS13}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(site.TLSPort)))
		}
		url = "https://" + site.Hostname + "/"
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Host = site.Hostname
	response, err := client.Do(request)
	if err != nil {
		return errors.New("agent: shared website request failed")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(len(site.HTML))+1))
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(body, []byte(site.HTML)) {
		return errors.New("agent: shared website content did not match its signed package")
	}
	if site.TLSPort != 0 {
		block, _ := pem.Decode([]byte(site.Certificate.CertificatePEM))
		if response.TLS == nil || response.TLS.NegotiatedProtocol != "h2" || len(response.TLS.PeerCertificates) == 0 || block == nil || nodesite.Fingerprint(response.TLS.PeerCertificates[0].Raw) != nodesite.Fingerprint(block.Bytes) {
			return errors.New("agent: shared website did not load its pinned certificate and HTTP/2")
		}
	}
	return nil
}
