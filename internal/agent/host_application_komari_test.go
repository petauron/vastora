package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/platform"
)

func TestSystemdKomariApplyAndRemove(t *testing.T) {
	t.Parallel()
	binary := []byte("komari-agent-test")
	digest := sha256.Sum256(binary)
	server := httptest.NewServer(httpHandler(binary))
	defer server.Close()
	var commands []string
	manager := SystemdHostApplicationManager{
		RootDir: t.TempDir(), HTTPClient: server.Client(), HostTarget: platform.Target{OS: platform.Linux, Architecture: platform.AMD64},
		RunCommand: func(_ context.Context, name string, arguments ...string) error {
			commands = append(commands, strings.Join(append([]string{name}, arguments...), " "))
			return nil
		},
	}
	task := komariTestTask(server.URL, hex.EncodeToString(digest[:]))
	if err := manager.ApplyKomari(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	configPath := manager.path(komariConfigPath)
	configRaw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config komariConfig
	if err := json.Unmarshal(configRaw, &config); err != nil {
		t.Fatal(err)
	}
	if config.Endpoint != "https://komari.example.test" || config.Token != "secret-token" || config.Interval != 3 || config.InfoReportInterval != 5 || !config.DisableAutoUpdate || !config.DisableWebSSH || config.IgnoreUnsafeCert || config.ProtocolVersion != 2 {
		t.Fatalf("unexpected config: %#v", config)
	}
	if info, err := os.Stat(configPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v", info.Mode().Perm())
	}
	if got, err := os.ReadFile(manager.path(komariBinaryPath)); err != nil || !reflect.DeepEqual(got, binary) {
		t.Fatalf("binary mismatch: %q, err=%v", got, err)
	}
	wantApply := []string{"systemctl daemon-reload", "systemctl enable komari-agent.service", "systemctl restart komari-agent.service", "systemctl is-active --quiet komari-agent.service"}
	if !reflect.DeepEqual(commands, wantApply) {
		t.Fatalf("commands = %#v", commands)
	}
	if err := manager.RemoveKomari(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{manager.path(komariBinaryPath), manager.path(komariConfigPath), manager.path(komariUnitPath)} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed file remains: %s (%v)", path, err)
		}
	}
}

func TestSystemdKomariApplyRollsBackFiles(t *testing.T) {
	t.Parallel()
	binary := []byte("new-binary")
	digest := sha256.Sum256(binary)
	server := httptest.NewServer(httpHandler(binary))
	defer server.Close()
	root := t.TempDir()
	manager := SystemdHostApplicationManager{
		RootDir: root, HTTPClient: server.Client(), HostTarget: platform.Target{OS: platform.Linux, Architecture: platform.AMD64},
		RunCommand: func(_ context.Context, _ string, arguments ...string) error {
			if len(arguments) != 0 && arguments[0] == "restart" {
				return errors.New("restart failed")
			}
			return nil
		},
	}
	old := map[string][]byte{komariBinaryPath: []byte("old-binary"), komariConfigPath: []byte("old-config"), komariUnitPath: []byte(komariUnitMarker + "old-unit")}
	for path, content := range old {
		fullPath := manager.path(path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.ApplyKomari(context.Background(), komariTestTask(server.URL, hex.EncodeToString(digest[:]))); err == nil {
		t.Fatal("expected service restart failure")
	}
	for path, want := range old {
		got, err := os.ReadFile(manager.path(path))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s was not restored: %q, err=%v", path, got, err)
		}
	}
}

func TestApplicationExecutorUsesNativeKomariWithoutDocker(t *testing.T) {
	t.Parallel()
	host := &fakeHostApplicationManager{}
	executor := ApplicationExecutor{DockerSocket: "unix://" + filepath.Join(t.TempDir(), "missing-docker.sock"), Host: host}
	if _, err := executor.Deploy(context.Background(), DeploymentTask{AppKey: komariKey, Operation: "install"}); err != nil {
		t.Fatal(err)
	}
	if host.applied != 1 || host.removed != 0 {
		t.Fatalf("native Komari calls after install = apply:%d remove:%d", host.applied, host.removed)
	}
	if _, err := executor.Deploy(context.Background(), DeploymentTask{AppKey: komariKey, Operation: "uninstall"}); err != nil {
		t.Fatal(err)
	}
	if host.removed != 1 {
		t.Fatalf("native Komari remove calls = %d", host.removed)
	}
}

func komariTestTask(downloadURL, digest string) DeploymentTask {
	return DeploymentTask{
		AppKey: komariKey, Operation: "install",
		Manifest: catalog.AppManifest{ID: "komari-agent", Version: "1.2.60", Artifacts: []catalog.Artifact{{Name: "komari-agent", OperatingSystem: "linux", Architecture: "amd64", URL: downloadURL, SHA256: digest}}},
		Config:   []byte(`{"endpoint":"https://komari.example.test/"}`), Secrets: []byte(`{"token":"secret-token"}`),
	}
}

func httpHandler(content []byte) *testHTTPHandler { return &testHTTPHandler{content: content} }

type testHTTPHandler struct{ content []byte }

func (handler *testHTTPHandler) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	_, _ = writer.Write(handler.content)
}

type fakeHostApplicationManager struct{ applied, removed int }

func (manager *fakeHostApplicationManager) ApplyKomari(context.Context, DeploymentTask) error {
	manager.applied++
	return nil
}

func (manager *fakeHostApplicationManager) RemoveKomari(context.Context) error {
	manager.removed++
	return nil
}
