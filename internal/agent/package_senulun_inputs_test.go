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
	"sync/atomic"
	"testing"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/senulun"
)

func TestManagedUpgradeSenulunEnrollmentIsOneTimeAndIdentityIsRequired(t *testing.T) {
	var recipe catalog.AppManifest
	for _, app := range officialContractFixture(t).Apps {
		if app.ID == "pulse-agent" {
			recipe = currentSenlunFixture(app)
		}
	}
	if recipe.Runtime == nil {
		t.Fatal("reviewed collector recipe is missing")
	}
	for _, scenario := range []string{"upgrade", "configure", "missing-identity", "invalid-identity", "wrong-service", "insecure-identity", "install-without-token", "install-with-token", "undeclared-config", "missing-service-association"} {
		t.Run(scenario, func(t *testing.T) {
			task := packageTestTask(t)
			task.AppKey, task.Manifest, task.Operation = senulun.AgentKey, recipe, "upgrade"
			settings := senulun.AgentConfig{ServiceURL: "https://monitor.example.test/", ServiceApplicationID: "monitor", NodeName: "retained-node", NodeGroup: "retained-group"}
			task.Config, _ = json.Marshal(settings)
			task.Secrets = json.RawMessage(`{}`)
			if scenario == "undeclared-config" || scenario == "missing-service-association" {
				var config map[string]json.RawMessage
				_ = json.Unmarshal(task.Config, &config)
				if scenario == "undeclared-config" {
					config["unknown_setting"] = json.RawMessage(`"must-not-be-discarded"`)
				} else {
					delete(config, "service_application_id")
				}
				task.Config, _ = json.Marshal(config)
			}
			binary := testArtifactELF(t, "amd64")
			digest := sha256.Sum256(binary)
			var downloads atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { downloads.Add(1); _, _ = w.Write(binary) }))
			defer server.Close()
			task.Manifest.Images = nil
			// Only replace the distribution artifact. Configuration, credentials,
			// environment and service confinement come from the reviewed recipe.
			task.Manifest.Artifacts = []catalog.Artifact{{Name: recipe.Runtime.Systemd.Artifact, OperatingSystem: "linux", Architecture: "amd64", URL: server.URL + "/collector", SHA256: hex.EncodeToString(digest[:]), Format: "raw"}}
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
			if err := validatePackageTask(task); scenario == "undeclared-config" {
				if err == nil {
					t.Fatal("preflight accepted undeclared collector configuration")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), HTTPClient: server.Client(), HostTarget: platform.Target{OS: "linux", Architecture: "amd64"}, RunCommand: func(_ context.Context, command string, _ ...string) error {
				if command == "getent" {
					return nil
				}
				t.Error("preparation mutated the service")
				return errors.New("unexpected command")
			}}
			if err := writeHostFileAtomic(manager.path("/etc/os-release"), []byte("ID=debian\nVERSION_ID=13\n"), 0644); err != nil {
				t.Fatal(err)
			}
			receipt := &InstanceResources{Resources: []RuntimeResource{{Kind: "directory", LogicalName: "state", Path: "/var/lib/retained-collector"}, {Kind: "file", LogicalName: "environment", Path: "/etc/retained-collector.env"}}}
			if task.Operation == "install" {
				receipt = &InstanceResources{}
			}
			if err := writeHostFileAtomic(manager.path("/etc/retained-collector.env"), []byte("PULSE_NODE_REGION=SG\nPULSE_GEOIP_PROVIDER=disabled\n"), 0600); err != nil {
				t.Fatal(err)
			}
			identityPath := manager.path(senulunPackageIdentityPath(task, receipt))
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
				prepared, err := backend.prepareSenulunInputs(task, receipt)
				var retained senulun.AgentConfig
				if err != nil || json.Unmarshal(prepared.Config, &retained) != nil || retained.NodeRegion == nil || *retained.NodeRegion != "SG" || retained.GeoIPProvider == nil || *retained.GeoIPProvider != "disabled" {
					t.Fatalf("retained location settings were lost: %v", err)
				}
				if retained.ServiceApplicationID != settings.ServiceApplicationID {
					t.Fatal("preparation lost the Center service association")
				}
				config, _, err := runtimeInputs(prepared)
				if err != nil {
					t.Fatalf("reviewed recipe rejected prepared collector inputs: %v", err)
				}
				if _, exists := config["service_application_id"]; exists {
					t.Fatal("Center metadata reached the runtime configuration")
				}
				var original senulun.AgentConfig
				if json.Unmarshal(task.Config, &original) != nil || original.ServiceApplicationID != settings.ServiceApplicationID {
					t.Fatal("preparation changed the original control-plane association")
				}
			}
			err := backend.Prepare(context.Background(), task, receipt)
			allowed := scenario == "upgrade" || scenario == "configure" || scenario == "install-with-token" || scenario == "missing-service-association"
			var expectedDownloads int32
			if allowed {
				expectedDownloads = 1
			}
			if (err == nil) != allowed || downloads.Load() != expectedDownloads {
				t.Fatalf("allowed=%v downloads=%d error=%v", allowed, downloads.Load(), err)
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
				if !bytes.Contains(backend.files["/etc/retained-collector.env"], []byte(`PULSE_NODE_REGION="SG"`)) || !bytes.Contains(backend.files["/etc/retained-collector.env"], []byte(`PULSE_GEOIP_PROVIDER="disabled"`)) {
					t.Fatal("staged environment lost location overrides")
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
	ordinary.Secrets = json.RawMessage(`{"enrollment_token":"fixture-required-secret"}`)
	ordinary.Config = json.RawMessage(`{"service_application_id":"monitor"}`)
	if _, _, err := runtimeInputs(ordinary); err == nil {
		t.Fatal("another application's undeclared Center metadata was accepted")
	}
	ordinary.AppKey = senulun.AgentKey
	if _, _, err := runtimeInputs(ordinary); err == nil {
		t.Fatal("a non-systemd package accepted collector-only metadata")
	}
}
