package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/hostservice"
)

func TestNativeCaddyBootWaitKeepsOwnedUnitAndHandlesConfigChanges(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(root, "agent")
	manager := SystemdHostApplicationManager{RootDir: root, InitSystem: hostservice.Systemd}
	reloads := 0
	manager.RunCommand = func(_ context.Context, name string, args ...string) error {
		if name != "systemctl" || len(args) != 1 || args[0] != "daemon-reload" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		reloads++
		return nil
	}
	for _, dir := range []string{filepath.Join(dataDir, "shared-caddy"), filepath.Dir(manager.path(nativeCaddyUnitPath)), filepath.Dir(manager.path(nativeCaddyBootstrap))} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	bootstrap, _ := json.Marshal(map[string]any{"admin": map[string]any{"listen": "unix/" + nativeCaddySocket}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{}}}})
	receipt, _ := json.Marshal(nativeCaddyReceipt{Version: 1, SHA256: strings.Repeat("a", 64), UnitSHA256: sha256String([]byte(nativeCaddyUnit)), BootstrapSHA256: sha256String(bootstrap)})
	if err := os.WriteFile(manager.path(nativeCaddyBootstrap), bootstrap, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "shared-caddy", "runtime.json"), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.path(nativeCaddyUnitPath), []byte(nativeCaddyUnit), 0644); err != nil {
		t.Fatal(err)
	}
	runtime := NativeCaddySiteRuntime{StateDirectory: dataDir, Manager: manager}
	config := []byte(`{"apps":{"http":{"servers":{"a":{"listen":["100.100.100.100:24481"]},"b":{"listen":["127.0.0.1:24443"]},"c":{"listen":["[2001:db8::4]:443"]},"d":{"listen":[":8443"]}}}}}`)
	if err := runtime.SyncBootWait(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	path := manager.path(nativeCaddyBootDropin)
	unit, err := os.ReadFile(path)
	if err != nil || !ownedNativeCaddyBootUnit(unit) || !strings.Contains(string(unit), "100.100.100.100:24481") || !strings.Contains(string(unit), "[2001:db8::4]:443") || strings.Contains(string(unit), "127.0.0.1:24443") {
		t.Fatal("native Caddy boot listeners differ", err)
	}
	if err := runtime.SyncBootWait(context.Background(), config); err != nil || reloads != 1 {
		t.Fatal("unchanged listeners caused extra work", err)
	}
	base, _ := os.ReadFile(manager.path(nativeCaddyUnitPath))
	currentReceipt, _ := os.ReadFile(filepath.Join(dataDir, "shared-caddy", "runtime.json"))
	if !bytes.Equal(base, []byte(nativeCaddyUnit)) || !bytes.Equal(currentReceipt, receipt) {
		t.Fatal("the existing owned unit or receipt was modified")
	}
	if err := runtime.SyncBootWait(context.Background(), []byte(`{"apps":{"http":{"servers":{}}}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) || reloads != 2 {
		t.Fatal("obsolete owned wait remains after website removal", err)
	}
}

func TestCaddyBootWaitSynchronizesOnlyAfterAcceptedLoad(t *testing.T) {
	var status = http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/load" || r.Method != http.MethodPost {
			t.Errorf("unexpected Caddy request")
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	driver, err := NewCaddyGatewayDriver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	driver.Sites = func(context.Context) ([]CaddySite, error) { return nil, nil }
	driver.SiteRuntime = func(context.Context, gateway.DesiredState, []CaddySite) error { return nil }
	applied := 0
	driver.SiteApplied = func(_ context.Context, config []byte) error {
		if !json.Valid(config) {
			t.Fatal("accepted Caddy config was not JSON")
		}
		applied++
		return nil
	}
	if err := driver.ReloadSites(context.Background()); err != nil || applied != 1 {
		t.Fatal("accepted Caddy load did not synchronize startup wait", err)
	}
	status = http.StatusBadRequest
	if err := driver.ReloadSites(context.Background()); err == nil || applied != 1 {
		t.Fatal("rejected Caddy load changed startup wait", err)
	}
}
