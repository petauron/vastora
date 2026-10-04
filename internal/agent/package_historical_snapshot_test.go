//go:build linux

package agent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/pulse"
	"github.com/petauron/vastora/internal/secret"
)

// Rehearse only Pulse historical adoption. Database/key inputs are private
// copies; Docker is exposed through a proxy that refuses every write request.
func TestPackageHistoricalPulseSnapshot(t *testing.T) {
	directory := os.Getenv("VASTORA_HISTORICAL_AGENT_SNAPSHOT")
	if directory == "" {
		t.Skip("requires an explicit private Agent snapshot")
	}
	key, err := secret.LoadKey(filepath.Join(directory, "agent.key"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(directory, "agent.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := &Store{db: db, key: key, dataDir: directory}
	ctx := context.Background()
	history, err := store.AppliedInstallation(ctx, pulse.ServiceKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(history.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	task := DeploymentTask{ID: "historical-copy-rehearsal", ApplicationID: history.ApplicationID, AppKey: pulse.ServiceKey, Operation: "adopt", Manifest: history.Manifest, HistoricalManifest: raw, ManifestSHA256: hex.EncodeToString(digest[:])}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}
	defer transport.CloseIdleConnections()
	target, _ := url.Parse("http://docker")
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && !(r.Method == http.MethodHead && strings.HasSuffix(r.URL.Path, "/_ping")) {
			http.Error(w, "read-only rehearsal", http.StatusForbidden)
			t.Error("Docker mutation refused")
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer server.Close()
	docker, err := client.New(client.WithHost("tcp://" + strings.TrimPrefix(server.URL, "http://")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	before, err := docker.ContainerInspect(ctx, pulseContainer, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	executor := ApplicationExecutor{Store: store, PackageStateDirectory: t.TempDir(), DockerSocket: "tcp://" + strings.TrimPrefix(server.URL, "http://")}
	result, err := executor.Adopt(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resources == nil || result.Resources.State != "ready" {
		t.Fatal("adoption receipt incomplete")
	}
	after, err := docker.ContainerInspect(ctx, pulseContainer, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if before.Container.State == nil || after.Container.State == nil {
		t.Fatal(errors.New("missing runtime observation"))
	}
	if before.Container.ID != after.Container.ID || before.Container.State.StartedAt != after.Container.State.StartedAt || !reflect.DeepEqual(before.Container.Mounts, after.Container.Mounts) || !reflect.DeepEqual(before.Container.Config, after.Container.Config) || !reflect.DeepEqual(before.Container.HostConfig, after.Container.HostConfig) {
		t.Fatal("historical runtime changed during adoption")
	}
	t.Log("historical Pulse adoption retained container identity, start time, mounts and configuration; receipt written only to temporary directory")
}
