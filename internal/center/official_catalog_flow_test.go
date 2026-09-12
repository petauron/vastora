package center

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
	"github.com/sigstore/sigstore/pkg/signature"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

// Real signed HTTPS publication -> authenticated Center API -> Agent typed
// executor -> checksum-verified filesystem install -> reported completion.
// Only systemd is simulated so this test never starts a host service. This is
// an integration test of the update workflow, not a live Komari service test.
func TestOfficialCatalogIndependentUpdateThroughInstallWorkflow(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	programVersion := Version
	now := time.Now().UTC().Truncate(time.Second)
	root := metadata.Root(now.Add(48 * time.Hour))
	root.Signed.ConsistentSnapshot = true
	signers := make(map[string][]signature.Signer)
	for _, role := range []string{"root", "timestamp", "snapshot", "targets"} {
		public, private, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		key, err := metadata.KeyFromPublicKey(public)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Signed.AddKey(key, role); err != nil {
			t.Fatal(err)
		}
		signer, err := signature.LoadSigner(private, crypto.Hash(0))
		if err != nil {
			t.Fatal(err)
		}
		signers[role] = []signature.Signer{signer}
	}
	if _, err := root.Sign(signers["root"][0]); err != nil {
		t.Fatal(err)
	}
	rootBytes, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	files := make(map[string][]byte)
	assets := make(map[string][]byte)
	unavailable := false
	var distributionMu sync.RWMutex
	distribution := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		distributionMu.RLock()
		defer distributionMu.RUnlock()
		if unavailable {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if raw, exists := assets[r.URL.Path]; exists {
			_, _ = w.Write(raw)
			return
		}
		name, valid := strings.CutPrefix(r.URL.Path, "/vastora/catalog/")
		if raw, exists := files[name]; valid && exists {
			_, _ = w.Write(raw)
			return
		}
		http.NotFound(w, r)
	}))
	defer distribution.Close()
	setUnavailable := func(value bool) {
		distributionMu.Lock()
		unavailable = value
		distributionMu.Unlock()
	}
	// This test is intentionally not parallel: install the test CA in the
	// default transport used by production FetchOfficial, and restore it on exit.
	previousTransport := http.DefaultTransport
	http.DefaultTransport = distribution.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	origin := distribution.URL + "/vastora/catalog/"
	if err := store.ConfigureOfficialCatalog(ctx, origin); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, "", false).WithOfficialCatalogTrust(origin, rootBytes)
	handler := server.Handler()
	session, csrf, err := store.CreateFirstAdmin(ctx, "catalog-admin", "test-only-long-password-not-a-production-secret")
	if err != nil {
		t.Fatal(err)
	}
	node := enrollOrchestrationNode(t, store, "catalog-test-node", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.45", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.45", LANAddress: "10.0.0.45", EnabledKinds: []string{networking.KindLAN}})
	rawCatalog, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	base, err := catalog.ParseCatalog(rawCatalog)
	if err != nil {
		t.Fatal(err)
	}
	var template catalog.AppManifest
	for _, app := range base.Apps {
		if app.ID == "komari-agent" {
			template = app
		}
	}
	if template.ID == "" {
		t.Fatal("missing compiled native executor fixture")
	}
	publish := func(revision uint64, version string) catalog.AppManifest {
		t.Helper()
		app := template
		app.Version = version
		app.Artifacts = append([]catalog.Artifact(nil), template.Artifacts...)
		for i := range app.Artifacts {
			artifact := &app.Artifacts[i]
			name := fmt.Sprintf("/artifacts/%s/%s", version, artifact.Architecture)
			// Valid platform headers with distinct fixture content, never executed.
			fixture := make([]byte, 64)
			copy(fixture, []byte("\x7fELF"))
			fixture[4], fixture[5], fixture[6] = 2, 1, 1
			binary.LittleEndian.PutUint16(fixture[16:18], 2)
			machine := map[string]uint16{"amd64": 62, "arm64": 183}[artifact.Architecture]
			if machine == 0 {
				t.Fatalf("unsupported fixture architecture %s", artifact.Architecture)
			}
			binary.LittleEndian.PutUint16(fixture[18:20], machine)
			binary.LittleEndian.PutUint32(fixture[20:24], 1)
			binary.LittleEndian.PutUint16(fixture[52:54], 64)
			fixture = append(fixture, []byte("test-only native binary "+version)...)
			distributionMu.Lock()
			assets[name] = fixture
			distributionMu.Unlock()
			digest := sha256.Sum256(fixture)
			artifact.URL, artifact.SHA256 = distribution.URL+name, hex.EncodeToString(digest[:])
		}
		value := base
		value.Apps = []catalog.AppManifest{app}
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		prior, _, err := store.OfficialCatalogTrust(ctx, "stable")
		if err != nil {
			t.Fatal(err)
		}
		target, err := json.Marshal(catalog.OfficialTarget{Source: catalog.OfficialSourceIdentity, Channel: "stable", Revision: revision, GeneratedAt: now, ExpiresAt: now.Add(time.Hour), Catalog: payload})
		if err != nil {
			t.Fatal(err)
		}
		next, err := catalog.BuildOfficialRepository(rootBytes, target, "stable", prior.Acceptance, time.Now().UTC(), signers)
		if err != nil {
			t.Fatal(err)
		}
		distributionMu.Lock()
		files = next
		distributionMu.Unlock()
		if _, err := server.RefreshCatalogSource(ctx, OfficialCatalogSourceID); err != nil {
			t.Fatal(err)
		}
		return app
	}
	create := func(operation string, config json.RawMessage, authorize bool) *httptest.ResponseRecorder {
		t.Helper()
		if config == nil {
			config = json.RawMessage(`{}`)
		}
		body, err := json.Marshal(DeploymentRequest{AgentID: node.ID, AppKey: komariAppKey, Operation: operation, Config: config})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/deployments", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		if authorize {
			request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	hostRoot := t.TempDir()
	var commands []string
	executor := agent.ApplicationExecutor{DockerSocket: "unix://" + filepath.Join(hostRoot, "absent-docker.sock"), Host: agent.SystemdHostApplicationManager{RootDir: hostRoot, HTTPClient: distribution.Client(), HostTarget: platform.Target{OS: "linux", Architecture: "amd64"}, RunCommand: func(_ context.Context, name string, args ...string) error {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil
	}}}
	execute := func(operation, version string) {
		t.Helper()
		task := claimTask(t, store, node)
		if task.Kind != "application.apply" || task.Operation != operation || task.Manifest.Version != version {
			t.Fatalf("unexpected task kind/operation/version: %s %s %s", task.Kind, task.Operation, task.Manifest.Version)
		}
		encoded, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		var delivery agent.DeploymentTask
		if err := json.Unmarshal(encoded, &delivery); err != nil {
			t.Fatal(err)
		}
		result, err := executor.Deploy(ctx, delivery)
		if err != nil {
			t.Fatal(err)
		}
		reported, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", reported, task.RequiredRuntimeGeneration); err != nil {
			t.Fatal(err)
		}
	}
	config := json.RawMessage(`{"endpoint":"https://komari.example.test","token":"test-only-enrollment-token"}`)
	first := publish(1, "1.2.60")
	if response := create("install", config, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized install returned %d", response.Code)
	}
	if response := create("install", config, true); response.Code != http.StatusCreated {
		t.Fatalf("install returned %d: %s", response.Code, response.Body.String())
	}
	execute("install", first.Version)
	installedPath := filepath.Join(hostRoot, "opt/komari/agent")
	firstBinary, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatal(err)
	}
	firstCommands := len(commands)
	second := publish(2, "1.2.61")
	apps, err := store.ListApps(ctx)
	if err != nil || len(apps) != 1 || apps[0].App.Version != second.Version || apps[0].InstallBlocked {
		t.Fatalf("new catalog was not offered: %v", err)
	}
	installed, err := store.ListApplications(ctx)
	if err != nil || len(installed) != 1 || installed[0].InstalledVersion != first.Version {
		t.Fatalf("refresh changed installed version: %v", err)
	}
	if pending, err := store.ClaimNextTask(ctx, node.ID, node.Credential); err != nil || pending != nil {
		t.Fatalf("catalog refresh queued a task: %v", err)
	}
	unchanged, err := os.ReadFile(installedPath)
	if err != nil || !bytes.Equal(firstBinary, unchanged) || len(commands) != firstCommands {
		t.Fatal("catalog refresh modified running installation")
	}
	if response := create("upgrade", nil, true); response.Code != http.StatusCreated {
		t.Fatalf("upgrade returned %d: %s", response.Code, response.Body.String())
	}
	execute("upgrade", second.Version)
	upgraded, err := os.ReadFile(installedPath)
	if err != nil || !bytes.Equal(upgraded, assets["/artifacts/1.2.61/amd64"]) || bytes.Equal(firstBinary, upgraded) {
		t.Fatal("explicit upgrade did not install verified new bytes")
	}
	installed, err = store.ListApplications(ctx)
	if err != nil || len(installed) != 1 || installed[0].InstalledVersion != second.Version {
		t.Fatalf("upgrade result missing: %v", err)
	}
	var agentVersion string
	if err := store.db.QueryRowContext(ctx, `SELECT version FROM agents WHERE id = ?`, node.ID).Scan(&agentVersion); err != nil {
		t.Fatal(err)
	}
	if Version != programVersion || agentVersion != "test" {
		t.Fatal("catalog publication changed program versions")
	}
	// Offer a newer version without installing it. Otherwise an upgrade would
	// be rejected as already current before reaching the freshness gate.
	publish(3, "1.2.62")
	setUnavailable(true)
	if _, err := server.RefreshCatalogSource(ctx, OfficialCatalogSourceID); err == nil {
		t.Fatal("unavailable distribution accepted")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE official_catalog_trust SET expires_at = ? WHERE channel = 'stable'`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if response := create("upgrade", nil, true); response.Code != http.StatusBadRequest {
		t.Fatalf("expired catalog upgrade returned %d", response.Code)
	}
	_, err = store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: komariAppKey, Operation: "upgrade", Config: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "refresh the official catalog before installing or upgrading") {
		t.Fatalf("upgrade was not rejected by the catalog freshness gate: %v", err)
	}
	// Configuring installed state still uses the recorded artifact; the real
	// download must remain available if that executor requires it.
	setUnavailable(false)
	if response := create("configure", json.RawMessage(`{"endpoint":"https://other.example.test"}`), true); response.Code != http.StatusCreated {
		t.Fatalf("expired catalog blocked configure: %d %s", response.Code, response.Body.String())
	}
	execute("configure", second.Version)
	setUnavailable(true)
	if response := create("uninstall", nil, true); response.Code != http.StatusCreated {
		t.Fatalf("unavailable catalog blocked uninstall: %d %s", response.Code, response.Body.String())
	}
	execute("uninstall", second.Version)
	if _, err := os.Stat(installedPath); !os.IsNotExist(err) {
		t.Fatalf("uninstall did not remove managed binary: %v", err)
	}
}
