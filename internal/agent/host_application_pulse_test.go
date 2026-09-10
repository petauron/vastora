package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/pulse"
)

func testPulseArchive(t *testing.T, name string, typeflag byte, duplicate bool) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	writer := tar.NewWriter(gzipWriter)
	data := []byte("\x7fELFfake-pulse-binary")
	count := 1
	if duplicate {
		count = 2
	}
	for range count {
		header := &tar.Header{Name: name, Typeflag: typeflag, Mode: 0o755}
		if typeflag == tar.TypeReg {
			header.Size = int64(len(data))
		} else {
			header.Linkname = "/etc/passwd"
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := writer.Write(data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestPulseArchiveExtractsOnlyExactRegularExecutable(t *testing.T) {
	name := "pulse-v0.1.0-alpha.2-linux-x86_64/pulse-agent"
	for _, test := range []struct {
		name             string
		entry            string
		kind             byte
		duplicate, valid bool
	}{
		{"regular", name, tar.TypeReg, false, true},
		{"traversal", "../../pulse-agent", tar.TypeReg, false, false},
		{"symlink", name, tar.TypeSymlink, false, false},
		{"hardlink", name, tar.TypeLink, false, false},
		{"duplicate", name, tar.TypeReg, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			binary, err := pulseArchiveBinary(testPulseArchive(t, test.entry, test.kind, test.duplicate), "0.1.0-alpha.2", "amd64")
			if (err == nil) != test.valid {
				t.Fatalf("unexpected archive result: %v", err)
			}
			if test.valid && !bytes.HasPrefix(binary, []byte("\x7fELF")) {
				t.Fatal("wrong executable")
			}
		})
	}
}

func TestPulseSupportedHostReleases(t *testing.T) {
	for _, value := range []string{"debian:12", "debian:13", "ubuntu:24.04", "ubuntu:26.04"} {
		id, version, _ := strings.Cut(value, ":")
		if !pulseSupportedOS([]byte("ID=" + id + "\nVERSION_ID=\"" + version + "\"\n")) {
			t.Fatal(value)
		}
	}
	for _, value := range []string{"ID=debian\nVERSION_ID=11", "ID=ubuntu\nVERSION_ID=22.04", "ID=alpine\nVERSION_ID=3.23"} {
		if pulseSupportedOS([]byte(value)) {
			t.Fatal("unsupported runtime accepted")
		}
	}
}

func TestPulseNativeLifecyclePreservesIdentity(t *testing.T) {
	archive := testPulseArchive(t, "pulse-v0.1.0-alpha.2-linux-x86_64/pulse-agent", tar.TypeReg, false)
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))
	defer server.Close()
	config := pulse.AgentConfig{ServiceURL: "https://pulse.private.example.com/", ServiceApplicationID: "monitor", NodeName: "node", NodeGroup: "region"}
	raw, _ := json.Marshal(config)
	task := DeploymentTask{ID: "deployment", ApplicationID: "collector", AppKey: pulse.AgentKey, Operation: "install", Config: raw, Secrets: json.RawMessage(`{"enrollment_token":"12345678901234567890123456789012"}`), Manifest: catalog.AppManifest{ID: "pulse-agent", Version: "0.1.0-alpha.2", Artifacts: []catalog.Artifact{{Name: "pulse-agent", OperatingSystem: "linux", Architecture: "amd64", URL: server.URL, SHA256: hex.EncodeToString(digest[:])}}}}
	manager := SystemdHostApplicationManager{RootDir: t.TempDir(), HTTPClient: server.Client(), HostTarget: platform.Target{OS: "linux", Architecture: "amd64"}}
	credentials := []byte(`{"protocol_version":2,"service_url":"https://pulse.private.example.com/","node_id":"stable-id","agent_token":"private-agent-token"}`)
	manager.RunCommand = func(ctx context.Context, command string, args ...string) error {
		if command == "systemctl" && len(args) > 0 && args[0] == "restart" {
			if _, err := os.Stat(manager.path(pulseCredentialsPath)); errors.Is(err, os.ErrNotExist) {
				return writeHostFileAtomic(manager.path(pulseCredentialsPath), credentials, 0o600)
			}
		}
		return nil
	}
	if err := writeHostFileAtomic(manager.path("/etc/os-release"), []byte("ID=debian\nVERSION_ID=12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApplyPulse(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	token, err := os.ReadFile(manager.path(pulseToken))
	if err != nil || len(token) != 0 {
		t.Fatal("one-time token retained")
	}
	task.Operation, task.Secrets = "upgrade", json.RawMessage(`{}`)
	if _, err := manager.ApplyPulse(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(manager.path(pulseCredentialsPath))
	if err != nil || !bytes.Equal(got, credentials) {
		t.Fatal("upgrade changed identity")
	}
	if err := manager.RestorePulse(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if err := manager.RemovePulse(context.Background(), "another-application", true); err == nil {
		t.Fatal("cross-application removal accepted")
	}
	if err := manager.RemovePulse(context.Background(), task.ApplicationID, false); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(manager.path(pulseCredentialsPath))
	if err != nil || !bytes.Equal(got, credentials) {
		t.Fatal("keep-data uninstall erased identity")
	}
}

func TestPulseInstallBeforeStartupFailureCleansManagedFiles(t *testing.T) {
	archive := testPulseArchive(t, "pulse-v0.1.0-alpha.2-linux-x86_64/pulse-agent", tar.TypeReg, false)
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))
	defer server.Close()
	config, _ := json.Marshal(pulse.AgentConfig{ServiceURL: "https://pulse.private.example.com/", ServiceApplicationID: "monitor", NodeName: "node"})
	task := DeploymentTask{ID: "deployment", ApplicationID: "collector", AppKey: pulse.AgentKey, Operation: "install", Config: config, Secrets: json.RawMessage(`{"enrollment_token":"12345678901234567890123456789012"}`), Manifest: catalog.AppManifest{Version: "0.1.0-alpha.2", Artifacts: []catalog.Artifact{{Name: "pulse-agent", OperatingSystem: "linux", Architecture: "amd64", URL: server.URL, SHA256: hex.EncodeToString(digest[:])}}}}
	manager := SystemdHostApplicationManager{RootDir: t.TempDir(), HTTPClient: server.Client(), HostTarget: platform.Target{OS: "linux", Architecture: "amd64"}}
	reloads := 0
	manager.RunCommand = func(ctx context.Context, command string, args ...string) error {
		if command == "systemctl" && args[0] == "daemon-reload" {
			reloads++
			if reloads == 1 {
				return errors.New("reload failed before starting")
			}
		}
		if command == "systemctl" && args[0] == "stop" {
			t.Fatal("must not stop a service that was never started")
		}
		return nil
	}
	if err := writeHostFileAtomic(manager.path("/etc/os-release"), []byte("ID=debian\nVERSION_ID=12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.ApplyPulse(context.Background(), task); err == nil {
		t.Fatal("failed installation reported success")
	}
	for _, path := range []string{pulseBinary, pulseEnv, pulseUnitPath, pulseDigest, pulseToken, pulseArchive} {
		if _, err := os.Lstat(manager.path(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial installation retained %s: %v", path, err)
		}
	}
}
