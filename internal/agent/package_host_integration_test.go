//go:build integration && linux

package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
)

func hostFixtureValue(value string) catalog.Value {
	raw, _ := json.Marshal(value)
	literal := json.RawMessage(raw)
	return catalog.Value{Literal: &literal}
}

// Host mutations are restricted to explicitly opted-in disposable hosted runners.
// Never use FromEnv: a developer's Docker context may refer to a live host.
func packageHostedRunner(t *testing.T) {
	t.Helper()
	if os.Getenv("VASTORA_PACKAGE_HOST_INTEGRATION") != "1" {
		t.Skip("requires an explicitly disposable hosted runner")
	}
	if os.Getenv("GITHUB_ACTIONS") != "true" || os.Getenv("RUNNER_ENVIRONMENT") != "github-hosted" || os.Geteuid() != 0 {
		t.Fatal("host integration requires root on a GitHub-hosted runner")
	}
}

func TestPackageRealHostLifecycle(t *testing.T) {
	packageHostedRunner(t)
	for _, kind := range []string{"docker", "systemd"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			task := packageTestTask(t)
			task.ApplicationID = "ci-" + kind + "-" + filepath.Base(t.TempDir())
			directory := packageTestDirectory(t)
			var backend packageMaintenanceBackend
			var docker *client.Client
			if kind == "docker" {
				image := os.Getenv("VASTORA_PACKAGE_TEST_IMAGE")
				if !strings.HasPrefix(image, "busybox@sha256:") {
					t.Fatal("CI must supply the resolved public busybox digest")
				}
				task.Manifest.Images[0].Reference = image
				task.Manifest.Runtime.Docker.Containers[0].Arguments = []catalog.Value{hostFixtureValue("sleep"), hostFixtureValue("300")}
				// Exercise real private-network creation, dependency order and aliases.
				task.Manifest.Runtime.Docker.Containers = append(task.Manifest.Runtime.Docker.Containers, catalog.Container{Name: "sidecar", Image: "app", Arguments: []catalog.Value{hostFixtureValue("sleep"), hostFixtureValue("300")}, DependsOn: []string{"app"}})
				var err error
				docker, err = client.New(client.WithHost("unix:///var/run/docker.sock"))
				if err != nil {
					t.Fatal(err)
				}
				defer docker.Close()
				backend = &DockerPackageBackend{Docker: docker, StateDirectory: directory}
			} else {
				binary, err := os.ReadFile("/usr/bin/sleep")
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(binary)
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(binary) }))
				defer server.Close()
				task.Manifest.Images = nil
				task.Manifest.Artifacts = []catalog.Artifact{{Name: "app", OperatingSystem: "linux", Architecture: runtime.GOARCH, URL: server.URL + "/sleep", SHA256: hex.EncodeToString(digest[:]), Format: "raw"}}
				task.Manifest.Runtime = &catalog.RuntimeSpec{Kind: "systemd", Version: 1, Storage: []catalog.Storage{{Name: "data", Persistent: true}}, Systemd: &catalog.SystemdRuntime{Artifact: "app", Executable: "sleep", Files: []catalog.ArtifactFile{{Path: "sleep", Executable: true}}, Arguments: []catalog.Value{hostFixtureValue("300")}, StateDirectories: []string{"data"}}}
				backend = &SystemdPackageBackend{Manager: SystemdHostApplicationManager{HTTPClient: server.Client()}, StateDirectory: directory}
			}
			packageTaskDigest(t, &task)
			executor := PackageExecutor{StateDirectory: directory, Backend: backend}
			installed, err := executor.Deploy(ctx, task)
			if err != nil {
				t.Fatal(err)
			}
			// Compare real runtime identities across metadata-only adoption.
			before, err := executor.ReadResources(task.ApplicationID)
			if err != nil {
				t.Fatal(err)
			}
			adopt := task
			adopt.ID, adopt.Operation, adopt.Resources = "adopt-copy", "adopt", before
			copyExecutor := PackageExecutor{StateDirectory: packageTestDirectory(t), Backend: backend}
			adopted, err := copyExecutor.Deploy(ctx, adopt)
			if err != nil {
				t.Fatal(err)
			}
			if err := backend.Inspect(ctx, adopt, adopted.Resources); err != nil {
				t.Fatal(err)
			}
			writeData := func(value string) {
				t.Helper()
				if kind == "systemd" {
					p := resourceNamed(installed.Resources, "directory", "data").Path
					if err := os.WriteFile(filepath.Join(p, "database"), []byte(value), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					id := resourceNamed(installed.Resources, "container", "app").ID
					_, err := docker.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: "/data", Content: bytes.NewReader(packageBackupTar(t, "database", value))})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			writeData("before-upgrade")
			task.ID, task.Operation, task.Manifest.Version = "upgrade", "upgrade", "1.1.0"
			packageTaskDigest(t, &task)
			installed, err = executor.Deploy(ctx, task)
			if err != nil {
				t.Fatal(err)
			}
			if len(installed.Resources.Backups) == 0 {
				t.Fatal("upgrade did not preserve a backup")
			}
			task.ID, task.PackageMaintenance = "snapshot", &PackageMaintenanceTask{Action: "backup"}
			backed, err := maintainPackage(ctx, executor, backend, task)
			if err != nil {
				t.Fatal(err)
			}
			if backed.PackageMaintenance.BackupID != "snapshot" {
				t.Fatal("missing backup identity")
			}
			writeData("after-backup")
			task.ID, task.PackageMaintenance = "restore", &PackageMaintenanceTask{Action: "restore", BackupID: "snapshot"}
			restored, err := maintainPackage(ctx, executor, backend, task)
			if err != nil {
				t.Fatal(err)
			}
			var restoredData []byte
			if kind == "systemd" {
				restoredData, err = os.ReadFile(filepath.Join(resourceNamed(restored.Resources, "directory", "data").Path, "database"))
			} else {
				var copied client.CopyFromContainerResult
				copied, err = docker.CopyFromContainer(ctx, resourceNamed(restored.Resources, "container", "app").ID, client.CopyFromContainerOptions{SourcePath: "/data/database"})
				if err == nil {
					reader := tar.NewReader(copied.Content)
					_, err = reader.Next()
					if err == nil {
						restoredData, err = io.ReadAll(io.LimitReader(reader, 1024))
					}
					copied.Content.Close()
				}
			}
			if err != nil || string(restoredData) != "before-upgrade" {
				t.Fatalf("restore did not recover actual data: %v", err)
			}
			task.ID, task.PackageMaintenance = "logs", &PackageMaintenanceTask{Action: "logs"}
			if _, err := maintainPackage(ctx, executor, backend, task); err != nil {
				t.Fatal(err)
			}
			task.ID, task.Operation, task.PackageMaintenance = "uninstall", "uninstall", nil
			removed, err := executor.Deploy(ctx, task)
			if err != nil {
				t.Fatal(err)
			}
			if removed.Resources.State != "retained" {
				t.Fatal("default uninstall discarded persistent data")
			}
			if err := backend.Inspect(ctx, task, removed.Resources); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s/%s: real install, identity inspection/adoption, upgrade, backup, restore, logs and retained uninstall passed", kind, runtime.GOARCH)
		})
	}
}
