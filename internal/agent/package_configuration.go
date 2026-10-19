package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

const packageConfigurationLimit = 16 << 20

// Capture opaque application-owned configuration, never its parsed settings.
// The container remains stopped from capture until replacement. Snapshots are
// private, integrity checked, and persisted before any old container is removed.
func (b *DockerPackageBackend) preserveUpgradeConfiguration(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) (err error) {
	if task.Operation != "upgrade" {
		return nil
	}
	// An image-only installation has never created a configuration writer or
	// persistent storage. Starting its first declared component is initialization,
	// not replacement of application-owned configuration. Inspect has already
	// verified these immutable image resources against the installed receipt.
	imageOnly := len(receipt.Resources) > 0 && len(receipt.ConfigurationPaths) == 0 && len(receipt.Backups) == 0
	for _, resource := range receipt.Resources {
		if resource.Kind != "image" {
			imageOnly = false
			break
		}
	}
	var stopped []string
	defer func() {
		if err != nil {
			for _, id := range stopped {
				_, resumeErr := b.Docker.ContainerStart(ctx, id, client.ContainerStartOptions{})
				err = errors.Join(err, resumeErr)
			}
		}
	}()
	for i := range b.plans {
		plan := &b.plans[i]
		plan.configPaths = append(slices.Clone(plan.configPaths), receipt.ConfigurationPaths[plan.logical]...)
		slices.Sort(plan.configPaths)
		plan.configPaths = slices.Compact(plan.configPaths)
		if len(plan.configPaths) == 0 {
			continue
		}
		declared, declarationErr := configurationDeclaration(plan.configPaths)
		if declarationErr != nil {
			return declarationErr
		}
		resource := "configuration:" + plan.logical
		var saved *RuntimeBackup
		for j := range receipt.Backups {
			if receipt.Backups[j].ID == task.ID && receipt.Backups[j].Resource == resource {
				saved = &receipt.Backups[j]
				break
			}
		}
		if saved != nil {
			plan.files, err = readPackageBackup(b.StateDirectory, task, *saved)
			if err != nil {
				return errors.New("agent: preserved configuration snapshot could not be verified")
			}
			continue
		}
		old := resourceNamed(receipt, "container", plan.logical)
		if old == nil {
			if imageOnly {
				continue
			}
			return errors.New("agent: upgrade requires existing application configuration; explicit migration is required")
		}
		current, inspectErr := b.Docker.ContainerInspect(ctx, old.ID, client.ContainerInspectOptions{})
		if inspectErr != nil || current.Container.ID != old.ID {
			return errors.New("agent: configuration source container changed")
		}
		if current.Container.State != nil && current.Container.State.Running {
			timeout := 30
			if _, stopErr := b.Docker.ContainerStop(ctx, old.ID, client.ContainerStopOptions{Timeout: &timeout}); stopErr != nil {
				return errors.New("agent: configuration writer could not be stopped")
			}
			stopped = append(stopped, old.ID)
		}
		var retainedMounts []mount.Mount
		for _, next := range plan.options.HostConfig.Mounts {
			for _, previous := range current.Container.Mounts {
				if next.Target == previous.Destination && (next.Type == mount.TypeVolume && previous.Name == next.Source || next.Type == mount.TypeBind && previous.Source == next.Source) {
					retainedMounts = append(retainedMounts, next)
				}
			}
		}
		preserved, captureErr := captureContainerConfiguration(ctx, b.Docker, old.ID, declared, retainedMounts)
		if captureErr != nil {
			return captureErr
		}
		backup, saveErr := savePackageBackup(b.StateDirectory, task, RuntimeResource{Name: resource, Path: "/"}, receipt.PackageVersion, bytes.NewReader(preserved))
		if saveErr != nil {
			return saveErr
		}
		receipt.Backups = append(receipt.Backups, backup)
		if err = persist(); err != nil {
			return err
		}
		plan.files = preserved
	}
	return nil
}

// The union includes prior paths even when a newer recipe stops mentioning
// them. No configuration content or initializer is rendered for this step.
func configurationDeclaration(paths []string) ([]byte, error) {
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	directories := map[string]bool{}
	if len(paths) > 1024 {
		return nil, errors.New("agent: too many configuration paths")
	}
	for _, location := range paths {
		if !path.IsAbs(location) || location == "/" || path.Clean(location) != location || strings.ContainsAny(location, "\\\x00\r\n") || len(location) > 4096 {
			return nil, errors.New("agent: unsafe retained configuration location")
		}
		name := strings.TrimPrefix(location, "/")
		var parents []string
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			parents = append(parents, parent)
		}
		for i := len(parents) - 1; i >= 0; i-- {
			if directories[parents[i]] {
				continue
			}
			if err := writer.WriteHeader(&tar.Header{Name: parents[i] + "/", Typeflag: tar.TypeDir, Mode: 0755}); err != nil {
				return nil, err
			}
			directories[parents[i]] = true
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600}); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// Docker's archive API supplies the requested file under its basename. Rebase
// only a single bounded regular file onto the already-reviewed recipe path.
// Reject links, extra entries and malformed/truncated archives before copying.
func captureContainerConfiguration(ctx context.Context, docker packageDockerEngine, id string, declared []byte, retainedMounts []mount.Mount) ([]byte, error) {
	reader := tar.NewReader(bytes.NewReader(declared))
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("agent: invalid declared configuration archive")
		}
		if header.Typeflag == tar.TypeDir {
			if configurationInMount("/"+strings.TrimSuffix(header.Name, "/"), retainedMounts) {
				continue
			}
			if err := writer.WriteHeader(header); err != nil {
				return nil, err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return nil, errors.New("agent: configuration declaration is not a regular file")
		}
		copied, err := docker.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: "/" + header.Name})
		if err != nil || copied.Content == nil {
			return nil, errors.New("agent: existing configuration is unavailable; refusing initialization during upgrade")
		}
		data, original, readErr := readSingleConfiguration(copied.Content, path.Base(header.Name))
		closeErr := copied.Content.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.New("agent: existing configuration is unsafe or unreadable")
		}
		total += int64(len(data))
		if total > packageConfigurationLimit {
			return nil, errors.New("agent: configuration snapshot exceeds limit")
		}
		// Reused storage already contains this exact application-owned file.
		// Do not rewrite its data or metadata through Docker's archive API.
		if configurationInMount("/"+header.Name, retainedMounts) {
			continue
		}
		copy := *header
		copy.Size, copy.Mode, copy.Uid, copy.Gid = int64(len(data)), original.Mode&0777, original.Uid, original.Gid
		if err := writer.WriteHeader(&copy); err != nil {
			return nil, err
		}
		if _, err := writer.Write(data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func configurationInMount(filePath string, mounts []mount.Mount) bool {
	for _, mounted := range mounts {
		if filePath == mounted.Target || strings.HasPrefix(filePath, strings.TrimSuffix(mounted.Target, "/")+"/") {
			return true
		}
	}
	return false
}

func readSingleConfiguration(source io.Reader, expected string) ([]byte, *tar.Header, error) {
	reader := tar.NewReader(io.LimitReader(source, packageConfigurationLimit+4096))
	header, err := reader.Next()
	if err != nil || header.Name != expected || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > packageConfigurationLimit {
		return nil, nil, errors.New("agent: configuration archive is not a bounded regular file")
	}
	data, err := io.ReadAll(reader)
	if err != nil || int64(len(data)) != header.Size {
		return nil, nil, errors.New("agent: truncated configuration archive")
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("agent: configuration archive has unexpected entries")
	}
	return data, header, nil
}

func applicationOwnedNativeFile(logical string) bool {
	return strings.HasPrefix(logical, "config:") || strings.HasPrefix(logical, "credential:") || logical == "environment"
}

func captureApplicationConfiguration(filePath string) (hostFileSnapshot, error) {
	info, err := os.Lstat(filePath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > packageConfigurationLimit {
		return hostFileSnapshot{}, errors.New("agent: existing configuration is missing or unsafe")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return hostFileSnapshot{}, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return hostFileSnapshot{}, errors.New("agent: configuration changed during inspection")
	}
	data, err := io.ReadAll(io.LimitReader(file, packageConfigurationLimit+1))
	if err != nil || len(data) > packageConfigurationLimit {
		return hostFileSnapshot{}, errors.New("agent: configuration exceeds limit or is unreadable")
	}
	return hostFileSnapshot{Exists: true, Data: data, Mode: info.Mode().Perm()}, nil
}
