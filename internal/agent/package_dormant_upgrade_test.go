package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/moby/moby/client"
)

func TestManagedUpgradeDormantMeridianStagesImageWithoutRuntimeActivation(t *testing.T) {
	for _, scenario := range []string{"upgrade", "configure", "state", "directory", "container", "receipt-resource", "during-pull"} {
		t.Run(scenario, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			task := packageTestTask(t)
			task.AppKey, task.Manifest.ID, task.Operation = meridianKey, "meridian", "upgrade"
			task.Manifest.Images[0].Name, task.Manifest.Images[0].Reference = "xray-core", xrayWorkerImageReference
			task.Manifest.Runtime.Docker.Containers[0].Image = "xray-core"
			packageTaskDigest(t, &task)
			receipt := historicalReceipt(task, "docker")
			receipt.PackageVersion, receipt.IntegrationState = "0.0.1", "dormant"
			if scenario == "configure" {
				task.Operation = "configure"
			}
			if scenario == "receipt-resource" {
				receipt.Resources = []RuntimeResource{{Kind: "image", LogicalName: "unowned"}}
			}
			if scenario == "state" {
				if _, err := store.db.Exec(`INSERT INTO meridian_runtime_state(id,sealed_state) VALUES(1,?)`, []byte("unexpected-state")); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "directory" {
				if err := os.Mkdir(filepath.Join(store.dataDir, meridianRuntimeDirectory), 0700); err != nil {
					t.Fatal(err)
				}
			}
			var pulls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("API-Version", "1.55")
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
					if scenario == "container" && strings.Contains(r.URL.Path, meridianXrayContainer+"/json") {
						_, _ = w.Write([]byte(`{"Id":"unexpected-container","Config":{}}`))
					} else {
						w.WriteHeader(http.StatusNotFound)
						_, _ = w.Write([]byte(`{"message":"not found"}`))
					}
				case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/images/create"):
					pulls.Add(1)
					if scenario == "during-pull" {
						if err := os.Mkdir(filepath.Join(store.dataDir, meridianRuntimeDirectory), 0700); err != nil {
							t.Error(err)
						}
					}
					_, _ = w.Write([]byte("{\"status\":\"download verified\"}\n"))
				default:
					t.Errorf("unexpected runtime operation: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusForbidden)
				}
			}))
			defer server.Close()
			docker, err := client.New(client.WithHost(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			defer docker.Close()
			directory := packageTestDirectory(t)
			backend := &meridianPackageBackend{DockerPackageBackend: &DockerPackageBackend{Docker: docker, StateDirectory: directory}, Executor: ApplicationExecutor{Store: store}, Client: docker}
			executor := PackageExecutor{StateDirectory: directory, Backend: backend}
			if err := executor.save(receipt); err != nil {
				t.Fatal(err)
			}
			result, err := executor.Deploy(context.Background(), task)
			if scenario == "upgrade" {
				if err != nil || result.Resources.IntegrationState != "awaiting-configuration" || result.Resources.State != "ready" || result.Resources.PackageVersion != task.Manifest.Version || len(result.Resources.Resources) != 1 || result.Resources.Resources[0].Kind != "image" || result.Resources.Resources[0].Name != xrayWorkerImageReference || pulls.Load() != 1 {
					t.Fatalf("dormant upgrade failed or activated runtime: result=%+v error=%v pulls=%d", result.Resources, err, pulls.Load())
				}
			} else {
				if err == nil {
					t.Fatal("conflicting dormant upgrade was accepted")
				}
				retained, readErr := executor.ReadResources(task.ApplicationID)
				if readErr != nil || retained.PackageVersion != "0.0.1" {
					t.Fatal("failure changed installed version")
				}
				if scenario == "during-pull" {
					if !taskOutcomeIsUncertain(err) || retained.State != "review-required" || pulls.Load() != 1 {
						t.Fatal("mid-upgrade conflict lost recovery evidence")
					}
				} else if pulls.Load() != 0 {
					t.Fatal("conflict was detected after staging")
				}
			}
			var rows int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM meridian_runtime_state`).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if scenario != "state" && rows != 0 {
				t.Fatal("upgrade created runtime state")
			}
		})
	}
}
