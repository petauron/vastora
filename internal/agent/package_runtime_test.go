package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/platform"
)

func packageTestDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func packageTestTask(t *testing.T) DeploymentTask {
	t.Helper()
	text := catalog.LocalizedText{English: "Unregistered app", SimplifiedChinese: "新应用"}
	app := catalog.AppManifest{ID: "not-in-vastora-source", Version: "1.0.0", PackageRevision: 1, Name: text, Description: text, License: "MIT", Config: []catalog.ConfigField{}, Images: []catalog.Image{{Name: "app", Reference: "ghcr.io/example/plain@sha256:" + strings.Repeat("a", 64)}}, Runtime: &catalog.RuntimeSpec{Kind: "docker", Version: 1, Storage: []catalog.Storage{{Name: "data", Persistent: true}}, Docker: &catalog.DockerRuntime{Containers: []catalog.Container{{Name: "app", Image: "app", Mounts: []catalog.Mount{{Storage: "data", Target: "/data"}}}}}}}
	task := DeploymentTask{ID: "first-task", AppKey: "test-source/" + app.ID, ApplicationID: "standalone-instance", Operation: "install", Manifest: app, PackageRevision: 1, Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`)}
	packageTaskDigest(t, &task)
	return task
}
func packageTaskDigest(t *testing.T, task *DeploymentTask) {
	t.Helper()
	app, err := catalog.CanonicalAppManifest(task.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	task.Manifest = app
	raw, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	task.ManifestSHA256 = hex.EncodeToString(sum[:])
	task.PackageRevision = app.PackageRevision
}

type fakePackageBackend struct {
	calls []string
	fail  string
}

func (b *fakePackageBackend) step(name string) error {
	b.calls = append(b.calls, name)
	if b.fail == name {
		return errors.New("injected failure")
	}
	return nil
}
func (b *fakePackageBackend) Prepare(context.Context, DeploymentTask, *InstanceResources) error {
	return b.step("prepare")
}
func (b *fakePackageBackend) Inspect(context.Context, DeploymentTask, *InstanceResources) error {
	return b.step("inspect")
}
func (b *fakePackageBackend) Backup(context.Context, DeploymentTask, *InstanceResources) error {
	return b.step("backup")
}
func (b *fakePackageBackend) Apply(_ context.Context, _ DeploymentTask, r *InstanceResources, persist func() error) error {
	r.Resources = []RuntimeResource{{Kind: "container", LogicalName: "app", Name: "old-name", ID: "runtime-identity"}}
	if err := persist(); err != nil {
		return err
	}
	return b.step("apply")
}
func (b *fakePackageBackend) Healthy(context.Context, DeploymentTask, *InstanceResources) error {
	return b.step("healthy")
}
func (b *fakePackageBackend) Remove(context.Context, DeploymentTask, *InstanceResources) error {
	return b.step("remove")
}

func TestPackageLifecycleUnknownApplicationAndNoAutomaticRollback(t *testing.T) {
	task := packageTestTask(t)
	backend := &fakePackageBackend{}
	executor := PackageExecutor{StateDirectory: packageTestDirectory(t), Backend: backend}
	result, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resources.State != "ready" || !slices.Equal(backend.calls, []string{"prepare", "apply", "healthy"}) {
		t.Fatalf("unexpected lifecycle: %v", backend.calls)
	}
	backend.calls = nil
	backend.fail = "healthy"
	task.ID = "upgrade-task"
	task.Operation = "upgrade"
	task.Manifest.Version = "2.0.0"
	packageTaskDigest(t, &task)
	result, err = executor.Deploy(context.Background(), task)
	if err == nil || !taskOutcomeIsUncertain(err) || result.Resources.State != "review-required" {
		t.Fatalf("failure state: %+v %v", result.Resources, err)
	}
	if !slices.Equal(backend.calls, []string{"inspect", "prepare", "backup", "apply", "healthy"}) {
		t.Fatalf("rollback or misordered operation: %v", backend.calls)
	}
	backend.calls = nil
	if _, err = executor.Deploy(context.Background(), task); err == nil || len(backend.calls) > 0 {
		t.Fatalf("uncertain operation was replayed: %v", backend.calls)
	}
}

func TestPackageRejectsUnauthorizedAndManifestMismatchBeforeBackend(t *testing.T) {
	for _, scenario := range []string{"digest", "missing-grant", "extra-grant", "future-runtime", "injected-id"} {
		t.Run(scenario, func(t *testing.T) {
			task := packageTestTask(t)
			switch scenario {
			case "digest":
				task.ManifestSHA256 = strings.Repeat("0", 64)
			case "missing-grant":
				task.Manifest.Runtime.RequiredCapabilities = []string{"root"}
				task.Manifest.Runtime.Docker.Containers[0].User = "0"
				packageTaskDigest(t, &task)
			case "extra-grant":
				task.AuthorizedCapabilities = []string{"root"}
			case "future-runtime":
				task.Manifest.Runtime.Version = 2
				packageTaskDigest(t, &task)
			case "injected-id":
				task.ApplicationID = "instance\nExecStart=/evil"
			}
			backend := &fakePackageBackend{}
			_, err := (PackageExecutor{StateDirectory: packageTestDirectory(t), Backend: backend}).Deploy(context.Background(), task)
			if err == nil || len(backend.calls) > 0 {
				t.Fatalf("unsafe task reached backend: %v", backend.calls)
			}
		})
	}
}

func TestPackageAdoptionOnlyInspectsAndWritesReceipt(t *testing.T) {
	task := packageTestTask(t)
	task.Operation = "adopt"
	task.Manifest.Runtime = nil
	task.Manifest.PackageRevision = 0
	task.PackageRevision = 0
	task.Resources = &InstanceResources{Version: 1, ApplicationID: task.ApplicationID, AppKey: task.AppKey, Runtime: "docker", Resources: []RuntimeResource{{Kind: "container", Name: "historical-name", ID: "same-container", StartedAt: "unchanged"}}}
	backend := &fakePackageBackend{}
	executor := PackageExecutor{StateDirectory: packageTestDirectory(t), Backend: backend}
	result, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(backend.calls, []string{"inspect"}) || result.Resources.Resources[0].ID != "same-container" || result.Resources.Resources[0].Name != "historical-name" {
		t.Fatal("adoption mutated runtime")
	}
}

type packagePull struct{ io.ReadCloser }

func (packagePull) Wait(context.Context) error { return nil }
func (packagePull) JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error] {
	return func(func(jsonstream.Message, error) bool) {}
}

type fakePackageDocker struct {
	fakeApplicationResourceEngine
	next      int
	calls     []string
	created   []client.ContainerCreateOptions
	archives  map[string][]byte
	data      map[string][]byte
	failStart bool
}

func newPackageDocker() *fakePackageDocker {
	return &fakePackageDocker{fakeApplicationResourceEngine: fakeApplicationResourceEngine{containers: map[string]client.ContainerInspectResult{}, volumes: map[string]client.VolumeInspectResult{}}, archives: map[string][]byte{}, data: map[string][]byte{}}
}
func (e *fakePackageDocker) ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
	e.calls = append(e.calls, "pull")
	return packagePull{io.NopCloser(strings.NewReader(""))}, nil
}
func (e *fakePackageDocker) ContainerCreate(_ context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	e.next++
	id := "container-" + strconv.Itoa(e.next)
	e.calls = append(e.calls, "create")
	e.created = append(e.created, options)
	response := container.InspectResponse{ID: id, Name: options.Name, Config: options.Config, HostConfig: options.HostConfig, State: &container.State{}}
	for _, mount := range options.HostConfig.Mounts {
		response.Mounts = append(response.Mounts, container.MountPoint{Type: mount.Type, Name: mount.Source, Source: mount.Source, Destination: mount.Target, RW: !mount.ReadOnly})
	}
	e.containers[options.Name] = client.ContainerInspectResult{Container: response}
	return client.ContainerCreateResult{ID: id}, nil
}
func (e *fakePackageDocker) ContainerStart(ctx context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	e.calls = append(e.calls, "start")
	if e.failStart {
		return client.ContainerStartResult{}, errors.New("failure")
	}
	v, err := e.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return client.ContainerStartResult{}, err
	}
	v.Container.State.Running = true
	v.Container.State.StartedAt = "started-" + id
	e.containers[v.Container.Name] = v
	return client.ContainerStartResult{}, nil
}
func (e *fakePackageDocker) ContainerStop(ctx context.Context, id string, _ client.ContainerStopOptions) (client.ContainerStopResult, error) {
	e.calls = append(e.calls, "stop")
	v, err := e.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return client.ContainerStopResult{}, err
	}
	v.Container.State.Running = false
	e.containers[v.Container.Name] = v
	return client.ContainerStopResult{}, nil
}
func (e *fakePackageDocker) ContainerRename(context.Context, string, client.ContainerRenameOptions) (client.ContainerRenameResult, error) {
	return client.ContainerRenameResult{}, errors.New("unexpected rename")
}
func (e *fakePackageDocker) CopyToContainer(_ context.Context, id string, options client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	e.calls = append(e.calls, "private-config")
	data, err := io.ReadAll(options.Content)
	e.archives[id] = data
	return client.CopyToContainerResult{}, err
}
func (e *fakePackageDocker) CopyFromContainer(ctx context.Context, id string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	e.calls = append(e.calls, "backup")
	for _, c := range e.containers {
		if c.Container.State.Running {
			return client.CopyFromContainerResult{}, errors.New("backup raced running writer")
		}
	}
	v, err := e.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return client.CopyFromContainerResult{}, err
	}
	for _, m := range v.Container.Mounts {
		if m.Destination == options.SourcePath {
			return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(e.data[m.Name]))}, nil
		}
	}
	return client.CopyFromContainerResult{}, errors.New("unknown mount")
}

func TestPackageDockerCompleteLifecycleAndDataRetention(t *testing.T) {
	task := packageTestTask(t)
	engine := newPackageDocker()
	directory := packageTestDirectory(t)
	backend := &DockerPackageBackend{Docker: engine, StateDirectory: directory}
	executor := PackageExecutor{StateDirectory: directory, Backend: backend}
	result, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	volume := resourceNamed(result.Resources, "volume", "data").Name
	engine.data[volume] = []byte("original data")
	if engine.created[0].Config.User != "65532:65532" || len(engine.created[0].HostConfig.CapDrop) != 1 {
		t.Fatal("unsafe default process")
	}
	task.ID = "upgrade"
	task.Operation = "upgrade"
	task.Manifest.Version = "1.1.0"
	packageTaskDigest(t, &task)
	result, err = executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources.Backups) != 1 {
		t.Fatal("no upgrade backup")
	}
	snapshot, err := os.ReadFile(result.Resources.Backups[0].Path)
	if err != nil || string(snapshot) != "original data" {
		t.Fatal("incorrect backup")
	}
	if resourceNamed(result.Resources, "volume", "data").Name != volume {
		t.Fatal("storage name changed")
	}
	task.ID = "uninstall"
	task.Operation = "uninstall"
	result, err = executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resources.State != "retained" || len(engine.containers) != 0 || len(engine.volumes) != 1 || engine.volumeRemoves != 0 {
		t.Fatal("uninstall did not retain data")
	}
}

func TestPackageDockerRejectsOwnershipCollisionBeforePull(t *testing.T) {
	task := packageTestTask(t)
	engine := newPackageDocker()
	name := packageIdentity(task.ApplicationID) + "-app"
	engine.containers[name] = client.ContainerInspectResult{Container: container.InspectResponse{ID: "foreign", Name: name, Config: &container.Config{}}}
	directory := packageTestDirectory(t)
	_, err := (PackageExecutor{StateDirectory: directory, Backend: &DockerPackageBackend{Docker: engine, StateDirectory: directory}}).Deploy(context.Background(), task)
	if err == nil || len(engine.calls) != 0 || engine.containerRemoves != 0 {
		t.Fatal("foreign container was touched")
	}
}

func TestPackageDockerFailureNeverRestartsOldImageAgainstNewData(t *testing.T) {
	task := packageTestTask(t)
	engine := newPackageDocker()
	directory := packageTestDirectory(t)
	executor := PackageExecutor{StateDirectory: directory, Backend: &DockerPackageBackend{Docker: engine, StateDirectory: directory}}
	if _, err := executor.Deploy(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	engine.failStart = true
	engine.calls = nil
	task.ID = "failed-upgrade"
	task.Operation = "upgrade"
	task.Manifest.Version = "2.0.0"
	packageTaskDigest(t, &task)
	result, err := executor.Deploy(context.Background(), task)
	if err == nil || !taskOutcomeIsUncertain(err) || result.Resources.State != "review-required" {
		t.Fatal("upgrade did not fail closed")
	}
	starts := 0
	for _, call := range engine.calls {
		if call == "start" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("automatic rollback observed: %v", engine.calls)
	}
}

func TestPackageSystemdCompleteLifecycleBothArchitectures(t *testing.T) {
	for _, architecture := range []string{"amd64", "arm64"} {
		t.Run(architecture, func(t *testing.T) {
			task := packageTestTask(t)
			binary := testArtifactELF(t, architecture)
			digest := sha256.Sum256(binary)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(binary) }))
			defer server.Close()
			task.Manifest.Images = nil
			task.Manifest.Artifacts = []catalog.Artifact{{Name: "app", OperatingSystem: "linux", Architecture: architecture, URL: server.URL + "/app", SHA256: hex.EncodeToString(digest[:]), Format: "raw"}}
			task.Manifest.Runtime = &catalog.RuntimeSpec{Kind: "systemd", Version: 1, Storage: []catalog.Storage{{Name: "data", Persistent: true}}, Systemd: &catalog.SystemdRuntime{Artifact: "app", Executable: "app", Files: []catalog.ArtifactFile{{Path: "app", Executable: true}}, StateDirectories: []string{"data"}}}
			packageTaskDigest(t, &task)
			root := packageTestDirectory(t)
			directory := packageTestDirectory(t)
			var calls []string
			manager := SystemdHostApplicationManager{RootDir: root, HTTPClient: server.Client(), HostTarget: platform.Target{OS: "linux", Architecture: architecture}, RunCommand: func(ctx context.Context, name string, args ...string) error {
				calls = append(calls, name+" "+strings.Join(args, " "))
				if name == "systemctl" && args[0] == "start" {
					return os.MkdirAll(filepath.Join(root, "var/lib/"+packageIdentity(task.ApplicationID)+"-data"), 0700)
				}
				return nil
			}, ReadCommand: func(context.Context, string, ...string) ([]byte, error) {
				return []byte("MainPID=123\nInvocationID=fixed-invocation\nActiveEnterTimestampMonotonic=100\n"), nil
			}}
			executor := PackageExecutor{StateDirectory: directory, Backend: &SystemdPackageBackend{Manager: manager, StateDirectory: directory}}
			result, err := executor.Deploy(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			unit := resourceNamed(result.Resources, "unit", "service")
			unitData, _ := os.ReadFile(manager.path(unit.Path))
			if !bytes.Contains(unitData, []byte("User=65534\n")) || unit.MainPID != 123 {
				t.Fatal("missing confinement or runtime identity")
			}
			dataPath := manager.path(resourceNamed(result.Resources, "directory", "data").Path)
			if err := os.WriteFile(filepath.Join(dataPath, "database"), []byte("version-one"), 0600); err != nil {
				t.Fatal(err)
			}
			task.ID = "upgrade"
			task.Operation = "upgrade"
			task.Manifest.Version = "1.1.0"
			packageTaskDigest(t, &task)
			result, err = executor.Deploy(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Resources.Backups) == 0 {
				t.Fatal("native backup missing")
			}
			task.ID = "remove"
			task.Operation = "uninstall"
			result, err = executor.Deploy(context.Background(), task)
			if err != nil {
				t.Fatal(err)
			}
			if result.Resources.State != "retained" {
				t.Fatal("data not retained")
			}
			if _, err := os.Stat(filepath.Join(dataPath, "database")); err != nil {
				t.Fatal(err)
			}
			for _, command := range calls {
				if strings.Contains(command, "sh ") || strings.Contains(command, "bash ") {
					t.Fatal("shell invoked")
				}
			}
		})
	}
}

func TestPackageReceiptRejectsSymlinkParent(t *testing.T) {
	directory := packageTestDirectory(t)
	outside := packageTestDirectory(t)
	if err := os.Symlink(outside, filepath.Join(directory, "packages")); err != nil {
		t.Fatal(err)
	}
	backend := &fakePackageBackend{}
	if _, err := (PackageExecutor{StateDirectory: directory, Backend: backend}).Deploy(context.Background(), packageTestTask(t)); err == nil || len(backend.calls) != 0 {
		t.Fatal("symlink escaped package state root")
	}
}
