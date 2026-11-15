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
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
)

type senlunMigrationDocker struct{ *fakePackageDocker }

func (d *senlunMigrationDocker) ContainerList(_ context.Context, _ client.ContainerListOptions) (client.ContainerListResult, error) {
	var result client.ContainerListResult
	for _, current := range d.containers {
		if current.Container.State != nil && current.Container.State.Running {
			result.Items = append(result.Items, container.Summary{ID: current.Container.ID, Mounts: current.Container.Mounts})
		}
	}
	return result, nil
}

func (d *senlunMigrationDocker) ContainerRename(ctx context.Context, id string, options client.ContainerRenameOptions) (client.ContainerRenameResult, error) {
	current, err := d.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return client.ContainerRenameResult{}, err
	}
	delete(d.containers, current.Container.Name)
	current.Container.Name = options.NewName
	d.containers[options.NewName] = current
	return client.ContainerRenameResult{}, nil
}

func (d *senlunMigrationDocker) CopyToContainer(ctx context.Context, id string, options client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	data, err := io.ReadAll(options.Content)
	if err != nil {
		return client.CopyToContainerResult{}, err
	}
	if options.DestinationPath == "/var/lib" {
		current, err := d.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil || len(current.Container.Mounts) != 1 {
			return client.CopyToContainerResult{}, errors.New("missing target mount")
		}
		d.data[current.Container.Mounts[0].Name] = data
	} else {
		d.archives[id] = data
	}
	return client.CopyToContainerResult{}, nil
}

func senlunTestArchive(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	archive := tar.NewWriter(&data)
	for _, header := range []*tar.Header{
		{Name: "senlun/", Typeflag: tar.TypeDir, Mode: 0700, Uid: 65532, Gid: 65532},
		{Name: "senlun/database", Typeflag: tar.TypeReg, Mode: 0600, Uid: 65532, Gid: 65532, Size: 4},
	} {
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size != 0 {
			if _, err := archive.Write([]byte("data")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestSenlunServiceIdentityTransferPreservesStateWithoutBackup(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		name := "success"
		if corrupt {
			name = "unsafe-source-restores-original"
		}
		t.Run(name, func(t *testing.T) {
			var original catalog.AppManifest
			for _, app := range officialContractFixture(t).Apps {
				if app.ID == "pulse" {
					original = app
				}
			}
			if original.Runtime == nil || original.Runtime.Docker == nil {
				t.Fatal("missing source service fixture")
			}
			original.Runtime.Docker.Containers[0].Mounts[0].Target = "/var/lib/senlun"
			raw, _ := json.Marshal(original)
			var target catalog.AppManifest
			if err := json.Unmarshal(raw, &target); err != nil {
				t.Fatal(err)
			}
			target.ID, target.PackageRevision = "senlun", original.PackageRevision+1
			target.Images[0].Name = "senlun"
			target.Runtime.Docker.Containers[0].Name = "senlun"
			target.Runtime.Docker.Containers[0].Image = "senlun"
			original, err := catalog.CanonicalAppManifest(original)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(original)
			originalSHA := sha256.Sum256(raw)
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			installation := AppliedInstallation{InstanceID: "original-task", ApplicationID: "same-instance", AppKey: "vastora-official/pulse", Version: original.Version, Manifest: original, Config: json.RawMessage(`{"public_url":"https://example.test"}`), Secrets: json.RawMessage(`{"token":"fixture-private-value"}`)}
			if _, err := store.RecordApplied(ctx, installation); err != nil {
				t.Fatal(err)
			}
			docker := &senlunMigrationDocker{newPackageDocker()}
			for _, volume := range []string{"source-volume", "historical-volume"} {
				if err := ensureOwnedApplicationVolume(ctx, docker, volume, installation.AppKey, applicationVolumeComponent(volume), installation.ApplicationID); err != nil {
					t.Fatal(err)
				}
			}
			created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "service", Config: &container.Config{Image: original.Images[0].Reference, Env: []string{"SENLUN_PUBLIC_URL=https://example.test"}, Labels: applicationResourceLabels(installation.AppKey, "pulse", installation.ApplicationID, installation.InstanceID)}, HostConfig: &container.HostConfig{NetworkMode: "bridge", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "source-volume", Target: "/var/lib/senlun"}}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
				t.Fatal(err)
			}
			docker.data["source-volume"] = senlunTestArchive(t)
			if corrupt {
				docker.data["source-volume"] = []byte("invalid archive")
			}
			receipt := &InstanceResources{Version: 1, ApplicationID: installation.ApplicationID, AppKey: installation.AppKey, Runtime: "docker", State: "ready", TaskID: installation.InstanceID, PackageVersion: original.Version, PackageRevision: original.PackageRevision, ManifestSHA256: hex.EncodeToString(originalSHA[:]), Resources: []RuntimeResource{
				{Kind: "volume", LogicalName: "data", Name: "source-volume", Path: "/var/lib/senlun", Component: applicationVolumeComponent("source-volume"), Persistent: true},
				{Kind: "retained-volume", Name: "historical-volume", Component: applicationVolumeComponent("historical-volume"), Persistent: true},
				{Kind: "container", LogicalName: "pulse", Name: "service", ID: created.ID, Component: "pulse"},
			}, Backups: []RuntimeBackup{{ID: "existing-backup"}}}
			packages := PackageExecutor{StateDirectory: store.dataDir}
			if err := packages.save(receipt); err != nil {
				t.Fatal(err)
			}
			raw, err = os.ReadFile(packages.receiptPath(installation.ApplicationID))
			if err != nil {
				t.Fatal(err)
			}
			sourceSHA := sha256.Sum256(raw)
			task := DeploymentTask{ID: "migration-task", ApplicationID: installation.ApplicationID, AppKey: "vastora-official/senlun", Operation: "install", Manifest: target, SourceReceiptSHA256: hex.EncodeToString(sourceSHA[:]), Config: installation.Config}
			packageTaskDigest(t, &task)
			backend := &DockerPackageBackend{Docker: docker, StateDirectory: store.dataDir, CheckHealth: func(context.Context, string, int, catalog.Service) error { return nil }}
			executor := ApplicationExecutor{Store: store, PackageBackend: backend}
			result, err := executor.renameSenlunServiceIdentity(ctx, task)
			if corrupt {
				if err == nil || !taskOutcomeIsUncertain(err) {
					t.Fatal("invalid data was accepted")
				}
				current, inspectErr := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
				if inspectErr != nil || !current.Container.State.Running || current.Container.Name != "service" {
					t.Fatal("original service was not restored before target start")
				}
				if _, ok := docker.volumes["source-volume"]; !ok {
					t.Fatal("original volume was removed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Resources.Backups, receipt.Backups) {
				t.Fatal("existing backup metadata changed")
			}
			volume := resourceNamed(result.Resources, "volume", "data")
			if volume == nil || !bytes.Equal(docker.data[volume.Name], senlunTestArchive(t)) {
				t.Fatal("target data differs")
			}
			updated, err := store.AppliedInstallation(ctx, task.AppKey)
			if err != nil || updated.ApplicationID != installation.ApplicationID || string(updated.Config) != string(installation.Config) || string(updated.Secrets) != string(installation.Secrets) {
				t.Fatal("application identity or credentials were lost")
			}
			if _, ok := docker.volumes["source-volume"]; !ok {
				t.Fatal("source was removed before Center acknowledgement")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(packages.receiptPath(installation.ApplicationID)), "backups")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("migration created a backup")
			}
			if err := store.completeSenlunIdentity(ctx, task); err != nil {
				t.Fatal(err)
			}
			if err := executor.FinishSenlunServiceIdentity(ctx, task); err != nil {
				t.Fatal(err)
			}
			if _, ok := docker.volumes["source-volume"]; ok {
				t.Fatal("old active volume was not retired")
			}
			if _, ok := docker.volumes["historical-volume"]; !ok {
				t.Fatal("historical retained volume was changed")
			}
			if _, err := packages.ReadResources(installation.ApplicationID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSenlunVolumeTransferRejectsOtherRunningWriters(t *testing.T) {
	ctx := context.Background()
	docker := &senlunMigrationDocker{newPackageDocker()}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "other", Config: &container.Config{}, HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: "source-volume", Target: "/data"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := noSenlunVolumeWriters(ctx, docker, "source-volume"); err == nil {
		t.Fatal("accepted a concurrent source-volume writer")
	}
	if _, err := docker.ContainerStop(ctx, created.ID, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := noSenlunVolumeWriters(ctx, docker, "source-volume"); err != nil {
		t.Fatal(err)
	}
}
