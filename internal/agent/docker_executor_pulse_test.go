package agent

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/pulse"
)

func TestPulseDeployCopiesPrivateBootstrapBeforeStartAndRetainsData(t *testing.T) {
	const token = "test-only-pulse-setup-token-0000000000"
	for _, copyFails := range []bool{false, true} {
		name := "private bootstrap"
		if copyFails {
			name = "copy failure"
		}
		t.Run(name, func(t *testing.T) {
			var created, copied, started, removed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/_ping"):
					w.Header().Set("API-Version", "1.55")
				case strings.HasSuffix(r.URL.Path, "/images/create"):
					_, _ = io.WriteString(w, "{}\n")
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/volumes/vastora-pulse-data"):
					_ = json.NewEncoder(w).Encode(map[string]any{"Name": "vastora-pulse-data", "Labels": applicationResourceLabels(pulse.ServiceKey, applicationVolumeComponent("vastora-pulse-data"), "application", "")})
				case strings.HasSuffix(r.URL.Path, "/containers/"+pulseContainer+"/json"):
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"message":"not found"}`)
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/create"):
					payload, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if strings.Contains(string(payload), token) || strings.Contains(string(payload), "PULSE_OAUTH") {
						t.Error("secret or unsolicited OAuth configuration entered container metadata")
					}
					var config struct {
						container.Config
						HostConfig container.HostConfig
					}
					if json.Unmarshal(payload, &config) != nil || config.User != "65532:65532" {
						t.Error("wrong Pulse runtime user")
					}
					if len(config.Env) != 2 || config.Env[0] != "PULSE_PUBLIC_URL=https://pulse.example.com" || config.Env[1] != "PULSE_SETUP_TOKEN_FILE="+pulseSetupTokenPath {
						t.Error("missing authentication environment contract")
					}
					mounts := config.HostConfig.Mounts
					if len(mounts) != 1 || mounts[0].Type != mount.TypeVolume || mounts[0].Source != "vastora-pulse-data" || mounts[0].Target != "/var/lib/pulse" {
						t.Error("data volume changed or a new secret volume was introduced")
					}
					created.Store(true)
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"Id":"new-pulse-container","Warnings":[]}`)
				case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/containers/new-pulse-container/archive"):
					if !created.Load() || started.Load() || r.URL.Query().Get("path") != "/" || r.URL.Query().Get("copyUIDGID") != "" || r.URL.Query().Get("noOverwriteDirNonDir") != "true" {
						t.Error("unsafe copy target, ownership, or operation order")
					}
					reader := tar.NewReader(r.Body)
					parent, err := reader.Next()
					if err != nil || parent.Name != pulseBootstrapDirectory+"/" || parent.Typeflag != tar.TypeDir || parent.Mode != 0o711 || parent.Uid != 0 || parent.Gid != 0 {
						t.Error("bootstrap parent is not protected by root ownership")
					}
					file, err := reader.Next()
					if err != nil || file.Name != pulseBootstrapDirectory+"/setup-token" || file.Typeflag != tar.TypeReg || file.Mode != 0o400 || file.Uid != 65532 || file.Gid != 65532 {
						t.Error("setup token file is not private to the Pulse user")
					}
					contents, err := io.ReadAll(reader)
					if err != nil || string(contents) != token {
						t.Error("setup token contents changed")
					}
					if _, err := reader.Next(); err != io.EOF {
						t.Error("unexpected files in bootstrap archive")
					}
					copied.Store(true)
					if copyFails {
						w.WriteHeader(http.StatusInternalServerError)
						_ = json.NewEncoder(w).Encode(map[string]string{"message": "simulated error " + token})
					}
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/new-pulse-container/start"):
					if !copied.Load() || copyFails {
						t.Error("started without a successfully copied private token")
					}
					started.Store(true)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/new-pulse-container"):
					if !copyFails || r.URL.Query().Get("v") == "true" {
						t.Error("unsafe cleanup of container or volumes")
					}
					removed.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Docker operation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			docker, err := client.New(client.WithHost(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			task := DeploymentTask{ID: "deployment", ApplicationID: "application", AppKey: pulse.ServiceKey, Operation: "install", Config: json.RawMessage(`{"public_url":"https://pulse.example.com"}`), Secrets: json.RawMessage(`{"setup_token":"` + token + `"}`), Manifest: catalog.AppManifest{Images: []catalog.Image{{Name: "pulse", Reference: "ghcr.io/petauron/pulse:v0.1.0-alpha.2"}}}}
			err = deployPulse(context.Background(), docker, task, "127.0.0.1")
			if (err != nil) != copyFails || !created.Load() || !copied.Load() || started.Load() == copyFails || removed.Load() != copyFails {
				t.Fatalf("unexpected deploy lifecycle: err=%v created=%v copied=%v started=%v removed=%v", err, created.Load(), copied.Load(), started.Load(), removed.Load())
			}
			if err != nil && strings.Contains(err.Error(), token) {
				t.Fatal("Docker error leaked the private setup token")
			}
		})
	}
}

func TestPulseAuthenticationConfigRejectedBeforeDocker(t *testing.T) {
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := catalog.ParseCatalog(payload)
	if err != nil {
		t.Fatal(err)
	}
	var manifest catalog.AppManifest
	for _, app := range value.Apps {
		if app.ID == "pulse" {
			manifest = app
		}
	}
	_, err = (ApplicationExecutor{DockerSocket: "not-a-docker-socket"}).Deploy(context.Background(), DeploymentTask{ID: "deployment", ApplicationID: "application", AppKey: pulse.ServiceKey, Operation: "upgrade", Manifest: manifest, Config: json.RawMessage(`{"public_url":"https://pulse.example.com"}`), Secrets: json.RawMessage(`{}`)})
	if err == nil || !strings.Contains(err.Error(), "setup_token") {
		t.Fatalf("missing initialization secret reached Docker: %v", err)
	}
}
