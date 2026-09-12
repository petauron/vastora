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
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/pulse"
)

func testPulseArchive(t *testing.T, name string, typeflag byte, duplicate bool) []byte {
	t.Helper()
	return testPulseArchiveData(t, name, typeflag, duplicate, testArtifactELF(t, "amd64"))
}

func testPulseArchiveData(t *testing.T, name string, typeflag byte, duplicate bool, data []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	writer := tar.NewWriter(gzipWriter)
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

func TestPulseUnitStagesOwnerOnlyEnrollmentCredential(t *testing.T) {
	unit := string(pulseUnit("collector"))
	for _, directive := range []string{
		"User=vastora-pulse\n", "Group=vastora-pulse\n",
		"LoadCredential=pulse-enrollment:" + pulseToken + "\n",
		"RuntimeDirectory=vastora-pulse-agent\n", "RuntimeDirectoryMode=0700\n",
		"Environment=PULSE_ENROLLMENT_TOKEN_FILE=" + pulseRuntimeToken + "\n",
		"StateDirectoryMode=0700\n", "UMask=0077\n", "NoNewPrivileges=yes\n",
	} {
		if !strings.Contains(unit, directive) {
			t.Fatalf("missing private credential directive: %s", directive)
		}
	}
	var command []string
	for _, line := range strings.Split(unit, "\n") {
		if value, ok := strings.CutPrefix(line, "ExecStartPre="); ok {
			command = strings.Fields(value)
		}
	}
	if len(command) != 5 || command[0] != "/usr/bin/install" || command[1] != "-m" || command[2] != "0600" || command[3] != "%d/pulse-enrollment" || command[4] != pulseRuntimeToken {
		t.Fatalf("unexpected credential preparation: %v", command)
	}
	for _, mode := range []os.FileMode{0o400, 0o440, 0o600} {
		t.Run(mode.String(), func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "systemd credential")
			destination := filepath.Join(root, "private credential")
			token := []byte("test-enrollment-token")
			if err := os.WriteFile(source, token, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(source, mode); err != nil {
				t.Fatal(err)
			}
			// Exercise the exact unit command, replacing only its runtime paths.
			// The source mode models systemd's ACL-backed 0440 credential file.
			if err := exec.Command(command[0], command[1], command[2], source, destination).Run(); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(destination)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("prepared credential is not owner-only: %v", err)
			}
			got, err := os.ReadFile(destination)
			if err != nil || !bytes.Equal(got, token) {
				t.Fatal("credential preparation changed the token")
			}
		})
	}
}

func TestPulseUnitOwnershipDoesNotDependOnTemplate(t *testing.T) {
	for _, unit := range [][]byte{pulseUnit("collector"), []byte("# Managed by Vastora\n# Application: collector\n[Service]\nDescription=Earlier unit template\n")} {
		if !pulseUnitOwnedBy(unit, "collector") || pulseUnitOwnedBy(unit, "other") || pulseUnitOwnedBy(unit, "collect") {
			t.Fatal("unit ownership must match the exact application, not the unit template")
		}
	}
	if pulseUnitOwnedBy([]byte("[Service]\n# Application: collector\n"), "collector") || pulseUnitOwnedBy(pulseUnit(""), "") {
		t.Fatal("unmanaged unit accepted")
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
			if err := writeHostFileAtomic(manager.path(pulseRuntimeToken), []byte("test-runtime-token"), 0o600); err != nil {
				return err
			}
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
	installedEnvironment, err := os.ReadFile(manager.path(pulseEnv))
	if err != nil || bytes.Contains(installedEnvironment, []byte("PULSE_INTERVAL_SECONDS=")) {
		t.Fatal("installation must use the Pulse collector's own metrics interval default")
	}
	token, err := os.ReadFile(manager.path(pulseToken))
	if err != nil || len(token) != 0 {
		t.Fatal("one-time token retained")
	}
	if _, err := os.Stat(manager.path(pulseRuntimeToken)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("runtime enrollment token retained")
	}
	// An owned unit may be regenerated as the service template evolves.
	if err := writeHostFileAtomic(manager.path(pulseUnitPath), []byte("# Managed by Vastora\n# Application: collector\n[Service]\nDescription=Earlier unit template\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyEnvironment := strings.ReplaceAll(string(pulseEnvironment(config)), "PULSE_NODE_REGION=\"\"\n", "")
	legacyEnvironment = strings.ReplaceAll(legacyEnvironment, "PULSE_GEOIP_PROVIDER=\"geojs\"\n", "")
	legacyEnvironment += "PULSE_NODE_REGION='sg'\nPULSE_GEOIP_PROVIDER=disabled\nPULSE_INTERVAL_SECONDS=30\n"
	if err := writeHostFileAtomic(manager.path(pulseEnv), []byte(legacyEnvironment), 0o600); err != nil {
		t.Fatal(err)
	}
	task.Operation, task.Secrets = "upgrade", json.RawMessage(`{}`)
	if _, err := manager.ApplyPulse(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(manager.path(pulseCredentialsPath))
	if err != nil || !bytes.Equal(got, credentials) {
		t.Fatal("upgrade changed identity")
	}
	environment, err := os.ReadFile(manager.path(pulseEnv))
	if err != nil || !bytes.Contains(environment, []byte("PULSE_NODE_REGION=\"SG\"\n")) || !bytes.Contains(environment, []byte("PULSE_GEOIP_PROVIDER=\"disabled\"\n")) {
		t.Fatal("upgrade changed retained location settings")
	}
	if bytes.Contains(environment, []byte("PULSE_INTERVAL_SECONDS=")) {
		t.Fatal("upgrade must not retain or replace the previous metrics interval override")
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

func TestPulseRetainsOnlySupportedLocationOverrides(t *testing.T) {
	base := pulse.AgentConfig{ServiceURL: "https://pulse.example.com/", ServiceApplicationID: "monitor", NodeName: "node"}
	for _, environment := range []string{
		"PULSE_NODE_REGION=SG\nPULSE_GEOIP_PROVIDER=disabled\n",
		"PULSE_NODE_REGION=\"SG\"\nPULSE_GEOIP_PROVIDER='disabled'\n",
	} {
		retained, err := pulseRetainLocation(base, []byte(environment+"PULSE_ENROLLMENT_TOKEN=must-not-be-copied\nOTHER=ignored\n"))
		if err != nil {
			t.Fatal(err)
		}
		output := string(pulseEnvironment(retained))
		if !strings.Contains(output, "PULSE_NODE_REGION=\"SG\"\n") || !strings.Contains(output, "PULSE_GEOIP_PROVIDER=\"disabled\"\n") || strings.Contains(output, "must-not-be-copied") || strings.Contains(output, "OTHER=") {
			t.Fatal("location preservation copied unsupported data or lost explicit settings")
		}
	}
	region, provider := "", "ipinfo"
	config := base
	config.NodeRegion, config.GeoIPProvider = &region, &provider
	retained, err := pulseRetainLocation(config, []byte("PULSE_NODE_REGION=SG\nPULSE_GEOIP_PROVIDER=disabled\n"))
	if err != nil || retained.NodeRegion == nil || *retained.NodeRegion != "" || retained.GeoIPProvider == nil || *retained.GeoIPProvider != "ipinfo" {
		t.Fatal("explicit administrator change did not override retained location")
	}
	for _, environment := range []string{
		"PULSE_NODE_REGION=SG\nPULSE_NODE_REGION=US\n",
		"PULSE_NODE_REGION=\"SG\n",
		"PULSE_NODE_REGION=$(id)\n",
		"PULSE_GEOIP_PROVIDER=untrusted\n",
		strings.Repeat("x", 16*1024+1),
	} {
		if _, err := pulseRetainLocation(base, []byte(environment)); err == nil {
			t.Fatal("invalid retained environment accepted")
		}
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
	if _, err := manager.ApplyPulse(context.Background(), task); err == nil || !strings.Contains(err.Error(), "reload failed before starting") || strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("failed installation lost its cause or blamed networking: %v", err)
	}
	for _, path := range []string{pulseBinary, pulseEnv, pulseUnitPath, pulseDigest, pulseToken, pulseArchive} {
		if _, err := os.Lstat(manager.path(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial installation retained %s: %v", path, err)
		}
	}
}

func TestPulseRejectsArtifactPlatformBeforeMutation(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		for _, installed := range []bool{false, true} {
			t.Run(architecture+map[bool]string{false: "/install", true: "/upgrade"}[installed], func(t *testing.T) {
				other := map[string]string{"amd64": "arm64", "arm64": "amd64"}[architecture]
				archiveArch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[architecture]
				name := "pulse-v0.1.0-alpha.2-linux-" + archiveArch + "/pulse-agent"
				archive := testPulseArchiveData(t, name, tar.TypeReg, false, testArtifactELF(t, other))
				digest := sha256.Sum256(archive) // Correct archive path/hash, wrong ELF architecture.
				server := httptest.NewTLSServer(httpHandler(archive))
				defer server.Close()
				commands := 0
				manager := SystemdHostApplicationManager{
					RootDir: t.TempDir(), HTTPClient: server.Client(), HostTarget: platform.Target{OS: "linux", Architecture: architecture},
					RunCommand: func(context.Context, string, ...string) error { commands++; return nil },
				}
				if err := writeHostFileAtomic(manager.path("/etc/os-release"), []byte("ID=debian\nVERSION_ID=12\n"), 0644); err != nil {
					t.Fatal(err)
				}
				paths := []string{pulseBinary, pulseEnv, pulseUnitPath, pulseDigest, pulseToken, pulseArchive, pulseCredentialsPath, pulseRuntimeToken}
				if installed {
					for _, file := range paths {
						if err := writeHostFileAtomic(manager.path(file), []byte("existing managed installation"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := writeHostFileAtomic(manager.path(pulseUnitPath), pulseUnit("collector"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := make(map[string]hostFileSnapshot)
				for _, file := range paths {
					var err error
					before[file], err = captureHostFile(manager.path(file))
					if err != nil {
						t.Fatal(err)
					}
				}
				config, err := json.Marshal(pulse.AgentConfig{ServiceURL: "https://pulse.private.example.com/", ServiceApplicationID: "monitor", NodeName: "node"})
				if err != nil {
					t.Fatal(err)
				}
				task := DeploymentTask{ID: "deployment", ApplicationID: "collector", AppKey: pulse.AgentKey, Operation: "install", Config: config, Secrets: json.RawMessage(`{"enrollment_token":"12345678901234567890123456789012"}`), Manifest: catalog.AppManifest{ID: "pulse-agent", Version: "0.1.0-alpha.2", Artifacts: []catalog.Artifact{{Name: "pulse-agent", OperatingSystem: "linux", Architecture: architecture, URL: server.URL, SHA256: hex.EncodeToString(digest[:])}}}}
				if installed {
					task.Operation = "upgrade"
				}
				if _, err := manager.ApplyPulse(context.Background(), task); err == nil || !strings.Contains(err.Error(), "platform") {
					t.Fatalf("digest-correct wrong-platform artifact was not rejected: %v", err)
				}
				if commands != 0 {
					t.Fatalf("ran %d commands before platform rejection", commands)
				}
				for _, file := range paths {
					after, err := captureHostFile(manager.path(file))
					if err != nil || !reflect.DeepEqual(after, before[file]) {
						t.Fatalf("platform rejection mutated %s: %v", file, err)
					}
				}
			})
		}
	}
}
