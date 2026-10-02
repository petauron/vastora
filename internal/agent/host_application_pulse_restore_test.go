package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/pulse"
)

func TestPulseRestoreUsesStdinImporterBeforeStartingOriginalIdentity(t *testing.T) {
	for _, mode := range []string{"success", "import-failed", "wrong-node", "wrong-token", "missing-file", "insecure-file", "existing-identity", "unowned-state"} {
		t.Run(mode, func(t *testing.T) {
			archive := testPulseArchive(t, "pulse-v0.1.0-alpha.2-linux-x86_64/pulse-agent", tar.TypeReg, false)
			digest := sha256.Sum256(archive)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))
			defer server.Close()
			config := pulse.AgentConfig{ServiceURL: "https://pulse.private.example.com/", ServiceApplicationID: "monitor", NodeName: "saved-name", NodeGroup: "saved-group"}
			raw, _ := json.Marshal(config)
			credentials := pulse.RestoreCredentials{NodeID: "11111111-1111-4111-8111-111111111111", Token: "test-rotated-credential-never-publish"}
			task := DeploymentTask{ID: "restore", ApplicationID: "collector", AppKey: pulse.AgentKey, Operation: "install", Config: raw, Secrets: json.RawMessage(`{}`), PulseRestore: &credentials, Manifest: catalog.AppManifest{ID: "pulse-agent", Version: "0.1.0-alpha.2", Artifacts: []catalog.Artifact{{Name: "pulse-agent", OperatingSystem: "linux", Architecture: "amd64", URL: server.URL, SHA256: hex.EncodeToString(digest[:])}}}}
			manager := SystemdHostApplicationManager{RootDir: t.TempDir(), HTTPClient: server.Client(), HostTarget: platform.Target{OS: "linux", Architecture: "amd64"}}
			if err := writeHostFileAtomic(manager.path("/etc/os-release"), []byte("ID=debian\nVERSION_ID=12\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			privateFile := func(id, token string, mode os.FileMode) error {
				raw, _ := json.Marshal(map[string]any{"protocol_version": 2, "service_url": config.ServiceURL, "node_id": id, "agent_token": token})
				return writeHostFileAtomic(manager.path(pulseCredentialsPath), raw, mode)
			}
			if mode == "existing-identity" {
				if err := privateFile("other-node", "existing-credential", 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "unowned-state" {
				if err := writeHostFileAtomic(manager.path(pulseState+"/unmanaged"), []byte("retained"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			imported, restarted := false, false
			imports := 0
			manager.RunInputCommand = func(ctx context.Context, input io.Reader, name string, args ...string) error {
				imports++
				expected := []string{"-u", pulseUser, "--", "env", "-i", "PULSE_SERVICE_URL=" + config.ServiceURL, "PULSE_CREDENTIALS_PATH=" + manager.path(pulseCredentialsPath), manager.path(pulseBinary), "credentials", "import", credentials.NodeID}
				if name != "runuser" || !slices.Equal(args, expected) {
					t.Fatalf("unexpected importer: %s %v", name, args)
				}
				if strings.Contains(strings.Join(args, " "), credentials.Token) {
					t.Fatal("credential in process arguments")
				}
				raw, err := io.ReadAll(input)
				if err != nil || string(raw) != credentials.Token+"\n" {
					t.Fatal("credential not sent over stdin")
				}
				token, err := os.ReadFile(manager.path(pulseToken))
				if err != nil || len(token) != 0 {
					t.Fatal("collector could enroll a new identity")
				}
				if mode == "import-failed" {
					return errors.New("private output " + credentials.Token)
				}
				if mode == "missing-file" {
					return nil
				}
				node, tokenValue, fileMode := credentials.NodeID, credentials.Token, os.FileMode(0o600)
				if mode == "wrong-node" {
					node = "other-node"
				}
				if mode == "wrong-token" {
					tokenValue = "other-token"
				}
				if mode == "insecure-file" {
					fileMode = 0o644
				}
				imported = true
				return privateFile(node, tokenValue, fileMode)
			}
			manager.RunCommand = func(ctx context.Context, name string, args ...string) error {
				if name == "systemctl" && len(args) > 0 && args[0] == "restart" {
					if !imported {
						t.Fatal("service started before original credentials were imported")
					}
					restarted = true
				}
				return nil
			}
			result, err := manager.ApplyPulse(context.Background(), task)
			if mode == "success" {
				if err != nil || result.PulseRestored == nil || result.PulseRestored.NodeID != credentials.NodeID || !restarted || imports != 1 {
					t.Fatalf("restoration incomplete: %v", err)
				}
				info, err := os.Stat(manager.path(pulseState))
				if err != nil || info.Mode().Perm() != 0o700 {
					t.Fatal("state directory not private")
				}
				env, err := os.ReadFile(manager.path(pulseEnv))
				if err != nil || bytes.Contains(env, []byte(credentials.Token)) || !bytes.Contains(env, []byte("saved-name")) {
					t.Fatal("saved config or secret handling invalid")
				}
				resultJSON, _ := json.Marshal(result)
				if bytes.Contains(resultJSON, []byte(credentials.Token)) {
					t.Fatal("credential in completion result")
				}
			} else {
				if err == nil || restarted || result.PulseRestored != nil {
					t.Fatal("invalid import started collector")
				}
				if strings.Contains(err.Error(), credentials.Token) {
					t.Fatal("credential leaked through task error")
				}
				if (mode == "existing-identity" || mode == "unowned-state") && imports != 0 {
					t.Fatal("existing identity overwritten")
				}
				if mode != "existing-identity" && mode != "unowned-state" && !taskOutcomeIsUncertain(err) {
					t.Fatal("partial installation not retained for review")
				}
			}
		})
	}
}
