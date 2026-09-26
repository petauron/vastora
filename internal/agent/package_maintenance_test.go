package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/client"
)

func packageBackupTar(t *testing.T, name, contents string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(contents))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func (e *fakePackageDocker) ContainerLogs(context.Context, string, client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	return io.NopCloser(strings.NewReader("ordinary log\n")), nil
}

func TestPackageDockerMaintenanceRestoresCopyAndRetainsPreviousVolume(t *testing.T) {
	task := packageTestTask(t)
	engine := newPackageDocker()
	directory := packageTestDirectory(t)
	backend := &DockerPackageBackend{Docker: engine, StateDirectory: directory}
	executor := PackageExecutor{StateDirectory: directory, Backend: backend}
	installed, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	previous := resourceNamed(installed.Resources, "volume", "data").Name
	engine.data[previous] = packageBackupTar(t, "data/database", "saved data")
	task.ID, task.Operation = "backup-task", "configure"
	task.PackageMaintenance = &PackageMaintenanceTask{Action: "backup"}
	backed, err := maintainPackage(context.Background(), executor, backend, task)
	if err != nil {
		t.Fatal(err)
	}
	if backed.PackageMaintenance.BackupID != task.ID || backed.Resources.State != "ready" {
		t.Fatal("backup not committed")
	}
	if len(backed.Resources.Backups) != 1 {
		t.Fatal("backup set missing")
	}
	task.ID = "logs-task"
	task.PackageMaintenance.Action = "logs"
	logged, err := maintainPackage(context.Background(), executor, backend, task)
	if err != nil || len(logged.PackageMaintenance.Logs) != 1 {
		t.Fatalf("logs: %+v %v", logged, err)
	}
	engine.data[previous] = packageBackupTar(t, "data/database", "later data")
	task.ID = "restore-task"
	task.PackageMaintenance = &PackageMaintenanceTask{Action: "restore", BackupID: "backup-task"}
	restored, err := maintainPackage(context.Background(), executor, backend, task)
	if err != nil {
		t.Fatal(err)
	}
	current := resourceNamed(restored.Resources, "volume", "data")
	if current.Name == previous || resourceNamed(restored.Resources, "retained-volume", "restore-predecessor:restore-task:data") == nil {
		t.Fatal("restore overwrote original data")
	}
	if !bytes.Contains(engine.data[previous], []byte("later data")) {
		t.Fatal("original volume was changed")
	}
	var stagedArchive []byte
	for _, created := range engine.created {
		if strings.HasSuffix(created.Name, "-copy") {
			for id, raw := range engine.archives {
				if id != resourceNamed(restored.Resources, "container", "app").ID {
					stagedArchive = raw
				}
			}
		}
	}
	if !bytes.Contains(stagedArchive, []byte("saved data")) {
		t.Fatal("restore did not copy verified snapshot")
	}
	if err := backend.Inspect(context.Background(), task, restored.Resources); err != nil {
		t.Fatal(err)
	}
}

func TestPackageMaintenanceRejectsTamperBeforeStoppingWriters(t *testing.T) {
	task := packageTestTask(t)
	engine := newPackageDocker()
	directory := packageTestDirectory(t)
	backend := &DockerPackageBackend{Docker: engine, StateDirectory: directory}
	executor := PackageExecutor{StateDirectory: directory, Backend: backend}
	installed, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	engine.data[resourceNamed(installed.Resources, "volume", "data").Name] = packageBackupTar(t, "data/database", "version one")
	task.ID, task.Operation = "snapshot", "configure"
	task.PackageMaintenance = &PackageMaintenanceTask{Action: "backup"}
	backed, err := maintainPackage(context.Background(), executor, backend, task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backed.Resources.Backups[0].Path, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	before := len(engine.calls)
	task.ID = "restore"
	task.PackageMaintenance = &PackageMaintenanceTask{Action: "restore", BackupID: "snapshot"}
	result, err := maintainPackage(context.Background(), executor, backend, task)
	if err == nil || result.Resources != nil || len(engine.calls) != before {
		t.Fatal("tampered backup caused mutation")
	}
}

func TestPackageBackupExtractionRejectsLinksAndTraversal(t *testing.T) {
	original := packageTestDirectory(t)
	info, _ := os.Stat(original)
	for _, name := range []string{"../escape", "/absolute", "a/../../escape"} {
		destination := filepath.Join(original, "restore")
		if err := extractBackupDirectory(packageBackupTar(t, name, "evil"), destination, info); err == nil {
			t.Fatalf("accepted %s", name)
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatal("unsafe archive created destination")
		}
	}
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	_ = writer.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_ = writer.Close()
	if validateBackupTar(raw.Bytes()) == nil {
		t.Fatal("accepted link")
	}
}

func TestPackageNoLegacyInstallerFallback(t *testing.T) {
	task := packageTestTask(t)
	task.Manifest.Runtime = nil
	task.Manifest.PackageRevision, task.PackageRevision = 0, 0
	task.ManifestSHA256 = ""
	backend := &fakePackageBackend{}
	_, err := (ApplicationExecutor{PackageBackend: backend, PackageStateDirectory: packageTestDirectory(t), Host: struct{}{}}).Deploy(context.Background(), task)
	if err == nil || len(backend.calls) != 0 {
		t.Fatal("legacy installation path was invoked")
	}
	encoded, _ := json.Marshal(task)
	if len(encoded) == 0 {
		t.Fatal("empty fixture")
	}
}
