//go:build integration && linux

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
	"os/exec"
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
	for _, kind := range []string{"docker", "systemd", "native"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
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
				// A container-local fixture with a TERM handler avoids making every
				// cold backup wait for Docker's PID-1 stop timeout. No host shell runs.
				arguments := []catalog.Value{hostFixtureValue("sh"), hostFixtureValue("-c"), hostFixtureValue("trap 'exit 0' TERM; while :; do sleep 1 & wait $!; done")}
				task.Manifest.Runtime.Docker.Containers[0].Arguments = arguments
				// Exercise real private-network creation, dependency order and aliases.
				task.Manifest.Runtime.Docker.Containers = append(task.Manifest.Runtime.Docker.Containers, catalog.Container{Name: "sidecar", Image: "app", Arguments: arguments, DependsOn: []string{"app"}})
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
				if kind == "native" {
					worker := *task.Manifest.Runtime.Systemd
					controller := worker
					controller.User, controller.StateDirectories = "root", nil
					task.Manifest.Runtime.Version = 2
					task.Manifest.Runtime.Systemd = nil
					task.Manifest.Runtime.RequiredCapabilities = []string{"root", "native-services"}
					task.AuthorizedCapabilities = []string{"root", "native-services"}
					task.Manifest.Runtime.NativeServices = &catalog.NativeServicesRuntime{Users: []string{"worker"}, Services: []catalog.NativeService{
						{Name: "controller", Start: "automatic", Process: controller},
						{Name: "worker", Start: "manual", ManagedUser: "worker", Process: worker},
					}}
					backend = &NativeServicesPackageBackend{Manager: SystemdHostApplicationManager{HTTPClient: server.Client()}, StateDirectory: directory}
				}
			}
			packageTaskDigest(t, &task)
			executor := PackageExecutor{StateDirectory: directory, Backend: backend}
			installed, err := executor.Deploy(ctx, task)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "native" {
				worker := resourceNamed(installed.Resources, "unit", "worker")
				if worker == nil || worker.MainPID != 0 {
					t.Fatal("manual native worker was started during installation")
				}
				if err := exec.CommandContext(ctx, "systemctl", "is-enabled", "--quiet", worker.Name).Run(); err == nil {
					t.Fatal("manual worker unexpectedly enabled")
				}
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
				if kind != "docker" {
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
			if len(installed.Resources.Backups) != 0 {
				t.Fatal("upgrade unexpectedly created a backup")
			}
			if kind == "native" {
				worker := resourceNamed(installed.Resources, "unit", "worker")
				if output, err := exec.CommandContext(ctx, "systemctl", "start", worker.Name).CombinedOutput(); err != nil {
					t.Fatalf("activate app-owned worker: %v: %s", err, output)
				}
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
			if kind != "docker" {
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
			if kind == "native" {
				worker := resourceNamed(restored.Resources, "unit", "worker")
				if worker.MainPID <= 0 || worker.InvocationID == "" {
					t.Fatal("restore did not resume previously active native worker")
				}
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
			if kind == "native" {
				verifyNativeRetainedReinstall(t, ctx, executor, task, removed.Resources)
			}
			t.Logf("%s/%s: real install, identity inspection/adoption, upgrade, backup, restore, logs and retained uninstall passed", kind, runtime.GOARCH)
		})
	}
}

func verifyNativeRetainedReinstall(t *testing.T, ctx context.Context, executor PackageExecutor, task DeploymentTask, retained *InstanceResources) {
	t.Helper()
	account := *resourceNamed(retained, "user", "worker")
	data := *resourceNamed(retained, "directory", "data")
	configuration := *resourceNamed(retained, "file", "controller:environment")
	custom := []byte("APPLICATION_SETTING=preserved\n")
	if err := os.WriteFile(configuration.Path, custom, 0600); err != nil {
		t.Fatal(err)
	}
	task.ID, task.Operation = "reinstall-retained", "install"
	reinstalled, err := executor.Deploy(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if got := resourceNamed(reinstalled.Resources, "user", "worker"); got == nil || got.ID != account.ID || got.Name != account.Name {
		t.Fatal("reinstallation changed retained account identity")
	}
	for _, check := range []struct{ path, content string }{
		{configuration.Path, string(custom)},
		{filepath.Join(data.Path, "database"), "before-upgrade"},
	} {
		raw, err := os.ReadFile(check.path)
		if err != nil || string(raw) != check.content {
			t.Fatal("retained configuration or data was reinitialized", err)
		}
	}
	// Deliberately collide with an immutable snapshot after both services have
	// started. Backup must fail without overwriting it, then resume the exact set.
	worker := resourceNamed(reinstalled.Resources, "unit", "worker")
	if output, err := exec.CommandContext(ctx, "systemctl", "start", worker.Name).CombinedOutput(); err != nil {
		t.Fatalf("activate worker before backup failure: %v: %s", err, output)
	}
	failedBackup := task
	failedBackup.ID = "snapshot"
	native := executor.Backend.(*NativeServicesPackageBackend)
	if err := native.Backup(ctx, failedBackup, reinstalled.Resources); err == nil {
		t.Fatal("immutable snapshot was overwritten")
	}
	if err := native.Healthy(ctx, task, reinstalled.Resources); err != nil {
		t.Fatal("backup failure did not restore healthy service state", err)
	}
	for _, resource := range reinstalled.Resources.Resources {
		if resource.Kind == "unit" && (resource.MainPID <= 0 || resource.InvocationID == "") {
			t.Fatal("backup failure left a previously active service stopped")
		}
	}
	resources := append([]RuntimeResource(nil), reinstalled.Resources.Resources...)
	task.ID, task.Operation, task.DeleteData = "delete-native-data", "uninstall", true
	removed, err := executor.Deploy(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Resources.State != "removed" || len(removed.Resources.Resources) != 0 {
		t.Fatal("explicit uninstall retained active resource receipts")
	}
	for _, resource := range resources {
		if resource.Path != "" {
			if _, err := os.Lstat(resource.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("owned resource remains: %s: %v", resource.Kind, err)
			}
		}
	}
	for _, database := range []string{"passwd", "group"} {
		output, err := exec.CommandContext(ctx, "getent", database, account.Name).Output()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 2 || len(output) != 0 {
			t.Fatal("owned account or private group still exists", database, err)
		}
	}
	// Explicit application deletion does not destroy immutable recovery backups.
	if len(removed.Resources.Backups) == 0 {
		t.Fatal("recovery backups lost")
	}
	for _, backup := range removed.Resources.Backups {
		file, err := openNativeBackup(executor.StateDirectory, task, backup)
		if err != nil {
			t.Fatal(err)
		}
		file.Close()
	}
	t.Log("native retained reinstall preserved account/config/data; failed backup resumed both services; explicit deletion removed owned resources and retained verified backups")
}
