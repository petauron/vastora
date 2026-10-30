package center

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
	"io/fs"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodesite"
	"github.com/petauron/vastora/internal/secret"
	"go.uber.org/zap"
)

func nodeSiteCenterFixture(t *testing.T) (*Store, string, string, string) {
	t.Helper()
	s := openOrchestrationStore(t)
	t.Cleanup(func() { s.Close() })
	n := enrollOrchestrationNode(t, s, "node-site-fixture", NodeCapabilities{}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
	id := "website-instance"
	if _, err := s.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at) VALUES(?,'Daylight',?,?,'vastora-official/daylight','','running','systemd','','','')`, id, n.ID, testSiteID(t, s)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,application_id,created_at,updated_at) VALUES('website-install',?,'vastora-official/daylight','0.1.0','{}','{"hostname":"us1.example.com","http_port":24481}','install','succeeded',?,'','')`, n.ID, id); err != nil {
		t.Fatal(err)
	}
	return s, n.ID, n.Credential, id
}
func claimNodeSiteForTest(t *testing.T, s *Store, agentID string) *AgentTask {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task, err := s.claimNodeSite(context.Background(), tx, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return task
}
func nodeSiteResultJSON(t *testing.T, r nodesite.Result) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"nodeSite": r})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func deliverNodeSiteForTest(t *testing.T, s *Store, app string) managedCertificate {
	t.Helper()
	certificate := testManagedCertificate(t, "us1.example.com")
	pair, err := tls.X509KeyPair([]byte(certificate.CertificatePEM), []byte(certificate.PrivateKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.NodeSite(context.Background(), app)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.deliverNodeSiteCertificate(context.Background(), app, view.Hostname, view.CADirectory, pair); err != nil {
		t.Fatal(err)
	}
	return certificate
}
func finishNodeSiteForTest(t *testing.T, s *Store, nodeID string, task *AgentTask) {
	t.Helper()
	block, _ := pem.Decode([]byte(task.NodeSite.CertificatePEM))
	raw := nodeSiteResultJSON(t, nodesite.Result{Action: "apply", Revision: task.Revision, Hostname: task.NodeSite.Hostname, State: "applied", Fingerprint: nodesite.Fingerprint(block.Bytes)})
	if err := s.completeNodeSite(context.Background(), commitProjectionOnlyForTest, nodeID, task.ID, task.Attempt, true, "", raw); err != nil {
		t.Fatal(err)
	}
}

func TestNodeSiteTaskIdentityReplayAndActualFingerprint(t *testing.T) {
	s, nodeID, _, app := nodeSiteCenterFixture(t)
	ctx := context.Background()
	before, err := s.NodeSite(ctx, app)
	if err != nil || before.State != "unconfigured" || before.Hostname != "us1.example.com" {
		t.Fatal("reading installed hostname changed certificate intent", before, err)
	}
	var queued int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM node_site_tasks`).Scan(&queued); err != nil || queued != 0 {
		t.Fatal("read created certificate work", queued, err)
	}

	if _, err := s.configureNodeSite(ctx, app, nodeSiteInput{Hostname: "other.example.com", Port: 24482}); err == nil {
		t.Fatal("unbound hostname accepted")
	}
	view, err := s.configureNodeSite(ctx, app, nodeSiteInput{Hostname: "us1.example.com", Port: 24482, CADirectory: certmagic.LetsEncryptStagingCA})
	if err != nil || view.State != "issuing" {
		t.Fatal(view, err)
	}
	certificate := deliverNodeSiteForTest(t, s, app)
	var command string
	var sealed []byte
	if err = s.db.QueryRow(`SELECT input_json FROM node_site_tasks`).Scan(&command); err != nil {
		t.Fatal(err)
	}
	if command != `{"action":"apply"}` {
		t.Fatal("task record contains more than its command")
	}
	if err = s.db.QueryRow(`SELECT sealed_key FROM node_sites`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(certificate.PrivateKeyPEM)) {
		t.Fatal("delivery key stored in plaintext")
	}
	if task := claimNodeSiteForTest(t, s, "another-agent"); task != nil {
		t.Fatal("cross-node claim allowed")
	}
	task := claimNodeSiteForTest(t, s, nodeID)
	if task == nil || task.NodeSite.PrivateKeyPEM != certificate.PrivateKeyPEM {
		t.Fatal("bound private key not delivered")
	}
	if _, err = s.configureNodeSite(ctx, app, nodeSiteInput{Hostname: view.Hostname, Port: view.Port}); err == nil {
		t.Fatal("overlapping revision allowed")
	}
	raw := nodeSiteResultJSON(t, nodesite.Result{Action: "apply", Revision: 1, Hostname: view.Hostname, State: "applied", Fingerprint: "wrong-fingerprint"})
	if err = s.completeNodeSite(ctx, commitProjectionOnlyForTest, nodeID, task.ID, task.Attempt+1, true, "", raw); err == nil {
		t.Fatal("stale attempt accepted")
	}
	if err = s.completeNodeSite(ctx, commitProjectionOnlyForTest, nodeID, task.ID, task.Attempt, true, "", raw); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = s.completeNodeSite(ctx, commitProjectionOnlyForTest, nodeID, task.ID, task.Attempt, false, "invalid confirmation", raw); err != nil {
			t.Fatal("result replay", err)
		}
	}
	view, _ = s.NodeSite(ctx, app)
	if view.State != "action_required" || view.AppliedRevision != 0 {
		t.Fatal("unverified certificate marked ready", view)
	}
	raw, _ = json.Marshal(view)
	if bytes.Contains(raw, []byte("PRIVATE KEY")) {
		t.Fatal("public view leaked key")
	}
}

func TestNodeSiteLostResponseRecoversFromRetainedExecution(t *testing.T) {
	s, nodeID, credential, app := nodeSiteCenterFixture(t)
	ctx := context.Background()
	if _, err := s.configureNodeSite(ctx, app, nodeSiteInput{Hostname: "us1.example.com", Port: 24482}); err != nil {
		t.Fatal(err)
	}
	deliverNodeSiteForTest(t, s, app)
	task := claimNodeSiteForTest(t, s, nodeID)
	session := "node-site-original-execution-session"
	if err := s.RegisterExecutionSession(ctx, nodeID, credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	auth, err := s.PersistExecutionAuthorization(ctx, nodeID, session, *task)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.StartExecution(ctx, nodeID, session, auth.ID, auth.Digest); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(task.NodeSite.CertificatePEM))
	raw := nodeSiteResultJSON(t, nodesite.Result{Action: "apply", Revision: 1, Hostname: "us1.example.com", State: "applied", Fingerprint: nodesite.Fingerprint(block.Bytes)})
	if err = s.StoreExecutionResult(ctx, nodeID, session, auth.ID, raw, true, false, "", nil, false); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterExecutionSession(ctx, nodeID, credential, "node-site-replacement-execution-session", controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	var state, phase string
	if err = s.db.QueryRow(`SELECT state,phase FROM task_executions WHERE id=?`, auth.ID).Scan(&state, &phase); err != nil {
		t.Fatal(err)
	}
	view, _ := s.NodeSite(ctx, app)
	if state != "succeeded" || phase != "reported" || view.State != "ready" {
		t.Fatal("retained result not reconciled", state, phase, view)
	}
	var count int
	s.db.QueryRow(`SELECT COUNT(*) FROM node_site_tasks`).Scan(&count)
	if count != 1 {
		t.Fatal("response recovery created duplicate work")
	}
}

func TestNodeSiteCenterMigration127PreservesSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO settings(key,value) VALUES('node-site-preserved','original'); DROP TABLE node_site_certmagic_storage; DROP TABLE node_site_tasks; DROP TABLE node_sites; DELETE FROM goose_db_version WHERE version_id=127; PRAGMA user_version=126;`)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var value string
	if err = s.db.QueryRow(`SELECT value FROM settings WHERE key='node-site-preserved'`).Scan(&value); err != nil || value != "original" {
		t.Fatal("existing state changed", err)
	}
	var version int
	s.db.QueryRow(`PRAGMA user_version`).Scan(&version)
	if version != 127 {
		t.Fatal("wrong migrated version", version)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "migration-backups", "center-v126-before-v127-*.db"))
	if len(backups) != 1 {
		t.Fatal("expected one consistent backup", len(backups))
	}
	b, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.QueryRow(`PRAGMA user_version`).Scan(&version)
	if version != 126 {
		t.Fatal("backup is not original schema")
	}
}

// Synthetic issuance exercises CertMagic's real obtain/load/renew/storage flow,
// without external DNS, a public CA, Linux services or production fixtures.
type nodeSiteTestIssuer struct {
	calls int
	key   *ecdsa.PrivateKey
}

func (i *nodeSiteTestIssuer) IssuerKey() string { return "node-site-test-ca" }
func (i *nodeSiteTestIssuer) Issue(ctx context.Context, request *x509.CertificateRequest) (*certmagic.IssuedCertificate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	i.calls++
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(int64(i.calls)), DNSNames: request.DNSNames, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, template, template, request.PublicKey, i.key)
	if err != nil {
		return nil, err
	}
	return &certmagic.IssuedCertificate{Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})}, nil
}
func nodeSiteTestManager(s *Store, issuer certmagic.Issuer) (*certmagic.Config, *certmagic.Cache) {
	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) { return cfg, nil }, Logger: zap.NewNop()})
	cfg = certmagic.New(cache, certmagic.Config{Storage: nodeSiteStorage{store: s}, Issuers: []certmagic.Issuer{issuer}, Logger: zap.NewNop()})
	return cfg, cache
}
func TestNodeSiteCertMagicRestartRenewalAndNodeKeys(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer := &nodeSiteTestIssuer{key: caKey}
	cfg, cache := nodeSiteTestManager(s, issuer)
	if err = cfg.ManageSync(ctx, []string{"us1.example.com", "hk1.example.com"}); err != nil {
		t.Fatal(err)
	}
	first := cache.AllMatchingCertificates("us1.example.com")[0]
	second := cache.AllMatchingCertificates("hk1.example.com")[0]
	firstKey, _ := x509.MarshalPKCS8PrivateKey(first.PrivateKey)
	secondKey, _ := x509.MarshalPKCS8PrivateKey(second.PrivateKey)
	if bytes.Equal(firstKey, secondKey) {
		t.Fatal("nodes share a private key")
	}
	if err = verifyCenterEncryptedState(ctx, s.db, s.key); err != nil {
		t.Fatal("encrypted certificate state lacks key binding", err)
	}
	cache.Stop()
	s.Close()
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg, cache = nodeSiteTestManager(s, issuer)
	defer cache.Stop()
	if err = cfg.ManageSync(ctx, []string{"us1.example.com", "hk1.example.com"}); err != nil {
		t.Fatal(err)
	}
	if issuer.calls != 2 {
		t.Fatal("restart issued another certificate", issuer.calls)
	}
	reloaded := cache.AllMatchingCertificates("us1.example.com")[0]
	reloadedKey, _ := x509.MarshalPKCS8PrivateKey(reloaded.PrivateKey)
	if !bytes.Equal(firstKey, reloadedKey) {
		t.Fatal("restart changed private key")
	}
	if err = cfg.RenewCertSync(ctx, "us1.example.com", true); err != nil {
		t.Fatal(err)
	}
	renewed, err := cfg.CacheManagedCertificate(ctx, "us1.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(renewed.Certificate.Certificate[0], first.Certificate.Certificate[0]) || issuer.calls != 3 {
		t.Fatal("renewal did not produce new certificate")
	}
	if _, err = nodeSiteDirectory("http://127.0.0.1/internal"); err == nil {
		t.Fatal("unapproved CA accepted")
	}
}
func TestNodeSiteEncryptedStorageContract(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	storage := nodeSiteStorage{store: s}
	for _, key := range []string{"one/private.key", "one/nested/account.key", "other/value"} {
		if err := storage.Store(ctx, key, []byte("secret-private-material")); err != nil {
			t.Fatal(err)
		}
	}
	var sealed []byte
	if err := s.db.QueryRow(`SELECT sealed FROM node_site_certmagic_storage WHERE key='one/private.key'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("secret-private-material")) {
		t.Fatal("CertMagic private material stored in plaintext")
	}
	keys, err := storage.List(ctx, "one", false)
	if err != nil || !reflect.DeepEqual(keys, []string{"one/nested", "one/private.key"}) {
		t.Fatal(keys, err)
	}
	info, err := storage.Stat(ctx, "one")
	if err != nil || info.IsTerminal {
		t.Fatal(info, err)
	}
	if err = storage.Lock(ctx, "test-lock"); err != nil {
		t.Fatal(err)
	}
	if err = storage.Unlock(ctx, "test-lock"); err != nil {
		t.Fatal(err)
	}
	// Ciphertext cannot be transplanted to another account/asset name.
	if _, err = s.db.Exec(`UPDATE node_site_certmagic_storage SET sealed=? WHERE key='other/value'`, sealed); err != nil {
		t.Fatal(err)
	}
	if _, err = storage.Load(ctx, "other/value"); err == nil {
		t.Fatal("cross-asset ciphertext accepted")
	}
	if err = storage.Delete(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	if storage.Exists(ctx, "one") {
		t.Fatal("prefix deletion left an asset")
	}
	if _, err = storage.Load(ctx, "one/private.key"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("missing asset", err)
	}
	if err = storage.Delete(ctx, "missing"); err != nil {
		t.Fatal("idempotent delete", err)
	}
}
func TestNodeSiteConfigurationReuseAndRenewalDelivery(t *testing.T) {
	s, nodeID, _, app := nodeSiteCenterFixture(t)
	ctx := context.Background()
	input := nodeSiteInput{Hostname: "us1.example.com", Port: 24482}
	if _, err := s.configureNodeSite(ctx, app, input); err != nil {
		t.Fatal(err)
	}
	deliverNodeSiteForTest(t, s, app)
	first := claimNodeSiteForTest(t, s, nodeID)
	finishNodeSiteForTest(t, s, nodeID, first)
	same, err := s.configureNodeSite(ctx, app, input)
	if err != nil || same.Revision != 1 || same.State != "ready" {
		t.Fatal("identical PUT changed ready certificate", same, err)
	}
	input.Port++
	changed, err := s.configureNodeSite(ctx, app, input)
	if err != nil || changed.Revision != 2 {
		t.Fatal(changed, err)
	}
	task := claimNodeSiteForTest(t, s, nodeID)
	if task == nil || task.NodeSite.PrivateKeyPEM != first.NodeSite.PrivateKeyPEM {
		t.Fatal("port change did not reuse private key")
	}
	// A renewal arriving during an active execution must not replace its material.
	deliverNodeSiteForTest(t, s, app)
	view, _ := s.NodeSite(ctx, app)
	if view.Revision != 2 {
		t.Fatal("renewal bypassed execution fence")
	}
	finishNodeSiteForTest(t, s, nodeID, task)
	deliverNodeSiteForTest(t, s, app)
	view, _ = s.NodeSite(ctx, app)
	if view.Revision != 3 || view.State != "pending" {
		t.Fatal("new managed certificate not queued", view)
	}
	if task = claimNodeSiteForTest(t, s, nodeID); task == nil || task.NodeSite.PrivateKeyPEM == first.NodeSite.PrivateKeyPEM {
		t.Fatal("renewed key not delivered")
	}
	// The saved key is bound to the application, never a portable wildcard key.
	var sealed []byte
	s.db.QueryRow(`SELECT sealed_key FROM node_sites WHERE application_id=?`, app).Scan(&sealed)
	if _, err = secret.Open(s.key, sealed, []byte("node-site-certificate:another-application")); err == nil {
		t.Fatal("cross-app key decryption allowed")
	}
}

func TestNodeSiteDNSReusesCurrentCenterOAuth(t *testing.T) {
	s, _, _, app := nodeSiteCenterFixture(t)
	ctx := context.Background()
	token := cloudflareOAuthToken{AccessToken: "synthetic-first", RefreshToken: "synthetic-refresh", Scope: "zone.read dns.write", ExpiresAt: time.Now().Add(time.Hour)}
	storeCloudflareOAuthIntegration(t, s, token)
	expected := token.AccessToken
	requests := 0
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer "+expected || r.URL.Path != "/zones" {
			t.Error("DNS client did not reuse the current authorized integration")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	s.cloudflareOAuth.APIURL = endpoint.URL
	s.cloudflareOAuth.HTTPClient = endpoint.Client()
	client := nodeSiteDNSClient{store: s}
	for range 2 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.cloudflare.com/client/v4/zones?name=example.com", nil)
		req.Header.Set("Authorization", "Bearer stale-provider-snapshot")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		var id string
		if err = s.db.QueryRow(`SELECT secret_id FROM network_integrations WHERE kind='cloudflare'`).Scan(&id); err != nil {
			t.Fatal(err)
		}
		token.AccessToken = "synthetic-refreshed"
		expected = token.AccessToken
		if err = s.saveCloudflareOAuthToken(ctx, id, token); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://unapproved.example.com/client/v4/zones", nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("DNS credentials can be sent to an unapproved destination")
	}
	if requests != 2 {
		t.Fatal("unapproved request reached the integration")
	}
	if _, err := s.newNodeSiteEngine(ctx, "missing", "us1.other.example", certmagic.LetsEncryptProductionCA); err == nil {
		t.Fatal("hostname outside the configured zone accepted")
	}
	if _, err := s.configureNodeSite(ctx, app, nodeSiteInput{Hostname: "us1.example.com", Port: 24482}); err != nil {
		t.Fatal(err)
	}
	cfg, cache := nodeSiteTestManager(s, &nodeSiteTestIssuer{})
	issuer := certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{CA: certmagic.LetsEncryptProductionCA})
	issuerKey := issuer.IssuerKey()
	cache.Stop()
	storage := nodeSiteStorage{store: s}
	for key, value := range map[string]string{
		certmagic.StorageKeys.SiteCert(issuerKey, "us1.example.com"):       "invalid-synthetic-chain",
		certmagic.StorageKeys.SitePrivateKey(issuerKey, "us1.example.com"): "invalid-synthetic-key",
		certmagic.StorageKeys.SiteMeta(issuerKey, "us1.example.com"):       `{"sans":["us1.example.com"]}`,
	} {
		if err := storage.Store(ctx, key, []byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	engine, err := s.newNodeSiteEngine(ctx, app, "us1.example.com", certmagic.LetsEncryptProductionCA)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { engine.cancel(); engine.cache.Stop() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		view, err := s.NodeSite(ctx, app)
		if err != nil {
			t.Fatal(err)
		}
		if view.Error != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("asynchronous certificate load failure was hidden")
		}
		time.Sleep(10 * time.Millisecond)
	}

}
