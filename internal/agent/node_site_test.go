package agent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/gatewayruntime"
	"github.com/petauron/vastora/internal/nodesite"
)

func TestSharedCaddyNativeRuntimeKeepsProtocolFrontendIndependent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := gateway.NodeListenerState{Revision: 1, NodeID: "node-a", Listener: gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, RejectUnmatched: true, Routes: []gateway.Layer4Route{{ID: "route", Hostname: "site.example.com", ApplicationNodeID: "node-a", ManagedReality: true, ProxyProtocol: gateway.ProxyProtocolV2, Upstreams: []gateway.Upstream{{Address: "100.64.0.2", Port: 10443}}}}}}
	if err := store.RecordNodeListenerState(ctx, state); err != nil {
		t.Fatal(err)
	}
	for _, container := range []bool{false, true} {
		driver := &CaddyGatewayDriver{SiteContainer: container, SiteRuntime: func(_ context.Context, desired gateway.DesiredState, _ []CaddySite) error {
			if (desired.SharedHTTPS != nil) != container {
				t.Fatal("protocol frontend changed the native website runtime")
			}
			return nil
		}}
		if err := store.ConfigureSharedCaddy(driver); err != nil {
			t.Fatal(err)
		}
		if err := driver.SiteRuntime(ctx, gateway.DesiredState{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	// A real gateway transition is rejected before it starts a competing
	// Docker runtime or changes the existing protocol frontend.
	runtime := &fakeGatewayRuntimeProvisioner{}
	layer4 := &fakeLayer4Provisioner{}
	driver := &ManagedGatewayDriver{Caddy: &CaddyGatewayDriver{SiteRuntime: func(context.Context, gateway.DesiredState, []CaddySite) error { return nil }}, Runtime: runtime, Layer4: layer4}
	if err := driver.ApplyConfiguration(ctx, gatewayState(1, 3000), nil); err == nil || len(runtime.states) != 0 || layer4.removed != 0 {
		t.Fatalf("unapproved runtime migration mutated services: %v", err)
	}
}

func TestSharedCaddyNativeRuntimeSelectionRequiresPrivateReceipt(t *testing.T) {
	root := packageTestDirectory(t)
	if found, err := NativeCaddyRuntimeConfigured(root); err != nil || found {
		t.Fatalf("absent native runtime = %v, %v", found, err)
	}
	path := filepath.Join(root, "shared-caddy", "runtime.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2} {
		raw, _ := json.Marshal(nativeCaddyReceipt{Version: version, SHA256: strings.Repeat("a", 64), UnitSHA256: strings.Repeat("b", 64), BootstrapSHA256: strings.Repeat("c", 64)})
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if found, err := NativeCaddyRuntimeConfigured(root); err != nil || !found {
			t.Fatalf("existing native runtime %d = %v, %v", version, found, err)
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NativeCaddyRuntimeConfigured(root); err == nil {
		t.Fatal("accepted an exposed runtime receipt")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if _, err := NativeCaddyRuntimeConfigured(root); err == nil {
		t.Fatal("accepted a symlink runtime receipt")
	}
}

func TestSharedCaddyWebsitePortConflictsRespectTransport(t *testing.T) {
	key, private := nodeSiteTestKey(t)
	certificate, err := nodeSiteCertificate("site.example.com", nodeSiteTestChain(t, key, "site.example.com"), private)
	if err != nil {
		t.Fatal(err)
	}
	site := CaddySite{ApplicationID: "site-instance", Hostname: certificate.Hostname, Address: "100.64.0.2", HTTPPort: 24481, TLSPort: 24483, PublicListen: "203.0.113.2:443", HTML: "owned site", Certificate: certificate}
	state := gateway.DesiredState{Revision: 1, Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.2", HTTPPort: 80, HTTPSPort: 443}}}
	exposed, bindings, err := gatewayruntime.DockerPorts(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = addWebsiteDockerPorts(exposed, bindings, state, []CaddySite{site}); err != nil {
		t.Fatalf("TCP website must coexist with gateway HTTPS/HTTP3: %v", err)
	}
	if len(bindings[dockernetwork.MustParsePort("443/udp")]) != 1 {
		t.Fatal("website changed the existing HTTP3 listener")
	}
	// A different container TCP listener claiming the same host socket remains
	// an actual conflict; allowing UDP coexistence must not weaken this guard.
	tcp := dockernetwork.MustParsePort("443/tcp")
	bindings[dockernetwork.MustParsePort("8443/tcp")] = bindings[tcp]
	delete(bindings, tcp)
	if err = addWebsiteDockerPorts(exposed, bindings, state, []CaddySite{site}); err == nil {
		t.Fatal("website accepted a conflicting TCP gateway binding")
	}
}

func TestLoopbackTLSDockerHandoffPreservesUDP443(t *testing.T) {
	key, private := nodeSiteTestKey(t)
	certificate, err := nodeSiteCertificate("site.example.com", nodeSiteTestChain(t, key, "site.example.com"), private)
	if err != nil {
		t.Fatal(err)
	}
	site := CaddySite{ApplicationID: "site-instance", Hostname: certificate.Hostname, Address: "100.64.0.2", HTTPPort: 24481, TLSPort: 24483, PublicListen: "203.0.113.2:443", HTML: "owned site", Certificate: certificate, TLSLoopbackOnly: true}
	state := gateway.DesiredState{Revision: 1, Listeners: []gateway.Listener{{Kind: "public", Address: "203.0.113.2", HTTPPort: 80, HTTPSPort: 443}}}
	exposed, bindings, err := gatewayruntime.DockerPorts(state)
	if err != nil {
		t.Fatal(err)
	}
	udp := dockernetwork.MustParsePort("443/udp")
	originalUDP := append([]dockernetwork.PortBinding(nil), bindings[udp]...)
	if err = addWebsiteDockerPorts(exposed, bindings, state, []CaddySite{site}); err != nil {
		t.Fatal(err)
	}
	if len(bindings[dockernetwork.MustParsePort("443/tcp")]) != 0 {
		t.Fatal("Caddy retained delegated TCP443")
	}
	if len(bindings[udp]) != len(originalUDP) || len(originalUDP) != 1 || bindings[udp][0] != originalUDP[0] {
		t.Fatal("UDP443 changed")
	}
	if len(bindings[dockernetwork.MustParsePort("24483/tcp")]) != 1 {
		t.Fatal("private TLS backend missing")
	}
	site.TLSLoopbackOnly = false
	if err = addWebsiteDockerPorts(exposed, bindings, state, []CaddySite{site}); err != nil {
		t.Fatal(err)
	}
	if len(bindings[dockernetwork.MustParsePort("443/tcp")]) != 1 {
		t.Fatal("forward restore lost public443")
	}
}

func nodeSiteTestChain(t *testing.T, key *ecdsa.PrivateKey, host string) string {
	t.Helper()
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), DNSNames: []string{host}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(60 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw}))
}
func nodeSiteTestTask() nodesite.Task {
	return nodesite.Task{ApplicationID: "site-instance", AppKey: "vastora-official/daylight", Hostname: "us1.example.com", Port: 24482, Revision: 1, Action: "apply"}
}
func nodeSiteTestConfig() []byte {
	return []byte(`{"admin":{"disabled":true},"apps":{"http":{"servers":{"private":{"listen":["127.0.0.1:24481"],"routes":[{"match":[{"host":["us1.example.com"],"path":["/"]}],"handle":[{"handler":"static_response","body":"old page"}]}]},"public":{"listen":["[::1]:443"]}}},"tls":{"automation":{"policies":[{}]}}}}`)
}

func nodeSiteTestKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
}
func TestNodeSiteDeliveredPairRejectsWrongIdentity(t *testing.T) {
	key, privateKey := nodeSiteTestKey(t)
	chain := nodeSiteTestChain(t, key, "us1.example.com")
	if _, err := nodeSiteCertificate("us1.example.com", chain, privateKey); err != nil {
		t.Fatal(err)
	}
	_, otherKey := nodeSiteTestKey(t)
	if _, err := nodeSiteCertificate("us1.example.com", chain, otherKey); err == nil {
		t.Fatal("mismatched private key accepted")
	}
	if _, err := nodeSiteCertificate("hk1.example.com", chain, privateKey); err == nil {
		t.Fatal("another node hostname accepted")
	}
	task := nodeSiteTestTask()
	task.CertificatePEM = chain
	if task.Validate() == nil {
		t.Fatal("apply without private key accepted")
	}
	task.PrivateKeyPEM = privateKey
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.nodeSiteHost(context.Background(), task); err == nil {
		t.Fatal("uninstalled application accepted")
	}
}

func TestNodeSiteAgentMigrationPreservesExistingState(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	keyBefore, err := os.ReadFile(filepath.Join(dir, "agent.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP TABLE shared_caddy_sites; DROP TABLE node_site_journal; PRAGMA user_version=22;`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != agentSchemaVersion {
		t.Fatal("migration version", version, err)
	}
	keyAfter, _ := os.ReadFile(filepath.Join(dir, "agent.key"))
	if !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("migration changed Agent encryption key")
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "schema-22-backup-*", "agent.db"))
	if len(backups) != 1 {
		t.Fatalf("expected one consistent recovery point, got %d", len(backups))
	}
	b, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err = b.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 22 {
		t.Fatal("backup is not pre-migration", err)
	}
	if err = VerifyRecoveryDatabaseKey(context.Background(), b, keyBefore); err != nil {
		t.Fatal("backup key binding", err)
	}
}

type nodeSiteRuntimeFixture struct {
	host            nodeSiteHost
	path            string
	mu              sync.Mutex
	restarts        int
	rejectCandidate bool
	certificate     *tls.Certificate
	admin           *httptest.Server
	https           *httptest.Server
}

func newNodeSiteRuntimeFixture(t *testing.T) *nodeSiteRuntimeFixture {
	t.Helper()
	f := &nodeSiteRuntimeFixture{path: filepath.Join(t.TempDir(), "caddy.json")}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "us1.example.com" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "packaged page")
	})
	private := httptest.NewServer(handler)
	t.Cleanup(private.Close)
	_, portText, _ := net.SplitHostPort(private.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	f.host.site = CaddySite{ApplicationID: "site-instance", Hostname: "us1.example.com", Address: "127.0.0.1", HTTPPort: port, HTML: "packaged page"}
	f.https = httptest.NewUnstartedServer(handler)
	f.https.EnableHTTP2 = true
	f.https.TLS = &tls.Config{MinVersion: tls.VersionTLS13, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.certificate == nil {
			return nil, errors.New("no configured certificate")
		}
		return f.certificate, nil
	}}
	f.https.StartTLS()
	t.Cleanup(f.https.Close)
	f.admin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/load" {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.restarts++
		var c struct {
			Apps struct {
				TLS struct {
					Certificates struct {
						Load []struct{ Certificate, Key string } `json:"load_pem"`
					} `json:"certificates"`
				} `json:"tls"`
				HTTP struct {
					Servers map[string]struct {
						Listen []string `json:"listen"`
					}
				} `json:"http"`
			} `json:"apps"`
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if len(c.Apps.TLS.Certificates.Load) > 0 && f.rejectCandidate {
			w.WriteHeader(400)
			return
		}
		if len(c.Apps.TLS.Certificates.Load) > 0 {
			pair, err := tls.X509KeyPair([]byte(c.Apps.TLS.Certificates.Load[0].Certificate), []byte(c.Apps.TLS.Certificates.Load[0].Key))
			if err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			f.certificate = &pair
		}
		w.WriteHeader(200)
	}))
	t.Cleanup(f.admin.Close)
	if err := os.WriteFile(f.path, nodeSiteTestConfig(), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *nodeSiteRuntimeFixture) attach(t *testing.T, store *Store) {
	t.Helper()
	driver, err := NewCaddyGatewayDriver(f.admin.URL)
	if err != nil {
		t.Fatal(err)
	}
	driver.SiteRuntime = func(context.Context, gateway.DesiredState, []CaddySite) error { return nil }
	if err := store.ConfigureSharedCaddy(driver); err != nil {
		t.Fatal(err)
	}
}
func (f *nodeSiteRuntimeFixture) loads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restarts
}
func freeNodeSitePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return p
}

func TestNodeSiteActivationReplayAndRollback(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(strconv.FormatBool(failure), func(t *testing.T) {
			s, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			f := newNodeSiteRuntimeFixture(t)
			f.attach(t, s)
			f.rejectCandidate = failure
			task := nodeSiteTestTask()
			task.Port = f.https.Listener.Addr().(*net.TCPAddr).Port
			key, privateKey := nodeSiteTestKey(t)
			task.PrivateKeyPEM = privateKey
			task.CertificatePEM = nodeSiteTestChain(t, key, task.Hostname)
			result, err := s.executeNodeSiteOnHost(ctx, task, f.host)
			if failure {
				if err == nil || result.State != "rolled_back" {
					t.Fatalf("failed activation was not rolled back: %s %v", result.State, err)
				}
			} else {
				if err != nil || result.State != "applied" {
					t.Fatal("activation", result.State, err)
				}
				before := f.loads()
				if _, err = s.executeNodeSiteOnHost(ctx, task, f.host); err != nil || f.loads() != before {
					t.Fatal("response replay restarted website", err)
				}
				task.Action = "rollback"
				task.CertificatePEM = ""
				task.PrivateKeyPEM = ""
				result, err = s.executeNodeSiteOnHost(ctx, task, f.host)
				if err != nil || result.State != "rolled_back" {
					t.Fatal("explicit rollback", err)
				}
			}
			raw, _ := os.ReadFile(f.path)
			if !bytes.Equal(raw, nodeSiteTestConfig()) {
				t.Fatal("previous configuration not restored exactly")
			}
			journal, err := s.readNodeSiteJournal(ctx, task.ApplicationID)
			if err != nil || journal.State != "rolled_back" {
				t.Fatal("rollback not persisted", err)
			}
		})
	}
}

func TestNodeSiteInterruptedJournalRecoveryAndConflict(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(strconv.FormatBool(conflict), func(t *testing.T) {
			dir := t.TempDir()
			s, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			f := newNodeSiteRuntimeFixture(t)
			task := nodeSiteTestTask()
			task.Port = f.https.Listener.Addr().(*net.TCPAddr).Port
			key, privateKey := nodeSiteTestKey(t)
			task.CertificatePEM = nodeSiteTestChain(t, key, task.Hostname)
			task.PrivateKeyPEM = privateKey
			previous := f.host.site
			site := previous
			site.TLSPort, site.Certificate = task.Port, gateway.Certificate{Hostname: task.Hostname, CertificatePEM: task.CertificatePEM, PrivateKeyPEM: task.PrivateKeyPEM}
			before, _ := json.Marshal(previous)
			candidate, _ := json.Marshal(site)
			j := nodeSiteJournal{Runtime: 2, Task: task, State: "applying", Previous: before, Candidate: candidate}
			if err = s.saveNodeSiteJournal(ctx, j); err != nil {
				t.Fatal(err)
			}
			var sealed []byte
			if err = s.db.QueryRow(`SELECT sealed_state FROM node_site_journal`).Scan(&sealed); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(sealed, j.Previous) {
				t.Fatal("recovery configuration is not encrypted")
			}
			if conflict {
				site.ApplicationID = "another-owner"
			}
			if err = s.saveCaddySite(ctx, caddySiteJournal{ApplicationID: task.ApplicationID, State: "applied", Site: &site}); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			f.attach(t, s)
			err = s.recoverNodeSiteJournals(ctx, func(ctx context.Context, saved nodesite.Task) (nodeSiteHost, error) {
				if saved.ApplicationID != task.ApplicationID {
					t.Fatal("wrong recovery owner")
				}
				return f.host, nil
			})
			if conflict {
				if err == nil || f.loads() != 0 {
					t.Fatal("independent change overwritten")
				}
			} else {
				state, stateErr := s.readCaddySite(ctx, task.ApplicationID)
				if err != nil || stateErr != nil || state.Site == nil || state.Site.TLSPort != 0 || f.loads() != 1 {
					t.Fatal("interrupted recovery failed", err)
				}
				again := f.loads()
				if err = s.recoverNodeSiteJournals(ctx, func(context.Context, nodesite.Task) (nodeSiteHost, error) {
					t.Fatal("completed recovery was rerun")
					return f.host, nil
				}); err != nil || f.loads() != again {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNodeSiteConfigurationPreservesPrivateAndPublicListeners(t *testing.T) {
	task := nodeSiteTestTask()
	key, privateKey := nodeSiteTestKey(t)
	chain := nodeSiteTestChain(t, key, task.Hostname)
	site := CaddySite{ApplicationID: task.ApplicationID, Hostname: task.Hostname, Address: "127.0.0.1", HTTPPort: 25481, TLSPort: task.Port, HTML: "packaged page", Certificate: gateway.Certificate{Hostname: task.Hostname, CertificatePEM: chain, PrivateKeyPEM: privateKey}}
	raw, err := composeCaddySites(nodeSiteTestConfig(), gateway.DesiredState{}, []CaddySite{site}, false)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	apps := cfg["apps"].(map[string]any)
	servers := apps["http"].(map[string]any)["servers"].(map[string]any)
	private := servers["private"].(map[string]any)
	public := servers["public"].(map[string]any)
	origin := servers["site-"+packageIdentity(task.ApplicationID)+"-tls"].(map[string]any)
	if private["listen"].([]any)[0] != "127.0.0.1:24481" || public["listen"].([]any)[0] != "[::1]:443" || origin["listen"].([]any)[0] != "127.0.0.1:24482" {
		t.Fatal("listener ownership changed")
	}
	if !bytes.Contains(raw, []byte("old page")) {
		t.Fatal("page routes lost")
	}
	if _, ok := apps["tls"].(map[string]any)["automation"]; !ok {
		t.Fatal("website update removed gateway certificate automation")
	}
}
