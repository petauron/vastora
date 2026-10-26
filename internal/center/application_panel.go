package center

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
)

const panelServerName = "panel.glivela.internal"
const panelConfigKey = "panel_access_json"

type panelIdentity struct {
	CA                string `json:"ca"`
	Certificate       string `json:"certificate"`
	Key               string `json:"key"`
	ServerCertificate string `json:"serverCertificate"`
	ServerKey         string `json:"serverKey"`
}

func panelIdentityID(node string) string {
	sum := sha256.Sum256([]byte("application-panel/v1/" + meridianAppKey + "/" + node))
	return "panel-" + hex.EncodeToString(sum[:])
}
func makePanelIdentity(now time.Time) (panelIdentity, error) {
	var out panelIdentity
	serial := func() *big.Int { n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)); return n }
	caPub, caKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return out, err
	}
	ca := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "Vastora application access"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	if ca.SerialNumber == nil {
		return out, errors.New("panel identity randomness unavailable")
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, caPub, caKey)
	if err != nil {
		return out, err
	}
	out.CA = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	leaf := func(server bool) (string, string, error) {
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return "", "", err
		}
		cert := &x509.Certificate{SerialNumber: serial(), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if cert.SerialNumber == nil {
			return "", "", errors.New("panel identity randomness unavailable")
		}
		if server {
			cert.DNSNames = []string{panelServerName}
			cert.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, cert, ca, pub, caKey)
		if err != nil {
			return "", "", err
		}
		pk, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return "", "", err
		}
		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk})), nil
	}
	out.Certificate, out.Key, err = leaf(false)
	if err != nil {
		return out, err
	}
	out.ServerCertificate, out.ServerKey, err = leaf(true)
	return out, err
}
func (s *Store) panelIdentity(ctx context.Context, node string, create bool) (panelIdentity, error) {
	id := panelIdentityID(node)
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id=?`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) && create {
		identity, e := makePanelIdentity(s.now().UTC())
		if e != nil {
			return panelIdentity{}, e
		}
		raw, e := json.Marshal(identity)
		if e != nil {
			return panelIdentity{}, e
		}
		sealed, e = secret.Seal(s.key, raw, []byte(id))
		if e != nil {
			return panelIdentity{}, e
		}
		stamp := s.now().UTC().Format(time.RFC3339Nano)
		if _, e = s.db.ExecContext(ctx, `INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, id, sealed, stamp, stamp); e != nil {
			return panelIdentity{}, e
		}
		err = s.db.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id=?`, id).Scan(&sealed)
	}
	if err != nil {
		return panelIdentity{}, errors.New("center: managed application access not provisioned")
	}
	raw, err := secret.Open(s.key, sealed, []byte(id))
	if err != nil {
		return panelIdentity{}, err
	}
	var out panelIdentity
	if json.Unmarshal(raw, &out) != nil {
		return out, errors.New("center: invalid panel identity")
	}
	return out, nil
}
func (s *Store) withPanelAccess(ctx context.Context, node string, manifest catalog.AppManifest, raw []byte) ([]byte, error) {
	supported := false
	for _, f := range manifest.Config {
		if f.Key == panelConfigKey && f.Secret {
			supported = true
		}
	}
	if !supported {
		return raw, nil
	}
	identity, err := s.panelIdentity(ctx, node, true)
	if err != nil {
		return nil, err
	}
	// Only server material and the public client certificate leave Center.
	bundle, err := json.Marshal(map[string]string{"ca": identity.CA, "certificate": identity.ServerCertificate, "key": identity.ServerKey, "clientCertificate": identity.Certificate})
	if err != nil {
		return nil, err
	}
	values, err := decodeJSONObject(raw, "center: invalid application secrets")
	if err != nil {
		return nil, err
	}
	values[panelConfigKey], _ = json.Marshal(string(bundle))
	return json.Marshal(values)
}
func (s *Server) handleApplicationPanel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// This endpoint is deliberately an opaque, bounded admin API transport. It
	// has no route/account commands and never relays cookies or caller headers.
	operation := r.PathValue("operation")
	if len(operation) == 0 || len(operation) > 80 {
		http.NotFound(w, r)
		return
	}
	for _, c := range operation {
		if c < 'a' || c > 'z' {
			if c != '-' {
				http.NotFound(w, r)
				return
			}
		}
	}
	if r.URL.RawQuery != "" || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "invalid application request", http.StatusBadRequest)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host || (u.Scheme != "https" && u.Scheme != "http") {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "cross-origin request refused", http.StatusForbidden)
		return
	}
	var node, endpoint string
	err := s.store.db.QueryRowContext(r.Context(), `SELECT a.node_id,sv.endpoint FROM applications a JOIN services sv ON sv.application_id=a.id WHERE a.id=? AND a.app_key=? AND a.status='running' AND sv.name='controller' AND sv.protocol='https' AND sv.status IN ('ready','running','publishing')`, r.PathValue("id"), meridianAppKey).Scan(&node, &endpoint)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("center: application panel unavailable"))
		return
	}
	host, port, err := net.SplitHostPort(endpoint)
	ip := net.ParseIP(host)
	number, e := strconv.Atoi(port)
	if err != nil || e != nil || number < 1 || number > 65535 || ip == nil || ip.IsLoopback() || !networking.IsPrivateServiceAddress(host) {
		writeError(w, http.StatusServiceUnavailable, errors.New("center: application endpoint unverified"))
		return
	}
	identity, err := s.store.panelIdentity(r.Context(), node, false)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("center: application access requires managed upgrade"))
		return
	}
	certificate, err := tls.X509KeyPair([]byte(identity.Certificate), []byte(identity.Key))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("center: application access identity unavailable"))
		return
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(identity.CA)) {
		writeError(w, http.StatusServiceUnavailable, errors.New("center: application access trust unavailable"))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	if err != nil {
		http.Error(w, "application request too large", http.StatusRequestEntityTooLarge)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+net.JoinHostPort(host, port)+"/v1/admin/"+operation, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "invalid application target", http.StatusServiceUnavailable)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 20 * time.Second, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, ServerName: panelServerName, RootCAs: roots, Certificates: []tls.Certificate{certificate}}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, errors.New("center: application panel connection failed"))
		return
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
	if err != nil || len(payload) > 8<<20 {
		writeError(w, http.StatusBadGateway, errors.New("center: application response unavailable"))
		return
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		writeError(w, http.StatusBadGateway, errors.New("center: application redirect refused"))
		return
	}
	// Upstream authentication failures are not a revoked Vastora session.
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		writeError(w, http.StatusBadGateway, errors.New("center: application access identity rejected"))
		return
	}
	contentType := response.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		w.Header().Set("Content-Type", "application/json")
	} else {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(payload)
}
