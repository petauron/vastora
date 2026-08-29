package deployer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gatewayruntime"
	"gopkg.in/yaml.v3"
)

const (
	headscaleRollbackContainer = "vastora-center-headscale-rollback"
	headscaleRollbackDirectory = ".vastora-headscale-rollback"
)

var headscaleConfigFiles = []string{"config.yaml", "derp.yaml", "policy.hujson"}

type headscaleReplacement struct {
	installer        DockerHeadscaleInstaller
	backupID         string
	backupWasRunning bool
}

func (installer DockerHeadscaleInstaller) validateHeadscaleConfig(ctx context.Context, docker *client.Client, files map[string][]byte) error {
	if err := validateHeadscaleCandidateFiles(files); err != nil {
		return err
	}
	volumeName := "vastora-headscale-configtest-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volumeName, Labels: map[string]string{
		gatewayruntime.ManagedLabel:   "true",
		gatewayruntime.ComponentLabel: "center-headscale-configtest",
	}}); err != nil {
		return fmt.Errorf("deployer: create Headscale config validation volume: %w", err)
	}
	defer func() {
		_, _ = docker.VolumeRemove(context.WithoutCancel(ctx), volumeName, client.VolumeRemoveOptions{Force: true})
	}()

	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image: installer.HeadscaleImage,
			Cmd:   []string{"configtest"},
			Labels: map[string]string{
				gatewayruntime.ManagedLabel:   "true",
				gatewayruntime.ComponentLabel: "center-headscale-configtest",
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode("none"),
			ReadonlyRootfs: true,
			Mounts:         []mount.Mount{{Type: mount.TypeVolume, Source: volumeName, Target: "/etc/headscale"}},
		},
	})
	if err != nil {
		return fmt.Errorf("deployer: create Headscale config validation container: %w", err)
	}
	defer func() {
		_, _ = docker.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	for _, name := range headscaleConfigFiles {
		content, ok := files[name]
		if !ok {
			return fmt.Errorf("deployer: Headscale candidate is missing %s", name)
		}
		if err := copyFileMode(ctx, docker, created.ID, "/etc/headscale", name, content, 0o644); err != nil {
			return fmt.Errorf("deployer: stage Headscale candidate %s: %w", name, err)
		}
	}
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("deployer: start Headscale config validation: %w", err)
	}
	var status container.WaitResponse
	select {
	case err := <-wait.Error:
		return fmt.Errorf("deployer: wait for Headscale config validation: %w", err)
	case status = <-wait.Result:
	case <-ctx.Done():
		return ctx.Err()
	}
	if status.StatusCode == 0 {
		return nil
	}
	logs, logErr := docker.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if logErr != nil {
		return fmt.Errorf("deployer: Headscale rejected candidate configuration (exit %d)", status.StatusCode)
	}
	defer logs.Close()
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, io.LimitReader(logs, 1<<20))
	detail := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
	if detail == "" {
		detail = "no diagnostic output"
	}
	return fmt.Errorf("deployer: Headscale rejected candidate configuration (exit %d): %s", status.StatusCode, detail)
}

func validateHeadscaleCandidateFiles(files map[string][]byte) error {
	for _, name := range headscaleConfigFiles {
		if _, ok := files[name]; !ok {
			return fmt.Errorf("deployer: Headscale candidate is missing %s", name)
		}
	}
	for _, name := range []string{"config.yaml", "derp.yaml"} {
		decoder := yaml.NewDecoder(bytes.NewReader(files[name]))
		var document yaml.Node
		if err := decoder.Decode(&document); err != nil {
			return fmt.Errorf("deployer: parse Headscale candidate %s: %w", name, err)
		}
		if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
			return fmt.Errorf("deployer: Headscale candidate %s must contain one YAML mapping", name)
		}
		var extra yaml.Node
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return fmt.Errorf("deployer: Headscale candidate %s contains multiple YAML documents", name)
			}
			return fmt.Errorf("deployer: parse Headscale candidate %s: %w", name, err)
		}
	}
	return nil
}

func (installer DockerHeadscaleInstaller) replaceHeadscale(ctx context.Context, docker *client.Client, files map[string][]byte) (headscaleReplacement, error) {
	if err := installer.recoverHeadscaleBackup(ctx, docker); err != nil {
		return headscaleReplacement{}, err
	}
	if err := installer.validateHeadscaleConfig(ctx, docker, files); err != nil {
		return headscaleReplacement{}, err
	}
	replacement := headscaleReplacement{installer: installer}
	if err := installer.snapshotHeadscaleConfig(); err != nil {
		return headscaleReplacement{}, err
	}
	existing, err := inspectManagedContainer(ctx, docker, DefaultHeadscaleContainer, "center-headscale")
	if err != nil {
		_ = replacement.restoreConfig()
		return headscaleReplacement{}, err
	}
	if existing != nil {
		replacement.backupID = existing.Container.ID
		replacement.backupWasRunning = existing.Container.State != nil && existing.Container.State.Running
		if replacement.backupWasRunning {
			if _, err := docker.ContainerStop(ctx, existing.Container.ID, client.ContainerStopOptions{}); err != nil {
				_ = replacement.restoreConfig()
				return headscaleReplacement{}, fmt.Errorf("deployer: stop existing Headscale: %w", err)
			}
		}
		if _, err := docker.ContainerRename(ctx, existing.Container.ID, client.ContainerRenameOptions{NewName: headscaleRollbackContainer}); err != nil {
			if replacement.backupWasRunning {
				_, _ = docker.ContainerStart(ctx, existing.Container.ID, client.ContainerStartOptions{})
			}
			_ = replacement.restoreConfig()
			return headscaleReplacement{}, fmt.Errorf("deployer: preserve existing Headscale: %w", err)
		}
	}
	for _, name := range headscaleConfigFiles {
		if err := writeAtomic(filepath.Join(installer.ConfigDir, name), files[name], 0o644); err != nil {
			rollbackErr := replacement.rollback(ctx, docker)
			return headscaleReplacement{}, errors.Join(err, rollbackErr)
		}
	}
	config, hostConfig := installer.headscaleContainerConfig()
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: config, HostConfig: hostConfig, NetworkingConfig: dockerruntime.NetworkingConfig(dockerruntime.HeadscaleAlias), Name: DefaultHeadscaleContainer,
	})
	if err != nil {
		rollbackErr := replacement.rollback(ctx, docker)
		return headscaleReplacement{}, errors.Join(fmt.Errorf("deployer: create Headscale container: %w", err), rollbackErr)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		rollbackErr := replacement.rollback(ctx, docker)
		return headscaleReplacement{}, errors.Join(fmt.Errorf("deployer: start Headscale container: %w", err), rollbackErr)
	}
	return replacement, nil
}

func (installer DockerHeadscaleInstaller) snapshotHeadscaleConfig() error {
	backup := filepath.Join(installer.ConfigDir, headscaleRollbackDirectory)
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("deployer: clear stale Headscale config backup: %w", err)
	}
	if err := os.MkdirAll(backup, 0o700); err != nil {
		return fmt.Errorf("deployer: create Headscale config backup: %w", err)
	}
	for _, name := range headscaleConfigFiles {
		content, err := os.ReadFile(filepath.Join(installer.ConfigDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("deployer: back up Headscale config %s: %w", name, err)
		}
		if err := writeAtomic(filepath.Join(backup, name), content, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (replacement headscaleReplacement) restoreConfig() error {
	backup := filepath.Join(replacement.installer.ConfigDir, headscaleRollbackDirectory)
	if _, err := os.Stat(backup); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("deployer: inspect Headscale config backup: %w", err)
	}
	var failures []error
	for _, name := range headscaleConfigFiles {
		backupPath := filepath.Join(backup, name)
		content, err := os.ReadFile(backupPath)
		if errors.Is(err, os.ErrNotExist) {
			if removeErr := os.Remove(filepath.Join(replacement.installer.ConfigDir, name)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("deployer: remove new Headscale config %s: %w", name, removeErr))
			}
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("deployer: read Headscale config backup %s: %w", name, err))
			continue
		}
		if err := writeAtomic(filepath.Join(replacement.installer.ConfigDir, name), content, 0o644); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (replacement headscaleReplacement) rollback(ctx context.Context, docker *client.Client) error {
	var failures []error
	current, err := inspectManagedContainer(ctx, docker, DefaultHeadscaleContainer, "center-headscale")
	if err != nil {
		failures = append(failures, err)
	} else if current != nil {
		if _, err := docker.ContainerRemove(ctx, current.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			failures = append(failures, fmt.Errorf("deployer: remove failed Headscale: %w", err))
		}
	}
	if err := replacement.restoreConfig(); err != nil {
		failures = append(failures, err)
	}
	if replacement.backupID != "" {
		if _, err := docker.ContainerRename(ctx, replacement.backupID, client.ContainerRenameOptions{NewName: DefaultHeadscaleContainer}); err != nil {
			failures = append(failures, fmt.Errorf("deployer: restore Headscale container name: %w", err))
		} else if replacement.backupWasRunning {
			if _, err := docker.ContainerStart(ctx, replacement.backupID, client.ContainerStartOptions{}); err != nil {
				failures = append(failures, fmt.Errorf("deployer: restart previous Headscale: %w", err))
			}
		}
	}
	return errors.Join(failures...)
}

func (replacement headscaleReplacement) commit(ctx context.Context, docker *client.Client) error {
	if replacement.backupID != "" {
		if _, err := docker.ContainerRemove(ctx, replacement.backupID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("deployer: finalize Headscale replacement: %w", err)
		}
	}
	if err := os.RemoveAll(filepath.Join(replacement.installer.ConfigDir, headscaleRollbackDirectory)); err != nil {
		return fmt.Errorf("deployer: remove Headscale config backup: %w", err)
	}
	return nil
}

func (installer DockerHeadscaleInstaller) recoverHeadscaleBackup(ctx context.Context, docker *client.Client) error {
	backup, err := inspectManagedContainer(ctx, docker, headscaleRollbackContainer, "center-headscale")
	if err != nil {
		return err
	}
	if backup == nil {
		return os.RemoveAll(filepath.Join(installer.ConfigDir, headscaleRollbackDirectory))
	}
	replacement := headscaleReplacement{installer: installer, backupID: backup.Container.ID, backupWasRunning: true}
	return replacement.rollback(ctx, docker)
}
