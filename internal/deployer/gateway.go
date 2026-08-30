package deployer

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

const (
	gatewayReplacementJournalFile    = "gateway-replacement.json"
	gatewayReplacementJournalVersion = 1
)

type gatewayReplacementJournal struct {
	Version          int    `json:"version"`
	Generation       string `json:"generation"`
	Phase            string `json:"phase"`
	CandidateID      string `json:"candidateId,omitempty"`
	BackupID         string `json:"backupId,omitempty"`
	LegacyID         string `json:"legacyId,omitempty"`
	Layer4ID         string `json:"layer4Id,omitempty"`
	BackupWasRunning bool   `json:"backupWasRunning"`
	LegacyWasRunning bool   `json:"legacyWasRunning"`
	Layer4WasRunning bool   `json:"layer4WasRunning"`
}

type gatewayReplacement struct {
	generation       string
	journalPath      string
	candidateID      string
	backupID         string
	legacyID         string
	layer4ID         string
	backupWasRunning bool
	legacyWasRunning bool
	layer4WasRunning bool
	socket           string
}

func (installer DockerHeadscaleInstaller) replaceGateway(ctx context.Context, docker *client.Client, caddyfile []byte, centerBindAddresses, publicBindAddresses []string) (gatewayReplacement, error) {
	if err := installer.recoverGatewayBackup(ctx, docker); err != nil {
		return gatewayReplacement{}, err
	}
	generation, err := newGatewayReplacementGeneration()
	if err != nil {
		return gatewayReplacement{}, err
	}
	replacement := gatewayReplacement{generation: generation, journalPath: filepath.Join(installer.ConfigDir, gatewayReplacementJournalFile), socket: installer.CaddyAdminSocket}
	existing, err := inspectManagedContainer(ctx, docker, DefaultGatewayContainer, gatewayruntime.CaddyComponentLabel)
	if err != nil {
		return gatewayReplacement{}, err
	}
	legacy, err := inspectManagedContainer(ctx, docker, gatewayruntime.LegacyCenterCaddyContainer, "center-headscale-gateway")
	if err != nil {
		return gatewayReplacement{}, err
	}
	if legacy != nil {
		replacement.legacyID = legacy.Container.ID
		replacement.legacyWasRunning = legacy.Container.State != nil && legacy.Container.State.Running
	}
	layer4, err := inspectManagedContainer(ctx, docker, gatewayruntime.HAProxyContainer, gatewayruntime.Layer4ComponentLabel)
	if err != nil {
		return gatewayReplacement{}, err
	}
	if layer4 != nil {
		replacement.layer4ID = layer4.Container.ID
		replacement.layer4WasRunning = layer4.Container.State != nil && layer4.Container.State.Running
	}
	if existing != nil {
		replacement.backupWasRunning = existing.Container.State != nil && existing.Container.State.Running
		replacement.backupID = existing.Container.ID
	}
	if err := replacement.persist("prepared"); err != nil {
		return gatewayReplacement{}, err
	}
	if existing != nil {
		if replacement.backupWasRunning {
			if _, err := docker.ContainerStop(ctx, existing.Container.ID, client.ContainerStopOptions{}); err != nil {
				cause := fmt.Errorf("deployer: stop existing unified gateway: %w", err)
				return gatewayReplacement{}, errors.Join(cause, replacement.rollback(context.WithoutCancel(ctx), docker))
			}
		}
		if _, err := docker.ContainerRename(ctx, existing.Container.ID, client.ContainerRenameOptions{NewName: gatewayRollbackContainer}); err != nil {
			cause := fmt.Errorf("deployer: preserve existing unified gateway: %w", err)
			return gatewayReplacement{}, errors.Join(cause, replacement.rollback(context.WithoutCancel(ctx), docker))
		}
	}
	if err := os.MkdirAll(filepath.Dir(installer.CaddyAdminSocket), 0o700); err != nil {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, fmt.Errorf("deployer: create Caddy Admin socket directory: %w", err)
	}
	if err := os.Remove(installer.CaddyAdminSocket); err != nil && !os.IsNotExist(err) {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, fmt.Errorf("deployer: remove stale Caddy Admin socket: %w", err)
	}
	initialState := gateway.DesiredState{Revision: 1, Listeners: []gateway.Listener{{Kind: "system", Address: "127.0.0.1", HTTPPort: 80, HTTPSPort: 443}}}
	for _, address := range centerBindAddresses {
		initialState.Listeners = append(initialState.Listeners, gateway.Listener{Kind: "headscale", Address: address, HTTPPort: 80, HTTPSPort: 443})
	}
	for _, address := range publicBindAddresses {
		initialState.Listeners = append(initialState.Listeners, gateway.Listener{Kind: "public", Address: address, HTTPPort: 80, HTTPSPort: 443})
	}
	exposedPorts, portBindings, err := gatewayruntime.DockerPorts(initialState)
	if err != nil {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, err
	}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{
			Image:        installer.CaddyImage,
			Cmd:          []string{"caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"},
			ExposedPorts: exposedPorts,
			Labels: map[string]string{
				gatewayruntime.ManagedLabel:        "true",
				gatewayruntime.ComponentLabel:      gatewayruntime.CaddyComponentLabel,
				gatewayruntime.SystemServicesLabel: gatewayruntime.SystemServices,
			},
		},
		HostConfig: &container.HostConfig{
			NetworkMode:   container.NetworkMode(dockerruntime.NetworkName),
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			PortBindings:  portBindings,
			Mounts: []mount.Mount{
				{Type: mount.TypeVolume, Source: installer.CaddyDataVolume, Target: "/data"},
				{Type: mount.TypeVolume, Source: installer.CaddyConfigVolume, Target: "/config"},
				{Type: mount.TypeBind, Source: filepath.Dir(installer.CaddyAdminSocket), Target: filepath.Dir(installer.CaddyAdminSocket)},
			},
		},
		NetworkingConfig: dockerruntime.NetworkingConfig(dockerruntime.CaddyAlias),
		Name:             DefaultGatewayContainer,
	})
	if err != nil {
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, fmt.Errorf("deployer: create unified gateway: %w", err)
	}
	replacement.candidateID = created.ID
	if err := replacement.persist("prepared"); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		rollbackErr := replacement.rollback(ctx, docker)
		return gatewayReplacement{}, errors.Join(err, rollbackErr)
	}
	if err := copyFile(ctx, docker, created.ID, "/etc/caddy", "Caddyfile", caddyfile); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		_ = replacement.rollback(ctx, docker)
		return gatewayReplacement{}, err
	}
	for _, file := range []struct {
		name    string
		content string
	}{{"center.crt", installer.CenterCertificatePEM}, {"center.key", installer.CenterPrivateKeyPEM}} {
		if err := copyFile(ctx, docker, created.ID, "/etc/caddy", "system/"+file.name, []byte(file.content)); err != nil {
			_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
			_ = replacement.rollback(ctx, docker)
			return gatewayReplacement{}, err
		}
	}
	for index, alias := range installer.CenterAliases {
		for _, file := range []struct {
			name    string
			content string
		}{
			{filepath.Base(centerAliasCertificatePath(index)), alias.CertificatePEM},
			{filepath.Base(centerAliasPrivateKeyPath(index)), alias.CertificateKeyPEM},
		} {
			if err := copyFile(ctx, docker, created.ID, "/etc/caddy", "system/"+file.name, []byte(file.content)); err != nil {
				_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
				_ = replacement.rollback(ctx, docker)
				return gatewayReplacement{}, err
			}
		}
	}
	if replacement.legacyWasRunning {
		if _, err := docker.ContainerStop(ctx, legacy.Container.ID, client.ContainerStopOptions{}); err != nil {
			_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
			_ = replacement.rollback(ctx, docker)
			return gatewayReplacement{}, fmt.Errorf("deployer: stop legacy Center gateway: %w", err)
		}
	}
	if replacement.layer4WasRunning {
		if _, err := docker.ContainerStop(ctx, layer4.Container.ID, client.ContainerStopOptions{}); err != nil {
			_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
			_ = replacement.rollback(ctx, docker)
			return gatewayReplacement{}, fmt.Errorf("deployer: stop shared-443 frontend for gateway migration: %w", err)
		}
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		rollbackErr := replacement.rollback(ctx, docker)
		if rollbackErr != nil {
			return gatewayReplacement{}, errors.Join(fmt.Errorf("deployer: start unified gateway: %w", err), rollbackErr)
		}
		return gatewayReplacement{}, fmt.Errorf("deployer: start unified gateway: %w", err)
	}
	return replacement, nil
}

func (installer DockerHeadscaleInstaller) recoverGatewayBackup(ctx context.Context, docker *client.Client) error {
	journalPath := filepath.Join(installer.ConfigDir, gatewayReplacementJournalFile)
	journal, found, err := loadGatewayReplacementJournal(journalPath)
	if err != nil {
		return err
	}
	backup, err := inspectManagedContainer(ctx, docker, gatewayRollbackContainer, gatewayruntime.CaddyComponentLabel)
	if err != nil {
		return err
	}
	current, err := inspectManagedContainer(ctx, docker, DefaultGatewayContainer, gatewayruntime.CaddyComponentLabel)
	if err != nil {
		return err
	}
	if !found {
		if backup == nil {
			return nil
		}
		// Releases before the durable journal could leave both names behind.
		// Their current container is never proof of commit, so preserve the
		// rollback as the last-known-good gateway.
		return (gatewayReplacement{journalPath: journalPath, socket: installer.CaddyAdminSocket, backupID: backup.Container.ID, backupWasRunning: true}).rollback(ctx, docker)
	}
	replacement := gatewayReplacement{
		generation: journal.Generation, journalPath: journalPath, socket: installer.CaddyAdminSocket,
		candidateID: journal.CandidateID, backupID: journal.BackupID, legacyID: journal.LegacyID, layer4ID: journal.Layer4ID,
		backupWasRunning: journal.BackupWasRunning, legacyWasRunning: journal.LegacyWasRunning, layer4WasRunning: journal.Layer4WasRunning,
	}
	if committedGatewayReady(journal, current, installer.CaddyAdminSocket) {
		return replacement.finalize(ctx, docker)
	}
	return replacement.rollback(ctx, docker)
}

func committedGatewayReady(journal gatewayReplacementJournal, current *client.ContainerInspectResult, socket string) bool {
	return journal.Phase == "committed" && current != nil && current.Container.ID == journal.CandidateID && gatewayContainerHealthy(current, socket)
}

func (replacement gatewayReplacement) rollback(ctx context.Context, docker *client.Client) error {
	var failures []error
	originalStillCurrent := false
	if current, err := inspectManagedContainer(ctx, docker, DefaultGatewayContainer, gatewayruntime.CaddyComponentLabel); err != nil {
		failures = append(failures, err)
	} else if current != nil {
		if replacement.backupID != "" && current.Container.ID == replacement.backupID {
			originalStillCurrent = true
			if replacement.backupWasRunning && (current.Container.State == nil || !current.Container.State.Running) {
				if replacement.socket != "" {
					if err := os.Remove(replacement.socket); err != nil && !os.IsNotExist(err) {
						failures = append(failures, fmt.Errorf("deployer: clear preserved gateway Admin socket: %w", err))
					}
				}
				if _, err := docker.ContainerStart(ctx, current.Container.ID, client.ContainerStartOptions{}); err != nil {
					failures = append(failures, fmt.Errorf("deployer: restart preserved unified gateway: %w", err))
				}
			}
		} else if replacement.candidateID != "" && current.Container.ID != replacement.candidateID {
			failures = append(failures, errors.New("deployer: current gateway does not belong to the pending replacement"))
		} else if _, err := docker.ContainerRemove(ctx, current.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			failures = append(failures, fmt.Errorf("deployer: remove failed unified gateway: %w", err))
		}
	}
	if replacement.socket != "" && !originalStillCurrent {
		if err := os.Remove(replacement.socket); err != nil && !os.IsNotExist(err) {
			failures = append(failures, fmt.Errorf("deployer: clear failed gateway Admin socket: %w", err))
		}
	}
	if replacement.backupID != "" && !originalStillCurrent {
		if _, err := docker.ContainerRename(ctx, replacement.backupID, client.ContainerRenameOptions{NewName: DefaultGatewayContainer}); err != nil {
			failures = append(failures, fmt.Errorf("deployer: restore unified gateway name: %w", err))
		} else if replacement.backupWasRunning {
			backup, inspectErr := inspectManagedContainer(ctx, docker, replacement.backupID, gatewayruntime.CaddyComponentLabel)
			if inspectErr != nil {
				failures = append(failures, inspectErr)
			} else if backup == nil || backup.Container.State == nil || !backup.Container.State.Running {
				if _, err := docker.ContainerStart(ctx, replacement.backupID, client.ContainerStartOptions{}); err != nil {
					failures = append(failures, fmt.Errorf("deployer: restart previous unified gateway: %w", err))
				}
			}
		}
	}
	if replacement.legacyID != "" && replacement.legacyWasRunning {
		if err := ensureManagedContainerRunning(ctx, docker, replacement.legacyID, "center-headscale-gateway"); err != nil {
			failures = append(failures, fmt.Errorf("deployer: restart legacy Center gateway: %w", err))
		}
	}
	if replacement.layer4ID != "" && replacement.layer4WasRunning {
		if err := ensureManagedContainerRunning(ctx, docker, replacement.layer4ID, gatewayruntime.Layer4ComponentLabel); err != nil {
			failures = append(failures, fmt.Errorf("deployer: restart shared-443 frontend: %w", err))
		}
	}
	if len(failures) != 0 {
		return errors.Join(failures...)
	}
	if replacement.journalPath != "" {
		if err := removeGatewayReplacementJournal(replacement.journalPath); err != nil {
			return fmt.Errorf("deployer: clear rolled-back gateway replacement: %w", err)
		}
	}
	return nil
}

func ensureManagedContainerRunning(ctx context.Context, docker *client.Client, id, component string) error {
	managed, err := inspectManagedContainer(ctx, docker, id, component)
	if err != nil {
		return err
	}
	if managed == nil {
		return errors.New("managed rollback container is missing")
	}
	if managed.Container.State != nil && managed.Container.State.Running {
		return nil
	}
	_, err = docker.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}

func (replacement gatewayReplacement) commit(ctx context.Context, docker *client.Client) error {
	if err := replacement.persist("committed"); err != nil {
		return err
	}
	return replacement.finalize(ctx, docker)
}

func (replacement gatewayReplacement) finalize(ctx context.Context, docker *client.Client) error {
	for _, id := range []string{replacement.backupID, replacement.legacyID, replacement.layer4ID} {
		if id == "" {
			continue
		}
		if _, err := docker.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("deployer: finalize unified gateway migration: %w", err)
		}
	}
	if replacement.journalPath != "" {
		if err := removeGatewayReplacementJournal(replacement.journalPath); err != nil {
			return fmt.Errorf("deployer: clear committed gateway replacement: %w", err)
		}
	}
	return nil
}

func (replacement gatewayReplacement) persist(phase string) error {
	if replacement.journalPath == "" || replacement.generation == "" || phase != "prepared" && phase != "committed" {
		return errors.New("deployer: gateway replacement journal is incomplete")
	}
	encoded, err := json.Marshal(gatewayReplacementJournal{
		Version: gatewayReplacementJournalVersion, Generation: replacement.generation, Phase: phase,
		CandidateID: replacement.candidateID, BackupID: replacement.backupID, LegacyID: replacement.legacyID, Layer4ID: replacement.layer4ID,
		BackupWasRunning: replacement.backupWasRunning, LegacyWasRunning: replacement.legacyWasRunning, Layer4WasRunning: replacement.layer4WasRunning,
	})
	if err != nil {
		return err
	}
	if err := writeAtomic(replacement.journalPath, encoded, 0o600); err != nil {
		return fmt.Errorf("deployer: persist %s gateway replacement: %w", phase, err)
	}
	if err := syncGatewayReplacementDirectory(replacement.journalPath); err != nil {
		return fmt.Errorf("deployer: sync %s gateway replacement: %w", phase, err)
	}
	return nil
}

func removeGatewayReplacementJournal(path string) error {
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncGatewayReplacementDirectory(path)
}

func syncGatewayReplacementDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func loadGatewayReplacementJournal(path string) (gatewayReplacementJournal, bool, error) {
	encoded, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return gatewayReplacementJournal{}, false, nil
	}
	if err != nil {
		return gatewayReplacementJournal{}, false, fmt.Errorf("deployer: read gateway replacement journal: %w", err)
	}
	var journal gatewayReplacementJournal
	if json.Unmarshal(encoded, &journal) != nil || journal.Version != gatewayReplacementJournalVersion || journal.Generation == "" || journal.Phase != "prepared" && journal.Phase != "committed" {
		return gatewayReplacementJournal{}, false, errors.New("deployer: gateway replacement journal is invalid")
	}
	if journal.Phase == "committed" && journal.CandidateID == "" {
		return gatewayReplacementJournal{}, false, errors.New("deployer: committed gateway replacement has no candidate")
	}
	return journal, true, nil
}

func newGatewayReplacementGeneration() (string, error) {
	value := make([]byte, 18)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("deployer: generate gateway replacement generation: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func gatewayContainerHealthy(current *client.ContainerInspectResult, socket string) bool {
	if current == nil || current.Container.State == nil || !current.Container.State.Running || current.Container.State.Restarting || current.Container.State.Dead {
		return false
	}
	if health := current.Container.State.Health; health != nil && health.Status != container.Healthy {
		return false
	}
	if socket == "" {
		return true
	}
	info, err := os.Stat(socket)
	return err == nil && info.Mode()&os.ModeSocket != 0
}
