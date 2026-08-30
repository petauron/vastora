package center

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestInitialSetupRejectsInvalidInputBeforeAnySagaMutation(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	installer := &fakeBuiltinHeadscaleInstaller{}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	invalidInputs := []InitialSetupInput{
		{
			Site:      SiteInput{Name: "Invalid", Code: "INVALID CODE", Timezone: "UTC"},
			Network:   CenterNetworkInput{AgentConnectionMode: "headscale", AgentConnectURL: "https://center.example.com"},
			Headscale: &HeadscaleInput{Mode: "builtin", URL: "https://headscale.example.com"},
		},
		{
			Site:      SiteInput{Name: "Invalid network", Code: "invalid-network", Timezone: "UTC"},
			Network:   CenterNetworkInput{AgentConnectionMode: "headscale", AgentConnectURL: "http://center.example.com"},
			Headscale: &HeadscaleInput{Mode: "builtin", URL: "https://headscale.example.com"},
		},
	}
	for _, input := range invalidInputs {
		if _, err = server.CompleteInitialSetup(context.Background(), input); err == nil {
			t.Fatalf("invalid setup was accepted: %#v", input)
		}
	}
	for table, query := range map[string]string{
		"setup operation": `SELECT COUNT(*) FROM initial_setup_operations`,
		"Headscale":       `SELECT COUNT(*) FROM network_integrations WHERE kind = 'headscale'`,
		"site":            `SELECT COUNT(*) FROM sites`,
	} {
		var count int
		if err := store.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("invalid setup mutated %s: count=%d err=%v", table, count, err)
		}
	}
	if installer.installCalls != 0 {
		t.Fatalf("invalid setup reached the deployment helper %d time(s)", installer.installCalls)
	}
}

func TestInitialSetupResumesHeadscaleAfterPhasePersistenceFailure(t *testing.T) {
	headscale := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/user" {
			t.Fatalf("unexpected Headscale verification path: %s", request.URL.Path)
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"users": []any{}})
	}))
	defer headscale.Close()
	endpoint := fmt.Sprintf("https://example.com:%d", headscale.Listener.Addr().(*net.TCPAddr).Port)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.headscaleHTTPClient = headscale.Client()
	store.builtinHeadscaleDialAddress = headscale.Listener.Addr().String()
	if _, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	storeSystemCenterCertificateForTest(t, store, "center.example.com")
	if _, err := store.db.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)`, setupGatewayBindingSetting, `{"publicAddress":"192.9.143.79","bindAddress":"10.0.0.157"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_initial_setup_phase BEFORE UPDATE OF phase ON initial_setup_operations
		WHEN OLD.phase = 'headscale' BEGIN SELECT RAISE(ABORT, 'injected setup phase failure'); END`); err != nil {
		t.Fatal(err)
	}
	installer := &fakeBuiltinHeadscaleInstaller{endpoint: endpoint}
	server := NewServer(store, "", false).WithInfrastructureManager(installer)
	input := InitialSetupInput{
		Site:      SiteInput{Name: "Recoverable", Code: "recoverable", Timezone: "UTC"},
		Network:   CenterNetworkInput{AgentConnectionMode: "headscale", AgentConnectURL: "https://center.example.com"},
		Headscale: &HeadscaleInput{Mode: "builtin", URL: endpoint},
	}
	if _, err := server.CompleteInitialSetup(context.Background(), input); err == nil || !strings.Contains(err.Error(), "injected setup phase failure") {
		t.Fatalf("phase failure was not reported: %v", err)
	}
	operation, exists, err := store.initialSetupOperation(context.Background())
	if err != nil || !exists || operation.Phase != "headscale" || !strings.Contains(operation.LastError, "injected setup phase failure") {
		t.Fatalf("failed setup was not resumable: operation=%#v exists=%v err=%v", operation, exists, err)
	}
	if installer.installCalls != 1 {
		t.Fatalf("Headscale install calls = %d, want 1", installer.installCalls)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_initial_setup_phase`); err != nil {
		t.Fatal(err)
	}
	result, err := NewServer(store, "", false).WithInfrastructureManager(installer).CompleteInitialSetup(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Site.Code != "recoverable" || installer.installCalls != 1 || len(installer.installCommits) != 2 {
		t.Fatalf("setup did not resume idempotently: result=%#v installs=%d commits=%#v", result, installer.installCalls, installer.installCommits)
	}
}

func TestInitialSetupCommitFailureAndConcurrentReplayConverge(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.CreateFirstAdmin(context.Background(), "admin", "correct-horse-battery-staple"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_initial_site BEFORE INSERT ON sites BEGIN SELECT RAISE(ABORT, 'injected site failure'); END`); err != nil {
		t.Fatal(err)
	}
	server := NewServer(store, "", false)
	input := InitialSetupInput{
		Site:    SiteInput{Name: "Atomic", Code: "atomic", Timezone: "UTC"},
		Network: CenterNetworkInput{AgentConnectionMode: "lan", AgentConnectURL: "https://center.example.com"},
	}
	if _, err := server.CompleteInitialSetup(context.Background(), input); err == nil || !strings.Contains(err.Error(), "injected site failure") {
		t.Fatalf("commit failure was not reported: %v", err)
	}
	operation, exists, err := store.initialSetupOperation(context.Background())
	if err != nil || !exists || operation.Phase != "commit" || operation.LastError == "" {
		t.Fatalf("commit failure was not resumable: operation=%#v exists=%v err=%v", operation, exists, err)
	}
	var sites, settings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sites`).Scan(&sites); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN (?, ?)`, agentConnectionModeSetting, agentConnectURLSetting).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if sites != 0 || settings != 0 {
		t.Fatalf("failed final transaction leaked state: sites=%d settings=%d", sites, settings)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_initial_site`); err != nil {
		t.Fatal(err)
	}
	results := make(chan InitialSetupResult, 2)
	errors := make(chan error, 2)
	var start sync.WaitGroup
	start.Add(2)
	for range 2 {
		go func() {
			start.Done()
			start.Wait()
			result, err := server.CompleteInitialSetup(context.Background(), input)
			results <- result
			errors <- err
		}()
	}
	first, second := <-results, <-results
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
	if first.Site.ID == "" || first.Site.ID != second.Site.ID {
		t.Fatalf("concurrent replay diverged: first=%#v second=%#v", first, second)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sites`).Scan(&sites); err != nil || sites != 1 {
		t.Fatalf("concurrent setup created %d sites: %v", sites, err)
	}
}
