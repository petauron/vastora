package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

type PackageMaintenanceTask struct {
	Action   string `json:"action"`
	BackupID string `json:"backupId,omitempty"`
}
type RuntimeLog struct {
	Resource string `json:"resource"`
	Content  string `json:"content"`
}
type PackageMaintenanceResult struct {
	Logs     []RuntimeLog `json:"logs,omitempty"`
	BackupID string       `json:"backupId,omitempty"`
}

type packageMaintenanceBackend interface {
	PackageBackend
	Resume(context.Context, DeploymentTask, *InstanceResources) error
	Logs(context.Context, DeploymentTask, *InstanceResources) ([]RuntimeLog, error)
	Restore(context.Context, DeploymentTask, *InstanceResources, []RuntimeBackup, func() error) error
}

func (e ApplicationExecutor) ManagePackage(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	if task.PackageMaintenance == nil || !slices.Contains([]string{"logs", "backup", "restore"}, task.PackageMaintenance.Action) {
		return ApplicationTaskResult{}, errors.New("agent: unsupported package maintenance request")
	}
	var backend packageMaintenanceBackend
	if injected, ok := e.PackageBackend.(packageMaintenanceBackend); ok {
		return maintainPackage(ctx, PackageExecutor{StateDirectory: e.packageDirectory(), Backend: injected}, injected, task)
	}
	receipt, err := e.ApplicationResources(task.ApplicationID)
	if err != nil {
		return ApplicationTaskResult{}, err
	}
	if receipt.Runtime == "systemd" {
		manager, ok := e.Host.(SystemdHostApplicationManager)
		if !ok {
			return ApplicationTaskResult{}, errors.New("agent: native package manager unavailable")
		}
		backend = &SystemdPackageBackend{Manager: manager, StateDirectory: e.packageDirectory()}
	} else if receipt.Runtime == "docker" {
		socket := e.DockerSocket
		if socket == "" {
			socket = "unix:///var/run/docker.sock"
		}
		docker, err := client.New(client.WithHost(socket))
		if err != nil {
			return ApplicationTaskResult{}, err
		}
		defer docker.Close()
		backend = &DockerPackageBackend{Docker: docker, StateDirectory: e.packageDirectory()}
	} else {
		return ApplicationTaskResult{}, errors.New("agent: unsupported receipt runtime")
	}
	return maintainPackage(ctx, PackageExecutor{StateDirectory: e.packageDirectory(), Backend: backend}, backend, task)
}

func maintainPackage(ctx context.Context, executor PackageExecutor, backend packageMaintenanceBackend, task DeploymentTask) (result ApplicationTaskResult, resultErr error) {
	if task.PackageMaintenance == nil {
		return result, errors.New("agent: missing package maintenance request")
	}
	action := task.PackageMaintenance.Action
	if task.Manifest.Runtime == nil && task.PackageRevision == 0 {
		if err := validateHistoricalPackageTask(task); err != nil {
			return result, err
		}
	} else {
		validation := task
		validation.Operation = "configure"
		if err := validatePackageTask(validation); err != nil {
			return result, err
		}
	}
	receipt, err := executor.ReadResources(task.ApplicationID)
	if err != nil {
		return result, err
	}
	if receipt.AppKey != task.AppKey || receipt.State != "ready" || receipt.ManifestSHA256 != task.ManifestSHA256 || receipt.PackageRevision != task.PackageRevision {
		return result, errors.New("agent: maintenance requires the exact healthy installed receipt")
	}
	if err := backend.Inspect(ctx, task, receipt); err != nil {
		return result, err
	}
	result.PackageMaintenance = &PackageMaintenanceResult{}
	if action == "logs" {
		logs, err := backend.Logs(ctx, task, receipt)
		if err != nil {
			return result, err
		}
		_, secrets, _ := runtimeInputs(task)
		for i := range logs {
			for _, secret := range secrets {
				if secret != "" {
					logs[i].Content = strings.ReplaceAll(logs[i].Content, secret, "[REDACTED]")
				}
			}
		}
		result.PackageMaintenance.Logs = logs
		receipt.TaskID = task.ID
		result.Resources = receipt
		return result, executor.save(receipt)
	}
	if action != "backup" && action != "restore" {
		return result, errors.New("agent: unsupported package maintenance action")
	}
	storageFound := false
	for _, resource := range receipt.Resources {
		storageFound = storageFound || resource.Kind == "volume" || resource.Kind == "directory" || resource.Kind == "bind"
	}
	if !storageFound {
		return result, errors.New("agent: package has no managed data to back up")
	}
	var selected []RuntimeBackup
	if action == "restore" {
		if task.PackageMaintenance.BackupID == "" {
			return result, errors.New("agent: restore requires a recorded backup ID")
		}
		for _, backup := range receipt.Backups {
			if backup.ID != task.PackageMaintenance.BackupID {
				continue
			}
			if backup.PackageVersion != receipt.PackageVersion {
				return result, errors.New("agent: restore cannot cross package versions; use matched offline recovery")
			}
			if _, err := readPackageBackup(executor.StateDirectory, task, backup); err != nil {
				return result, err
			}
			selected = append(selected, backup)
		}
		if len(selected) == 0 {
			return result, errors.New("agent: recorded backup set not found")
		}
		if receipt.Runtime == "docker" {
			for _, resource := range receipt.Resources {
				if resource.Kind == "bind" {
					return result, errors.New("agent: external host mounts require explicit offline data recovery")
				}
			}
			if task.Manifest.Runtime == nil {
				return result, errors.New("agent: historical Docker restore requires an explicit upgrade to a signed v4 recipe")
			}
			// Compile/download before stopping the current writers. No arbitrary
			// container configuration may be reconstructed from user input.
			prepare := task
			prepare.Operation = "configure"
			if err := backend.Prepare(ctx, prepare, receipt); err != nil {
				return result, err
			}
		}
	}
	receipt.State, receipt.TaskID = "backing-up", task.ID
	if err := executor.save(receipt); err != nil {
		return result, err
	}
	result.Resources = receipt
	defer func() {
		if resultErr != nil {
			receipt.State = "review-required"
			resultErr = uncertainTaskOutcome(errors.Join(resultErr, executor.save(receipt)))
		}
	}()
	if err := backend.Backup(ctx, task, receipt); err != nil {
		return result, err
	}
	if err := executor.save(receipt); err != nil {
		return result, err
	}
	result.PackageMaintenance.BackupID = task.ID
	if action == "restore" {
		receipt.State = "restoring"
		if err := executor.save(receipt); err != nil {
			return result, err
		}
		if err := backend.Restore(ctx, task, receipt, selected, func() error { return executor.save(receipt) }); err != nil {
			return result, err
		}
	} else if err := backend.Resume(ctx, task, receipt); err != nil {
		return result, err
	}
	receipt.State = "ready"
	return result, executor.save(receipt)
}

func readPackageBackup(directory string, task DeploymentTask, backup RuntimeBackup) ([]byte, error) {
	expected := filepath.Join(directory, "packages", packageIdentity(task.ApplicationID), "backups", packageIdentity(backup.ID), packageIdentity(backup.Resource)+".tar")
	if backup.ID == "" || backup.Path != expected {
		return nil, errors.New("agent: backup is outside the recorded instance namespace")
	}
	if err := checkPackageParents(backup.Path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(backup.Path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 512<<20 {
		return nil, errors.New("agent: backup is missing, unsafe or exceeds restore limit")
	}
	raw, err := os.ReadFile(backup.Path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != backup.SHA256 {
		return nil, errors.New("agent: backup integrity mismatch")
	}
	if err := validateBackupTar(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func validateBackupTar(raw []byte) error {
	reader := tar.NewReader(bytes.NewReader(raw))
	seen := map[string]bool{}
	var size int64
	for count := 0; ; count++ {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if count > 100000 {
			return errors.New("agent: backup has too many entries")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || name == "." || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || name == ".." || strings.ContainsAny(name, "\\\x00\r\n") || seen[name] {
			return errors.New("agent: unsafe or duplicate backup member")
		}
		seen[name] = true
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA && header.Typeflag != tar.TypeDir {
			return errors.New("agent: backup links and special files are forbidden")
		}
		size += header.Size
		if header.Size < 0 || size > 512<<20 {
			return errors.New("agent: backup expansion exceeds limit")
		}
	}
}

func (b *DockerPackageBackend) Resume(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	for _, id := range b.resumeIDs {
		if _, err := b.Docker.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
			return errors.New("agent: original package process could not resume after backup")
		}
		current, err := b.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil || current.Container.State == nil || !current.Container.State.Running {
			return errors.New("agent: resumed package process is not running")
		}
		for i := range receipt.Resources {
			if receipt.Resources[i].Kind == "container" && receipt.Resources[i].ID == id {
				receipt.Resources[i].StartedAt = current.Container.State.StartedAt
			}
		}
	}
	return b.Healthy(ctx, task, receipt)
}
func (b *DockerPackageBackend) Logs(ctx context.Context, task DeploymentTask, receipt *InstanceResources) ([]RuntimeLog, error) {
	engine, ok := b.Docker.(interface {
		ContainerLogs(context.Context, string, client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	})
	if !ok {
		return nil, errors.New("agent: Docker log reader unavailable")
	}
	var result []RuntimeLog
	for _, resource := range receipt.Resources {
		if resource.Kind != "container" {
			continue
		}
		stream, err := engine.ContainerLogs(ctx, resource.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "200"})
		if err != nil {
			return nil, errors.New("agent: container logs unavailable")
		}
		raw, err := io.ReadAll(io.LimitReader(stream, 256<<10))
		_ = stream.Close()
		if err != nil {
			return nil, err
		}
		var decoded bytes.Buffer
		if _, err := stdcopy.StdCopy(&decoded, &decoded, bytes.NewReader(raw)); err != nil {
			decoded.Reset()
			decoded.Write(raw)
		}
		content := decoded.Bytes()
		if len(content) > 64<<10 {
			content = content[len(content)-(64<<10):]
		}
		result = append(result, RuntimeLog{Resource: resource.Name, Content: string(content)})
	}
	return result, nil
}

// Restore data into fresh named volumes. Existing volumes remain untouched as
// recovery copies; only after all archives have been copied do managed container
// bindings change. The helper container is never started or given a shell.
func (b *DockerPackageBackend) Restore(ctx context.Context, task DeploymentTask, receipt *InstanceResources, backups []RuntimeBackup, persist func() error) error {
	for _, resource := range slices.Clone(receipt.Resources) {
		if resource.Kind == "bind" {
			return errors.New("agent: external host mounts require explicit offline data recovery")
		}
		if resource.Kind != "volume" || !resource.Persistent {
			continue
		}
		var backup *RuntimeBackup
		for i := range backups {
			if backups[i].Resource == resource.Name {
				backup = &backups[i]
			}
		}
		if backup == nil {
			return errors.New("agent: restore snapshot does not cover every persistent volume")
		}
		raw, err := readPackageBackup(b.StateDirectory, task, *backup)
		if err != nil {
			return err
		}
		name := packageIdentity(task.ApplicationID) + "-restore-" + strings.TrimPrefix(packageIdentity(task.ID+resource.LogicalName), "vastora-pkg-")
		if err := ensureOwnedApplicationVolume(ctx, b.Docker, name, task.AppKey, applicationVolumeComponent(name), task.ApplicationID); err != nil {
			return err
		}
		var plan *packageContainerPlan
		for i := range b.plans {
			for _, mounted := range b.plans[i].options.HostConfig.Mounts {
				if mounted.Source == resource.Name {
					plan = &b.plans[i]
				}
			}
		}
		if plan == nil {
			return errors.New("agent: recorded volume has no current recipe binding")
		}
		options := plan.options
		hostCopy := *options.HostConfig
		options.HostConfig = &hostCopy
		options.HostConfig.Mounts = []mount.Mount{{Type: mount.TypeVolume, Source: name, Target: backup.Target, VolumeOptions: &mount.VolumeOptions{NoCopy: true}}}
		options.Name = name + "-copy"
		created, err := b.Docker.ContainerCreate(ctx, options)
		if err != nil {
			return errors.New("agent: restore staging container could not be created")
		}
		if _, err := b.Docker.CopyToContainer(ctx, created.ID, client.CopyToContainerOptions{DestinationPath: path.Dir(backup.Target), Content: bytes.NewReader(raw)}); err != nil {
			return errors.New("agent: restore archive could not be copied")
		}
		if _, err := b.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{}); err != nil {
			return errors.New("agent: restore helper could not be retired")
		}
		for i := range b.plans {
			for j := range b.plans[i].options.HostConfig.Mounts {
				mounted := &b.plans[i].options.HostConfig.Mounts[j]
				if mounted.Source == resource.Name {
					mounted.Source = name
					mounted.VolumeOptions = &mount.VolumeOptions{NoCopy: true}
				}
			}
		}
		old := resourceNamed(receipt, "volume", resource.LogicalName)
		old.Name, old.Component = name, applicationVolumeComponent(name)
		// Keep the predecessor visible but it is never mounted into new runtime.
		retained := resource
		retained.Kind = "retained-volume"
		retained.LogicalName = "restore-predecessor:" + task.ID + ":" + resource.LogicalName
		receipt.Resources = append(receipt.Resources, retained)
		if err := persist(); err != nil {
			return err
		}
	}
	if err := b.Apply(ctx, task, receipt, persist); err != nil {
		return err
	}
	return b.Healthy(ctx, task, receipt)
}

func (b *SystemdPackageBackend) Resume(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	if !b.resumeService {
		return nil
	}
	unit := resourceNamed(receipt, "unit", "service")
	if unit == nil {
		return errors.New("agent: systemd ownership receipt missing")
	}
	if err := b.Manager.run(ctx, "systemctl", "start", unit.Name); err != nil {
		return errors.New("agent: original service could not resume after backup")
	}
	return b.Healthy(ctx, task, receipt)
}
func (b *SystemdPackageBackend) Logs(ctx context.Context, task DeploymentTask, receipt *InstanceResources) ([]RuntimeLog, error) {
	unit := resourceNamed(receipt, "unit", "service")
	if unit == nil {
		return nil, errors.New("agent: systemd ownership receipt missing")
	}
	args := []string{"--unit", unit.Name, "--no-pager", "--lines=200", "--output=cat"}
	var raw []byte
	var err error
	if b.Manager.ReadCommand != nil {
		raw, err = b.Manager.ReadCommand(ctx, "journalctl", args...)
	} else {
		raw, err = exec.CommandContext(ctx, "journalctl", args...).Output()
	}
	if err != nil {
		return nil, errors.New("agent: service journal unavailable")
	}
	if len(raw) > 64<<10 {
		raw = raw[len(raw)-(64<<10):]
	}
	return []RuntimeLog{{Resource: unit.Name, Content: string(raw)}}, nil
}

func (b *SystemdPackageBackend) Restore(ctx context.Context, task DeploymentTask, receipt *InstanceResources, backups []RuntimeBackup, persist func() error) error {
	for _, resource := range receipt.Resources {
		if resource.Kind != "directory" || !resource.Persistent {
			continue
		}
		var backup *RuntimeBackup
		for i := range backups {
			if backups[i].Resource == resource.Name {
				backup = &backups[i]
			}
		}
		if backup == nil {
			return errors.New("agent: restore snapshot does not cover every persistent directory")
		}
		raw, err := readPackageBackup(b.StateDirectory, task, *backup)
		if err != nil {
			return err
		}
		original := b.Manager.path(resource.Path)
		if err := checkPackageParents(filepath.Join(original, "member")); err != nil {
			return err
		}
		info, err := os.Lstat(original)
		if err != nil || !info.IsDir() {
			return errors.New("agent: restore target is not an owned state directory")
		}
		staged := original + ".restore-" + packageIdentity(task.ID)
		previous := original + ".retained-" + packageIdentity(task.ID)
		if _, err := os.Lstat(previous); !errors.Is(err, os.ErrNotExist) {
			return errors.New("agent: prior restore residue requires review")
		}
		if err := extractBackupDirectory(raw, staged, info); err != nil {
			return err
		}
		if err := os.Rename(original, previous); err != nil {
			return err
		}
		if err := os.Rename(staged, original); err != nil {
			return err
		}
		if err := persist(); err != nil {
			return err
		}
	}
	return b.Resume(ctx, task, receipt)
}

func extractBackupDirectory(raw []byte, destination string, original os.FileInfo) error {
	if err := validateBackupTar(raw); err != nil {
		return err
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	reader := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if header.Typeflag == tar.TypeDir {
			if err := root.MkdirAll(name, 0700); err != nil {
				return err
			}
			continue
		}
		if err := root.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return err
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(header.Mode)&0700)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, reader)
		syncErr, closeErr := file.Sync(), file.Close()
		if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
			return err
		}
	}
	// Apply only the original directory owner's UID/GID, never tar ownership.
	if stat, ok := original.Sys().(*syscall.Stat_t); ok && os.Geteuid() == 0 {
		if err := filepath.WalkDir(destination, func(path string, _ os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			return os.Chown(path, int(stat.Uid), int(stat.Gid))
		}); err != nil {
			return err
		}
	}
	return os.Chmod(destination, original.Mode().Perm()&0700)
}
