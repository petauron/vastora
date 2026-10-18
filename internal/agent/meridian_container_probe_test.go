package agent

import (
	"context"
	"encoding/json"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/meridianruntime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMeridianContainerProbeIsolation(t *testing.T) {
	clientOptions := meridianAcceptanceOptions("client", "isolated", true)
	if len(clientOptions.HostConfig.PortBindings) != 0 || len(clientOptions.Config.ExposedPorts) != 0 || clientOptions.HostConfig.NetworkMode != "isolated" {
		t.Fatal("container probe client published host sockets")
	}
	options := meridianContainerProbeOptions("probe", "immutable-client", "vastora@sha256:abc", "1.1.1.1")
	h, c := options.HostConfig, options.Config
	if h.NetworkMode != "container:immutable-client" || len(h.PortBindings) != 0 || len(h.Binds) != 0 || len(h.Mounts) != 0 || len(c.ExposedPorts) != 0 || len(c.Env) != 0 || c.User != "65532:65532" {
		t.Fatal("probe must share only the temporary client network, without host sockets or mounts")
	}
	if h.Tmpfs["/var/lib/vastora"] == "" || !h.ReadonlyRootfs || h.LogConfig.Type != "none" || len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL" || *h.PidsLimit != 32 || h.Memory != 128<<20 {
		t.Fatal("probe bounds lost")
	}
	if strings.Join(c.Cmd, " ") != "deployer meridian-entry-probe --expected-exit 1.1.1.1" {
		t.Fatal("wrong helper command")
	}
}

func TestMeridianContainerProbeRequiresImmutableRuntimeAndPublicExit(t *testing.T) {
	for _, tc := range []struct{ image, exit string }{{"latest", "1.1.1.1"}, {"", "1.1.1.1"}, {"vastora@sha256:" + strings.Repeat("a", 64), "127.0.0.1"}} {
		if err := (ApplicationExecutor{}).VerifyMeridianClientInContainer(context.Background(), meridianruntime.AcceptanceClient{}, tc.exit, tc.image); err == nil {
			t.Fatal("accepted invalid probe")
		}
	}
}

func TestMeridianContainerProbeWaitAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "probe-failed", "start-failed", "wait-failed", "cleanup-failed"} {
		t.Run(mode, func(t *testing.T) {
			removed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("API-Version", "1.55")
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					var c map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
						t.Error(err)
					}
					var h container.HostConfig
					if err := json.Unmarshal(c["HostConfig"], &h); err != nil {
						t.Error(err)
					}
					if h.NetworkMode != "container:client-id" {
						t.Error("wrong network namespace")
					}
					w.WriteHeader(201)
					_, _ = w.Write([]byte(`{"Id":"helper-id"}`))
				case strings.HasSuffix(r.URL.Path, "/containers/helper-id/start"):
					if mode == "start-failed" {
						w.WriteHeader(500)
						_, _ = w.Write([]byte(`{"message":"failed"}`))
					} else {
						w.WriteHeader(204)
					}
				case strings.HasSuffix(r.URL.Path, "/containers/helper-id/wait"):
					if mode == "wait-failed" {
						w.WriteHeader(500)
						_, _ = w.Write([]byte(`{"message":"failed"}`))
						return
					}
					code := 0
					if mode == "probe-failed" {
						code = 1
					}
					_ = json.NewEncoder(w).Encode(map[string]int{"StatusCode": code})
				case strings.HasSuffix(r.URL.Path, "/containers/vastora-entry-probe-client-id/json"):
					_ = json.NewEncoder(w).Encode(container.InspectResponse{ID: "helper-id", Config: &container.Config{Labels: map[string]string{acceptanceOwnerLabel: "vastora-entry-probe-client-id"}}})
				case strings.HasSuffix(r.URL.Path, "/containers/helper-id") && r.Method == http.MethodDelete:
					removed = true
					if mode == "cleanup-failed" {
						w.WriteHeader(500)
						_, _ = w.Write([]byte(`{"message":"failed"}`))
					} else {
						w.WriteHeader(204)
					}
				case strings.HasSuffix(r.URL.Path, "/networks/vastora-entry-probe-client-id"):
					w.WriteHeader(404)
					_, _ = w.Write([]byte(`{"message":"missing"}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			docker, err := client.New(client.WithHost(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			err = runMeridianContainerProbe(context.Background(), docker, "client-id", "vastora@sha256:abc", "1.1.1.1")
			if (err == nil) != (mode == "success") || !removed {
				t.Fatalf("result=%v removed=%v", err, removed)
			}
		})
	}
}
