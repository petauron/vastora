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

func TestSystemdKomariRemoveResumesFromOwnershipJournal(t *testing.T) {
	t.Parallel()
	binary := []byte("managed-binary")
	digest := sha256.Sum256(binary)
	server := httptest.NewServer(httpHandler(binary))
	defer server.Close()
	paths := []string{komariUnitPath, komariConfigPath, komariBinaryPath}
	for _, interruption := range []struct {
		name    string
		removed int
		reload  bool
	}{
		{name: "journal persisted"},
		{name: "unit removed", removed: 1},
		{name: "configuration removed", removed: 2},
		{name: "binary removed", removed: 3},
		{name: "systemd reloaded", removed: 3, reload: true},
	} {
		t.Run(interruption.name, func(t *testing.T) {
			manager := SystemdHostApplicationManager{
				RootDir: t.TempDir(), HTTPClient: server.Client(), HostTarget: platform.Target{OS: platform.Linux, Architecture: platform.AMD64},
				RunCommand: func(context.Context, string, ...string) error { return nil },
			}
			task := komariTestTask(server.URL, hex.EncodeToString(digest[:]))
			if err := manager.ApplyKomari(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			originals := make(map[string]hostFileSnapshot, len(paths))
			for _, path := range paths {
				snapshot, err := captureHostFile(manager.path(path))
				if err != nil || !snapshot.Exists {
					t.Fatalf("missing installed file %s: %v", path, err)
				}
				originals[path] = snapshot
			}
			journal, err := manager.prepareKomariRemoval()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths[:interruption.removed] {
				if err := removeJournaledKomariFile(manager.path(path), journal); err != nil {
					t.Fatal(err)
				}
			}
			if interruption.reload {
				if err := manager.run(context.Background(), "systemctl", "daemon-reload"); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := manager.RemoveKomari(context.Background()); err != nil {
					t.Fatal(err)
				}
				for _, path := range append(append([]string{}, paths...), komariRemovalJournalPath) {
					if _, err := os.Lstat(manager.path(path)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("managed file remains after resumed uninstall: %s (%v)", path, err)
					}
				}
			}
			if err := manager.ApplyKomari(context.Background(), task); err != nil {
				t.Fatalf("reinstall after interrupted removal: %v", err)
			}
			for path, want := range originals {
				got, err := captureHostFile(manager.path(path))
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("reinstall did not restore %s: %v", path, err)
				}
			}
		})
	}
}

func TestSystemdKomariRemoveRejectsChangedFilesBeforeStoppingService(t *testing.T) {
	t.Parallel()
	for _, changedPath := range []string{komariUnitPath, komariConfigPath, komariBinaryPath} {
		t.Run(filepath.Base(changedPath), func(t *testing.T) {
			commands := 0
			manager := SystemdHostApplicationManager{
				RootDir:    t.TempDir(),
				RunCommand: func(context.Context, string, ...string) error { commands++; return nil },
			}
			files := map[string][]byte{
				komariUnitPath: komariUnit(), komariConfigPath: []byte("configuration"), komariBinaryPath: []byte("binary"),
			}
			for path, data := range files {
				if err := writeHostFileAtomic(manager.path(path), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := manager.prepareKomariRemoval(); err != nil {
				t.Fatal(err)
			}
			files[changedPath] = []byte("operator replacement")
			if err := writeHostFileAtomic(manager.path(changedPath), files[changedPath], 0o600); err != nil {
				t.Fatal(err)
			}
			if err := manager.RemoveKomari(context.Background()); err == nil || !strings.Contains(err.Error(), "changed Komari Agent file") {
				t.Fatalf("replacement was not rejected: %v", err)
			}
			if commands != 0 {
				t.Fatalf("ran %d service commands before rejecting replacement", commands)
			}
			for path, want := range files {
				got, err := os.ReadFile(manager.path(path))
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("changed file before rejecting replacement: %s (%v)", path, err)
				}
			}
			if _, err := os.Lstat(manager.path(komariRemovalJournalPath)); err != nil {
				t.Fatalf("lost recovery journal: %v", err)
			}
		})
	}
}

func TestSystemdKomariRemoveRejectsUnprotectedJournal(t *testing.T) {
	t.Parallel()
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "public permissions", true: "symlink"}[symlink], func(t *testing.T) {
			manager := SystemdHostApplicationManager{
				RootDir:    t.TempDir(),
				RunCommand: func(context.Context, string, ...string) error { t.Fatal("untrusted journal ran a command"); return nil },
			}
			if err := writeHostFileAtomic(manager.path(komariUnitPath), komariUnit(), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.prepareKomariRemoval(); err != nil {
				t.Fatal(err)
			}
			journalPath := manager.path(komariRemovalJournalPath)
			if symlink {
				if err := os.Rename(journalPath, journalPath+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(journalPath+".original", journalPath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(journalPath, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := manager.RemoveKomari(context.Background()); err == nil {
				t.Fatal("accepted unprotected uninstall journal")
			}
			if got, err := os.ReadFile(manager.path(komariUnitPath)); err != nil || !reflect.DeepEqual(got, komariUnit()) {
				t.Fatalf("changed service with unprotected journal: %v", err)
			}
		})
	}
}

func TestSystemdKomariRemoveFailsClosedWithoutOwnershipEvidence(t *testing.T) {
	t.Parallel()
	manager := SystemdHostApplicationManager{RootDir: t.TempDir(), RunCommand: func(context.Context, string, ...string) error { return nil }}
	if err := writeHostFileAtomic(manager.path(komariBinaryPath), []byte("unproven-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := manager.RemoveKomari(context.Background())
	if err == nil || !strings.Contains(err.Error(), "without Vastora ownership evidence") {
		t.Fatalf("unproven residual file was not rejected: %v", err)
	}
	if _, err := os.Stat(manager.path(komariBinaryPath)); err != nil {
		t.Fatalf("unproven binary was changed: %v", err)
	}
}

func TestSystemdKomariRemoveRetriesDaemonReload(t *testing.T) {
	t.Parallel()
	reloads := 0
	manager := SystemdHostApplicationManager{
		RootDir: t.TempDir(),
		RunCommand: func(_ context.Context, _ string, arguments ...string) error {
			if len(arguments) != 0 && arguments[0] == "daemon-reload" {
				reloads++
				if reloads == 1 {
					return errors.New("reload failed")
				}
			}
			return nil
		},
	}
	for path, content := range map[string][]byte{
		komariUnitPath:   []byte(komariUnitMarker + "[Service]\n"),
		komariConfigPath: []byte("config"),
		komariBinaryPath: []byte("binary"),
	} {
		if err := writeHostFileAtomic(manager.path(path), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.RemoveKomari(context.Background()); err == nil {
		t.Fatal("expected first daemon reload to fail")
	}
	if _, err := os.Stat(manager.path(komariRemovalJournalPath)); err != nil {
		t.Fatalf("uninstall journal was not retained: %v", err)
	}
	if err := manager.RemoveKomari(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reloads != 2 {
		t.Fatalf("daemon reload calls = %d", reloads)
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
	if _, err := executor.Deploy(context.Background(), komariTestTask("https://example.invalid/komari-agent", strings.Repeat("0", 64))); err != nil {
		t.Fatal(err)
	}
	if host.applied != 1 || host.removed != 0 {
		t.Fatalf("native Komari calls after install = apply:%d remove:%d", host.applied, host.removed)
	}
	if _, err := executor.Deploy(context.Background(), DeploymentTask{ID: "komari-uninstall", ApplicationID: "komari-application", AppKey: komariKey, Operation: "uninstall"}); err != nil {
		t.Fatal(err)
	}
	if host.removed != 1 {
		t.Fatalf("native Komari remove calls = %d", host.removed)
	}
}

func komariTestTask(downloadURL, digest string) DeploymentTask {
	return DeploymentTask{
		ID: "komari-install", ApplicationID: "komari-application", AppKey: komariKey, Operation: "install",
		Manifest: catalog.AppManifest{
			ID: "komari-agent", Version: "1.2.60", License: "MIT", HostAccess: true,
			Name:        catalog.LocalizedText{English: "Komari Agent", SimplifiedChinese: "Komari 探针"},
			Description: catalog.LocalizedText{English: "Komari monitoring agent.", SimplifiedChinese: "Komari 监控探针。"},
			Artifacts:   []catalog.Artifact{{Name: "komari-agent", OperatingSystem: "linux", Architecture: "amd64", URL: downloadURL, SHA256: digest}},
			Config: []catalog.ConfigField{
				{Key: "endpoint", Type: "string", Label: catalog.LocalizedText{English: "Endpoint", SimplifiedChinese: "面板地址"}, Description: catalog.LocalizedText{English: "Komari endpoint.", SimplifiedChinese: "Komari 面板地址。"}, Required: true},
				{Key: "token", Type: "string", Label: catalog.LocalizedText{English: "Token", SimplifiedChinese: "令牌"}, Description: catalog.LocalizedText{English: "Agent token.", SimplifiedChinese: "探针令牌。"}, Required: true, Secret: true},
			},
		},
		Config: []byte(`{"endpoint":"https://komari.example.test/"}`), Secrets: []byte(`{"token":"secret-token"}`),
	}
}

func httpHandler(content []byte) *testHTTPHandler { return &testHTTPHandler{content: content} }

type testHTTPHandler struct{ content []byte }

func (handler *testHTTPHandler) ServeHTTP(writer http.ResponseWriter, _ *http.Request) {
	_, _ = writer.Write(handler.content)
}

type fakeHostApplicationManager struct{ applied, restored, removed int }

func (manager *fakeHostApplicationManager) ApplyKomari(context.Context, DeploymentTask) error {
	manager.applied++
	return nil
}

func (manager *fakeHostApplicationManager) RemoveKomari(context.Context) error {
	manager.removed++
	return nil
}

func (manager *fakeHostApplicationManager) RestoreKomari(context.Context, DeploymentTask) error {
	manager.restored++
	return nil
}

func TestApplicationRestoreContinuesPastLegacyState(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.RecordApplied(ctx, AppliedInstallation{InstanceID: "legacy", AppKey: "aaa/legacy", Version: "1.0.0", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	task := komariTestTask("https://example.invalid/komari-agent", strings.Repeat("0", 64))
	if _, err := store.RecordApplied(ctx, AppliedInstallation{InstanceID: task.ID, ApplicationID: task.ApplicationID, AppKey: task.AppKey, Version: task.Manifest.Version, Manifest: task.Manifest, Config: task.Config, Secrets: task.Secrets}); err != nil {
		t.Fatal(err)
	}
	host := &fakeHostApplicationManager{}
	err = (ApplicationExecutor{Host: host}).Restore(ctx, store)
	if err == nil || !strings.Contains(err.Error(), "legacy aaa/legacy") {
		t.Fatalf("restore error = %v", err)
	}
	if host.restored != 1 {
		t.Fatalf("later valid installation restore calls = %d, want 1", host.restored)
	}
}
