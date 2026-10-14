package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"testing"

	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
)

type configurationDocker struct {
	*fakePackageDocker
	content []byte
	missing bool
}

func literalValue(text string) catalog.Value {
	raw := json.RawMessage(text)
	return catalog.Value{Literal: &raw}
}

func configurationTar(name string, data []byte, flag byte) []byte {
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	header := &tar.Header{Name: name, Typeflag: flag, Mode: 0600, Uid: 65532, Gid: 65532}
	if flag == tar.TypeReg {
		header.Size = int64(len(data))
	} else {
		header.Linkname = "/etc/shadow"
	}
	_ = writer.WriteHeader(header)
	if flag == tar.TypeReg {
		_, _ = writer.Write(data)
	}
	_ = writer.Close()
	return output.Bytes()
}

func (engine *configurationDocker) CopyFromContainer(ctx context.Context, id string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	if options.SourcePath != "/app/config.json" {
		return engine.fakePackageDocker.CopyFromContainer(ctx, id, options)
	}
	current, err := engine.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || engine.missing {
		return client.CopyFromContainerResult{}, errors.New("missing configuration")
	}
	if current.Container.State.Running {
		return client.CopyFromContainerResult{}, errors.New("writer is not stopped")
	}
	return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(configurationTar("config.json", engine.content, tar.TypeReg)))}, nil
}

func TestThirdPartyUpgradePreservesOpaqueDockerConfiguration(t *testing.T) {
	task := packageTestTask(t)
	task.Manifest.Runtime.Docker.Containers[0].Environment = map[string]catalog.Value{"APP_SETTING": literalValue(`"initial"`)}
	task.Manifest.Runtime.Docker.Containers[0].ConfigFiles = []catalog.ConfigFile{{Path: "/app/config.json", Format: "json", Values: map[string]catalog.Value{"initialized": literalValue(`true`)}}}
	packageTaskDigest(t, &task)
	engine := &configurationDocker{fakePackageDocker: newPackageDocker(), content: []byte(`{"api-keys":["one","two","three","four"],"providers":[{"name":"example"}],"custom-setting":false}`)}
	executor := PackageExecutor{StateDirectory: packageTestDirectory(t), Backend: &DockerPackageBackend{Docker: engine, StateDirectory: ""}}
	executor.Backend.(*DockerPackageBackend).StateDirectory = executor.StateDirectory
	installed, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	old := resourceNamed(installed.Resources, "container", "app")
	if !bytes.Contains(engine.archives[old.ID], []byte("initialized")) {
		t.Fatal("first install did not initialize")
	}
	oldRuntime := engine.containers[old.Name]
	oldRuntime.Container.Config.Env = []string{"APP_SETTING=application-changed"}
	engine.containers[old.Name] = oldRuntime
	task.ID, task.Operation, task.Manifest.Version = "upgrade-preserving-config", "upgrade", "2.0.0"
	packageTaskDigest(t, &task)
	upgraded, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	current := resourceNamed(upgraded.Resources, "container", "app")
	if engine.created[len(engine.created)-1].Config.Env[0] != "APP_SETTING=application-changed" {
		t.Fatal("upgrade reinitialized the process environment")
	}
	reader := tar.NewReader(bytes.NewReader(engine.archives[current.ID]))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			t.Fatal("preserved configuration missing")
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != "app/config.json" {
			continue
		}
		data, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(data, engine.content) {
			t.Fatal("upgrade reinitialized business configuration")
		}
		break
	}
	if len(upgraded.Resources.Backups) != 1 {
		t.Fatal("configuration recovery evidence not retained")
	}
	if info, err := os.Stat(upgraded.Resources.Backups[0].Path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("configuration snapshot is not private")
	}
	// Removing an initializer from a newer recipe must not erase a file that
	// the application still owns. Retain locations, not parsed settings.
	task.ID, task.Manifest.Version = "upgrade-without-initializer", "3.0.0"
	task.Manifest.Runtime.Docker.Containers[0].ConfigFiles = nil
	packageTaskDigest(t, &task)
	upgraded, err = executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if len(upgraded.Resources.ConfigurationPaths["app"]) != 1 || !bytes.Contains(engine.archives[resourceNamed(upgraded.Resources, "container", "app").ID], engine.content) {
		t.Fatal("new recipe omitted existing application configuration")
	}
}

func TestThirdPartyUpgradeRejectsDetachedPersistentData(t *testing.T) {
	task := packageTestTask(t)
	engine := newPackageDocker()
	directory := packageTestDirectory(t)
	executor := PackageExecutor{StateDirectory: directory, Backend: &DockerPackageBackend{Docker: engine, StateDirectory: directory}}
	installed, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	old := resourceNamed(installed.Resources, "container", "app")
	task.ID, task.Operation, task.Manifest.Version = "detach-storage", "upgrade", "2.0.0"
	task.Manifest.Runtime.Storage = nil
	task.Manifest.Runtime.Docker.Containers[0].Mounts = nil
	packageTaskDigest(t, &task)
	if _, err := executor.Deploy(context.Background(), task); err == nil {
		t.Fatal("upgrade detached persistent storage")
	}
	current, err := engine.ContainerInspect(context.Background(), old.ID, client.ContainerInspectOptions{})
	if err != nil || !current.Container.State.Running || len(engine.created) != 1 {
		t.Fatal("storage rejection mutated runtime")
	}
}

func TestThirdPartyUpgradeRejectsMissingDockerConfigurationWithoutReplacement(t *testing.T) {
	task := packageTestTask(t)
	task.Manifest.Runtime.Docker.Containers[0].ConfigFiles = []catalog.ConfigFile{{Path: "/app/config.json", Format: "json", Values: map[string]catalog.Value{"initialized": literalValue(`true`)}}}
	packageTaskDigest(t, &task)
	engine := &configurationDocker{fakePackageDocker: newPackageDocker(), missing: true}
	directory := packageTestDirectory(t)
	executor := PackageExecutor{StateDirectory: directory, Backend: &DockerPackageBackend{Docker: engine, StateDirectory: directory}}
	installed, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	old := resourceNamed(installed.Resources, "container", "app")
	task.ID, task.Operation, task.Manifest.Version = "missing-config", "upgrade", "2.0.0"
	packageTaskDigest(t, &task)
	if _, err := executor.Deploy(context.Background(), task); err == nil {
		t.Fatal("upgrade initialized missing configuration")
	}
	current, err := engine.ContainerInspect(context.Background(), old.ID, client.ContainerInspectOptions{})
	if err != nil || !current.Container.State.Running || len(engine.created) != 1 {
		t.Fatal("failed capture replaced or left stopped the original container")
	}
}

func TestThirdPartyConfigurationArchiveRejectsLinksAndWrongMembers(t *testing.T) {
	for _, flag := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeDir} {
		if _, _, err := readSingleConfiguration(bytes.NewReader(configurationTar("config.json", nil, flag)), "config.json"); err == nil {
			t.Fatal("accepted non-regular configuration")
		}
	}
	for _, name := range []string{"../config.json", "/config.json", "other.json"} {
		if _, _, err := readSingleConfiguration(bytes.NewReader(configurationTar(name, []byte("secret"), tar.TypeReg)), "config.json"); err == nil {
			t.Fatal("accepted unexpected configuration path")
		}
	}
	data, _, err := readSingleConfiguration(bytes.NewReader(configurationTar("config.json", []byte("opaque"), tar.TypeReg)), path.Base("/app/config.json"))
	if err != nil || string(data) != "opaque" {
		t.Fatal("bounded opaque file rejected")
	}
}

func TestThirdPartyConfigurationPathsRejectUnsafeAndKeepMountBoundaries(t *testing.T) {
	for _, value := range []string{"relative", "/", "/a/../b", "/a\nb"} {
		if _, err := configurationDeclaration([]string{value}); err == nil {
			t.Fatal("unsafe recorded configuration path accepted")
		}
	}
	if _, err := configurationDeclaration([]string{"/app/config.json"}); err != nil {
		t.Fatal(err)
	}
}

func TestThirdPartyNativeConfigurationClassification(t *testing.T) {
	for _, logical := range []string{"config:config.yaml", "credential:token", "environment"} {
		if !applicationOwnedNativeFile(logical) {
			t.Fatal("application configuration not preserved")
		}
	}
	for _, logical := range []string{"artifact:app", "service", "directory"} {
		if applicationOwnedNativeFile(logical) {
			t.Fatal("executable ownership check weakened")
		}
	}
	root := t.TempDir()
	if _, err := captureApplicationConfiguration(root + "/missing"); err == nil {
		t.Fatal("missing file accepted")
	}
	if err := os.Symlink("/etc/passwd", root+"/link"); err != nil {
		t.Fatal(err)
	}
	if _, err := captureApplicationConfiguration(root + "/link"); err == nil {
		t.Fatal("symlink accepted")
	}
}
