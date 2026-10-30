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
	"sync"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/nodesite"
)

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
	if _, err = s.db.Exec(`DROP TABLE node_site_journal; PRAGMA user_version=22;`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 23 {
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
	mu              sync.Mutex
	listener        net.Listener
	restarts        int
	rejectCandidate bool
	running         bool
}

func newNodeSiteRuntimeFixture(t *testing.T) *nodeSiteRuntimeFixture {
	t.Helper()
	f := &nodeSiteRuntimeFixture{running: true}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		up := f.running
		f.mu.Unlock()
		if up {
			w.WriteHeader(204)
		} else {
			w.WriteHeader(503)
		}
	}))
	t.Cleanup(health.Close)
	f.host = nodeSiteHost{path: filepath.Join(t.TempDir(), "caddy.json"), unit: "only-test-site.service", executable: "verified-daylight", healthURL: health.URL}
	f.host.manager.RunInputCommand = func(ctx context.Context, in io.Reader, name string, args ...string) error {
		if name != f.host.executable || len(args) != 3 || args[0] != "validate" {
			return errors.New("unexpected validation command")
		}
		var v map[string]any
		return json.NewDecoder(in).Decode(&v)
	}
	f.host.manager.RunCommand = func(ctx context.Context, name string, args ...string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if name != "systemctl" || len(args) != 2 || args[0] != "restart" || args[1] != f.host.unit {
			return errors.New("attempted to control another service")
		}
		f.restarts++
		raw, err := os.ReadFile(f.host.path)
		if err != nil {
			return err
		}
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
			return err
		}
		if len(c.Apps.TLS.Certificates.Load) > 0 && f.rejectCandidate {
			return errors.New("injected candidate restart failure")
		}
		if f.listener != nil {
			f.listener.Close()
			f.listener = nil
		}
		f.running = true
		if len(c.Apps.TLS.Certificates.Load) == 0 {
			return nil
		}
		pair, err := tls.X509KeyPair([]byte(c.Apps.TLS.Certificates.Load[0].Certificate), []byte(c.Apps.TLS.Certificates.Load[0].Key))
		if err != nil {
			return err
		}
		address := c.Apps.HTTP.Servers["vastora-node-site"].Listen[0]
		listener, err := tls.Listen("tcp", address, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13, NextProtos: []string{"h2"}})
		if err != nil {
			return err
		}
		f.listener = listener
		go func() {
			for {
				conn, e := listener.Accept()
				if e != nil {
					return
				}
				go func() { defer conn.Close(); _ = conn.(*tls.Conn).Handshake() }()
			}
		}()
		return nil
	}
	t.Cleanup(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.listener != nil {
			f.listener.Close()
		}
	})
	if err := os.WriteFile(f.host.path, nodeSiteTestConfig(), 0600); err != nil {
		t.Fatal(err)
	}
	return f
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
			f.rejectCandidate = failure
			task := nodeSiteTestTask()
			task.Port = freeNodeSitePort(t)
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
				before := f.restarts
				if _, err = s.executeNodeSiteOnHost(ctx, task, f.host); err != nil || f.restarts != before {
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
			raw, _ := os.ReadFile(f.host.path)
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
			key, privateKey := nodeSiteTestKey(t)
			task.CertificatePEM = nodeSiteTestChain(t, key, task.Hostname)
			task.PrivateKeyPEM = privateKey
			candidate := []byte(`{"interrupted":true}`)
			j := nodeSiteJournal{Task: task, State: "applying", Previous: nodeSiteTestConfig(), Candidate: candidate}
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
			current := candidate
			if conflict {
				current = []byte(`{"independent-change":true}`)
			}
			if err = os.WriteFile(f.host.path, current, 0600); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			err = s.recoverNodeSiteJournals(ctx, func(ctx context.Context, saved nodesite.Task) (nodeSiteHost, error) {
				if saved.ApplicationID != task.ApplicationID {
					t.Fatal("wrong recovery owner")
				}
				return f.host, nil
			})
			raw, _ := os.ReadFile(f.host.path)
			if conflict {
				if err == nil || !bytes.Equal(raw, current) || f.restarts != 0 {
					t.Fatal("independent change overwritten")
				}
			} else {
				if err != nil || !bytes.Equal(raw, j.Previous) || f.restarts != 1 {
					t.Fatal("interrupted recovery failed", err)
				}
				again := f.restarts
				if err = s.recoverNodeSiteJournals(ctx, func(context.Context, nodesite.Task) (nodeSiteHost, error) {
					t.Fatal("completed recovery was rerun")
					return f.host, nil
				}); err != nil || f.restarts != again {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNodeSiteConfigurationPreservesPrivateAndPublicListeners(t *testing.T) {
	task := nodeSiteTestTask()
	raw, err := nodeSiteConfiguration(nodeSiteTestConfig(), task, "chain", "key")
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	json.Unmarshal(raw, &cfg)
	apps := cfg["apps"].(map[string]any)
	servers := apps["http"].(map[string]any)["servers"].(map[string]any)
	private := servers["private"].(map[string]any)
	public := servers["public"].(map[string]any)
	origin := servers["vastora-node-site"].(map[string]any)
	if private["listen"].([]any)[0] != "127.0.0.1:24481" || public["listen"].([]any)[0] != "[::1]:443" || origin["listen"].([]any)[0] != "127.0.0.1:24482" {
		t.Fatal("listener ownership changed")
	}
	if !bytes.Contains(raw, []byte("old page")) {
		t.Fatal("page routes lost")
	}
	if _, ok := apps["tls"].(map[string]any)["automation"]; ok {
		t.Fatal("node ACME automation retained")
	}
}
