package agent

import (
	"bytes"
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

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/pulse"
)

func TestManagedUpgradePulseEnrollmentIsOneTimeAndIdentityIsRequired(t *testing.T) {
	for _, scenario := range []string{"upgrade", "configure", "missing-identity", "invalid-identity", "wrong-service", "insecure-identity", "install-without-token", "install-with-token"} {
		t.Run(scenario, func(t *testing.T) {
			task := packageTestTask(t)
			task.AppKey, task.Manifest.ID, task.Operation = pulse.AgentKey, "pulse-agent", "upgrade"
			settings := pulse.AgentConfig{ServiceURL: "https://monitor.example.test/", ServiceApplicationID: "monitor", NodeName: "retained-node", NodeGroup: "retained-group"}
			task.Config, _ = json.Marshal(settings)
			task.Secrets = json.RawMessage(`{}`)
			task.Manifest.Config = []catalog.ConfigField{{Key: "service_url", Type: "string", Required: true}, {Key: "service_application_id", Type: "string", Required: true}, {Key: "node_name", Type: "string", Required: true}, {Key: "node_group", Type: "string"}, {Key: "node_region", Type: "string"}, {Key: "geoip_provider", Type: "string"}, {Key: "enrollment_token", Type: "string", Secret: true, Required: true}}
			binary := testArtifactELF(t, "amd64")
			digest := sha256.Sum256(binary)
			downloads := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { downloads++; _, _ = w.Write(binary) }))
			defer server.Close()
			task.Manifest.Images = nil
			task.Manifest.Artifacts = []catalog.Artifact{{Name: "app", OperatingSystem: "linux", Architecture: "amd64", URL: server.URL + "/collector", SHA256: hex.EncodeToString(digest[:]), Format: "raw"}}
			task.Manifest.Runtime = &catalog.RuntimeSpec{Kind: "systemd", Version: 1, Storage: []catalog.Storage{{Name: "state", Persistent: true}}, Systemd: &catalog.SystemdRuntime{Artifact: "app", Executable: "app", Files: []catalog.ArtifactFile{{Path: "app", Executable: true}}, StateDirectories: []string{"state"}, Credentials: map[string]string{"enrollment-token": "enrollment_token"}}}
			if scenario == "configure" {
				task.Operation = "configure"
			}
			if strings.HasPrefix(scenario, "install-") {
				task.Operation = "install"
			}
			if scenario == "install-with-token" {
				task.Secrets = json.RawMessage(`{"enrollment_token":"fixture-one-time-enrollment-token"}`)
			}
			packageTaskDigest(t, &task)
			if err := validatePackageTask(task); err != nil {
				t.Fatal(err)
			}
			manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), HTTPClient: server.Client(), HostTarget: platform.Target{OS: "linux", Architecture: "amd64"}, RunCommand: func(context.Context, string, ...string) error {
				t.Error("preparation mutated the service")
				return errors.New("unexpected command")
			}}
			if err := writeHostFileAtomic(manager.path("/etc/os-release"), []byte("ID=debian\nVERSION_ID=13\n"), 0644); err != nil {
				t.Fatal(err)
			}
			receipt := &InstanceResources{Resources: []RuntimeResource{{Kind: "directory", LogicalName: "state", Path: "/var/lib/retained-collector"}, {Kind: "file", LogicalName: "environment", Path: "/etc/retained-collector.env"}}}
			if err := writeHostFileAtomic(manager.path("/etc/retained-collector.env"), []byte("PULSE_NODE_REGION=SG\nPULSE_GEOIP_PROVIDER=disabled\n"), 0600); err != nil {
				t.Fatal(err)
			}
			identityPath := manager.path(pulsePackageIdentityPath(task, receipt))
			identity := []byte(`{"protocol_version":2,"service_url":"https://monitor.example.test/","node_id":"fixture-node","agent_token":"fixture-retained-token"}`)
			mode := os.FileMode(0600)
			switch scenario {
			case "invalid-identity":
				identity = []byte(`{invalid`)
			case "wrong-service":
				identity = bytes.ReplaceAll(identity, []byte("monitor.example.test"), []byte("other.example.test"))
			case "insecure-identity":
				mode = 0644
			}
			if scenario != "missing-identity" && !strings.HasPrefix(scenario, "install-") {
				if err := writeHostFileAtomic(identityPath, identity, mode); err != nil {
					t.Fatal(err)
				}
			}
			backend := &SystemdPackageBackend{Manager: manager, StateDirectory: packageTestDirectory(t)}
			if scenario == "upgrade" || scenario == "configure" {
				prepared, err := backend.preparePulseInputs(task, receipt)
				var retained pulse.AgentConfig
				if err != nil || json.Unmarshal(prepared.Config, &retained) != nil || retained.NodeRegion == nil || *retained.NodeRegion != "SG" || retained.GeoIPProvider == nil || *retained.GeoIPProvider != "disabled" {
					t.Fatalf("retained location settings were lost: %v", err)
				}
			}
			err := backend.Prepare(context.Background(), task, receipt)
			allowed := scenario == "upgrade" || scenario == "configure" || scenario == "install-with-token"
			if (err == nil) != allowed || downloads != map[bool]int{true: 1, false: 0}[allowed] {
				t.Fatalf("allowed=%v downloads=%d error=%v", allowed, downloads, err)
			}
			if scenario == "upgrade" || scenario == "configure" {
				foundCredential := false
				for path, raw := range backend.files {
					if backend.logicalNames[path] == "credential:enrollment-token" {
						foundCredential = true
						if len(raw) != 0 {
							t.Fatal("upgrade replayed enrollment")
						}
					}
				}
				if !foundCredential {
					t.Fatal("retained enrollment source was not staged")
				}
				retained, readErr := os.ReadFile(identityPath)
				if readErr != nil || !bytes.Equal(retained, identity) {
					t.Fatal("preparation changed the monitoring identity")
				}
			}
		})
	}
	ordinary := packageTestTask(t)
	ordinary.Manifest.Config = []catalog.ConfigField{{Key: "enrollment_token", Type: "string", Secret: true, Required: true}}
	if _, _, err := runtimeInputs(ordinary); err == nil {
		t.Fatal("another application's required secret was relaxed")
	}
}
