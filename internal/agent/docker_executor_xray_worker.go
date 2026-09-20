package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

const xrayWorkerConfigPath = "/etc/vastora/config.json"
const xrayWorkerRuntimeLabel = "io.vastora.proxy-runtime"

const (
	xrayWorkerCandidateContainer = xrayWorkerContainer + "-candidate"
	xrayWorkerBackupContainer    = xrayWorkerContainer + "-rollback"
	xrayWorkerCleanupContainer   = xrayWorkerContainer + "-cleanup"
)

func deployXrayWorker(ctx context.Context, docker *client.Client, dockerSocket string, store *Store, task DeploymentTask, bindAddress string) (string, error) {
	if store == nil {
		return "", errors.New("agent: Xray worker state store is unavailable")
	}
	settings, err := decodeThreeXUIConfig(task.Config)
	if err != nil {
		return "", err
	}
	imageRef, err := declaredImage(task.Manifest, "xray-core")
	if err != nil {
		return "", err
	}
	if imageRef != xrayWorkerImageReference {
		return "", errors.New("agent: declared Xray image does not match the audited worker runtime")
	}
	currentRuntime, _, existingRuntime, err := inspectCurrentXrayWorkerRuntime(ctx, docker)
	if err != nil {
		return "", err
	}
	legacyRuntime := existingRuntime && currentRuntime.Container.Config != nil && currentRuntime.Container.Config.Labels[xrayWorkerRuntimeLabel] != "xray"
	var rollbackState *xrayWorkerState
	if existingRuntime && currentRuntime.Container.Config != nil && currentRuntime.Container.Config.Labels[xrayWorkerRuntimeLabel] == "xray" {
		current, stateErr := store.loadXrayWorkerState(ctx)
		if stateErr != nil {
			return "", stateErr
		}
		observed, observeErr := dockerXrayWorkerObserve(dockerSocket)(ctx, current)
		if observeErr != nil {
			return "", fmt.Errorf("agent: checkpoint Xray traffic before replacement: %w", observeErr)
		}
		if err := store.saveXrayWorkerState(ctx, observed); err != nil {
			return "", fmt.Errorf("agent: persist Xray traffic checkpoint before replacement: %w", err)
		}
		rollbackState = &observed
	}
	state, token, err := prepareXrayWorkerState(ctx, store, task, bindAddress, settings.PanelPort, legacyRuntime)
	if err != nil {
		return "", err
	}
	if existingRuntime && currentRuntime.Container.Config != nil && currentRuntime.Container.HostConfig != nil && currentRuntime.Container.Config.Labels[xrayWorkerRuntimeLabel] == "xray" && currentRuntime.Container.Config.Labels[threeXUIDeploymentIDLabel] == task.ID && currentRuntime.Container.Config.Image == imageRef && state.ImageReference == imageRef && currentRuntime.Container.HostConfig.NetworkMode == container.NetworkMode(dockerruntime.NetworkName) {
		if err := store.saveXrayWorkerState(ctx, state); err != nil {
			return token, uncertainTaskOutcome(err)
		}
		if state.AppliedRevision < state.Revision {
			if err := dockerXrayWorkerApply(store, dockerSocket)(ctx, state, state); err != nil {
				return token, uncertainTaskOutcome(err)
			}
			state.AppliedRevision = state.Revision
			if err := store.saveXrayWorkerState(ctx, state); err != nil {
				return token, uncertainTaskOutcome(err)
			}
		}
		if err := store.startXrayWorkerAPI(state, dockerXrayWorkerApply(store, dockerSocket), dockerXrayWorkerObserve(dockerSocket)); err != nil {
			return token, uncertainTaskOutcome(err)
		}
		if err := waitForThreeXUINodeReady(ctx, bindAddress, settings.PanelPort, token, task.ApplicationID); err != nil {
			return token, uncertainTaskOutcome(err)
		}
		return token, nil
	}
	imageRef, err = pullDeclaredImage(ctx, docker, task, "xray-core")
	if err != nil {
		return "", fmt.Errorf("agent: pull Xray image: %w", err)
	}
	stagedConfig, configPath, err := store.stageXrayWorkerConfig(state)
	if err != nil {
		return "", fmt.Errorf("agent: stage Xray worker configuration: %w", err)
	}
	defer func() { _ = os.Remove(stagedConfig) }()
	if err := validateXrayWorkerConfig(ctx, docker, imageRef, stagedConfig); err != nil {
		return "", err
	}
	if existingRuntime && currentRuntime.Container.Config != nil && currentRuntime.Container.Config.Labels[xrayWorkerRuntimeLabel] != "xray" {
		refreshed, refreshedToken, err := prepareXrayWorkerState(ctx, store, task, bindAddress, settings.PanelPort, true)
		if err != nil || refreshedToken != token {
			return token, errors.Join(errors.New("agent: legacy worker identity changed during migration"), err)
		}
		configChanged, err := xrayWorkerConfigChanged(state, refreshed)
		if err != nil {
			return token, err
		}
		state = refreshed
		if configChanged {
			_ = os.Remove(stagedConfig)
			stagedConfig, configPath, err = store.stageXrayWorkerConfig(state)
			if err != nil {
				return token, fmt.Errorf("agent: restage changed legacy worker configuration: %w", err)
			}
			if err := validateXrayWorkerConfig(ctx, docker, imageRef, stagedConfig); err != nil {
				return token, err
			}
			confirmed, confirmedToken, err := prepareXrayWorkerState(ctx, store, task, bindAddress, settings.PanelPort, true)
			if err != nil || confirmedToken != token {
				return token, errors.Join(errors.New("agent: legacy worker identity changed during final migration check"), err)
			}
			if changedAgain, compareErr := xrayWorkerConfigChanged(state, confirmed); compareErr != nil {
				return token, compareErr
			} else if changedAgain {
				return token, errors.New("agent: legacy worker configuration is still changing; stop concurrent management changes before migration")
			}
			state = confirmed
		}
	}
	if existingRuntime && currentRuntime.Container.Config != nil && currentRuntime.Container.Config.Labels[xrayWorkerRuntimeLabel] == "xray" {
		// Persist the unapplied desired revision immediately before the atomic
		// config commit. Startup recovery can then finish exactly this revision
		// if the Agent or host stops between rename and container promotion.
		if err := store.saveXrayWorkerState(ctx, state); err != nil {
			return token, fmt.Errorf("agent: journal Xray worker desired revision: %w", err)
		}
	}
	if err := commitXrayWorkerConfig(stagedConfig, configPath); err != nil {
		return "", fmt.Errorf("agent: commit Xray worker configuration: %w", err)
	}
	// The encrypted state continues to identify the old runtime until the new
	// candidate has passed validation. This keeps interrupted upgrades
	// recoverable through ResumeXrayWorker instead of making the old, still
	// valid container look like an identity mismatch.
	state.ImageReference = imageRef
	options := xrayWorkerContainerOptions(task, imageRef, configPath, xrayWorkerHY2Enabled(state))
	if err := store.stopXrayWorkerAPI(ctx, false); err != nil {
		return token, err
	}
	resultToken, replaceErr := replaceXrayWorkerContainer(ctx, docker, options, func() error {
		if !legacyRuntime {
			return nil
		}
		latest, latestToken, err := prepareXrayWorkerState(ctx, store, task, bindAddress, settings.PanelPort, true)
		if err != nil || latestToken != token {
			return errors.Join(errors.New("agent: legacy worker identity changed at cutover"), err)
		}
		changed, err := xrayWorkerConfigChanged(state, latest)
		if err != nil {
			return err
		}
		if changed {
			return errors.New("agent: legacy worker configuration changed at cutover; retry after management changes stop")
		}
		state = latest
		return nil
	}, func(containerID string) (string, error) {
		if err := waitForXrayWorkerRuntime(ctx, docker, containerID); err != nil {
			return token, err
		}
		if err := store.recordXrayWorkerApplied(state); err != nil {
			return token, err
		}
		state.AppliedRevision = state.Revision
		if err := store.saveXrayWorkerState(ctx, state); err != nil {
			return token, err
		}
		return token, nil
	}, func(_ string, apiToken string) error {
		if apiToken != token {
			return errors.New("agent: Xray worker token changed during promotion")
		}
		if err := store.startXrayWorkerAPI(state, dockerXrayWorkerApply(store, dockerSocket), dockerXrayWorkerObserve(dockerSocket)); err != nil {
			return err
		}
		return waitForThreeXUINodeReady(ctx, bindAddress, settings.PanelPort, token, task.ApplicationID)
	}, func(recoveryContext context.Context) error {
		stopErr := store.stopXrayWorkerAPI(recoveryContext, false)
		if rollbackState == nil {
			return errors.Join(stopErr, store.retireXrayWorkerState(recoveryContext))
		}
		_, writeErr := store.writeXrayWorkerConfig(*rollbackState)
		receiptErr := store.recordXrayWorkerApplied(*rollbackState)
		stateErr := store.saveXrayWorkerState(recoveryContext, *rollbackState)
		return errors.Join(stopErr, writeErr, receiptErr, stateErr)
	})
	if replaceErr != nil {
		// Replacement rollback uses an uncancelled recovery context to restore
		// the previous container. Reopen its receiver under the same boundary;
		// reusing the failed task context could leave a healthy data plane with
		// no management path after a deadline or cancellation.
		recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return resultToken, errors.Join(replaceErr, store.ResumeXrayWorker(recoveryContext, dockerSocket))
	}
	return resultToken, nil
}

func xrayWorkerContainerOptions(task DeploymentTask, imageRef, configPath string, hy2Enabled bool) client.ContainerCreateOptions {
	pidsLimit := int64(512)
	exposed := dockernetwork.PortSet{dockernetwork.MustParsePort("443/tcp"): struct{}{}}
	bindings := dockernetwork.PortMap{}
	if hy2Enabled {
		exposed[hy2DockerPort] = struct{}{}
		bindings[hy2DockerPort] = []dockernetwork.PortBinding{{HostIP: netip.IPv4Unspecified(), HostPort: "443"}}
	}
	options := client.ContainerCreateOptions{
		Name: xrayWorkerCandidateContainer,
		Config: &container.Config{
			Image:        imageRef,
			Cmd:          []string{"run", "-c", xrayWorkerConfigPath},
			User:         strconv.Itoa(xrayWorkerRuntimeUID()),
			Labels:       applicationResourceLabels(threeXUIKey, "xray", task.ApplicationID, task.ID),
			ExposedPorts: exposed,
		},
		HostConfig: &container.HostConfig{
			RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			LogConfig:      container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3"}},
			NetworkMode:    container.NetworkMode(dockerruntime.NetworkName),
			PortBindings:   bindings,
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			Sysctls:        map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"},
			Resources:      container.Resources{PidsLimit: &pidsLimit},
			Mounts:         []mount.Mount{{Type: mount.TypeBind, Source: filepath.Dir(configPath), Target: filepath.Dir(xrayWorkerConfigPath), ReadOnly: true}},
		},
		NetworkingConfig: dockerruntime.NetworkingConfig(dockerruntime.XrayAlias),
	}
	options.Config.Labels[xrayWorkerRuntimeLabel] = "xray"
	return options
}

func inspectXrayWorkerContainer(ctx context.Context, docker threeXUIContainerEngine, name string) (client.ContainerInspectResult, bool, error) {
	return inspectOwnedApplicationContainer(ctx, docker, name, threeXUIKey, "xray", "", anyApplicationDeployment)
}

// inspectCurrentXrayWorkerRuntime recognizes the old worker name only as an
// explicit, one-way migration source. Newly created and recovered workers use
// the Vastora Xray identity exclusively.
func inspectCurrentXrayWorkerRuntime(ctx context.Context, docker threeXUIContainerEngine) (client.ContainerInspectResult, string, bool, error) {
	current, exists, err := inspectXrayWorkerContainer(ctx, docker, xrayWorkerContainer)
	if err != nil || exists {
		return current, xrayWorkerContainer, exists, err
	}
	legacy, legacyExists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return client.ContainerInspectResult{}, "", false, err
	}
	return legacy, threeXUIContainer, legacyExists, nil
}

func validateXrayWorkerOwnership(ctx context.Context, docker threeXUIContainerEngine, expectedApplicationID string) error {
	applicationID := ""
	for _, name := range []string{xrayWorkerContainer, xrayWorkerCandidateContainer, xrayWorkerBackupContainer, xrayWorkerCleanupContainer} {
		inspected, exists, err := inspectXrayWorkerContainer(ctx, docker, name)
		if err != nil {
			return fmt.Errorf("agent: verify Xray worker ownership: %w", err)
		}
		if !exists {
			continue
		}
		value := inspected.Container.Config.Labels[applicationInstallationLabel]
		if applicationID == "" {
			applicationID = value
		} else if applicationID != value {
			return errors.New("agent: conflicting Xray worker ownership markers")
		}
	}
	legacy, exists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return fmt.Errorf("agent: verify legacy Xray worker ownership: %w", err)
	}
	if exists && legacy.Container.Config != nil {
		value := legacy.Container.Config.Labels[applicationInstallationLabel]
		if applicationID == "" {
			applicationID = value
		} else if applicationID != value {
			return errors.New("agent: legacy Xray worker belongs to another application")
		}
	}
	if expectedApplicationID != "" && applicationID != "" && applicationID != expectedApplicationID {
		return errors.New("agent: refusing to mutate Xray resources owned by another application")
	}
	return nil
}

func requireNoInterruptedXrayWorkerDeploy(ctx context.Context, docker threeXUIContainerEngine) error {
	for _, name := range []string{xrayWorkerCandidateContainer, xrayWorkerBackupContainer, xrayWorkerCleanupContainer} {
		if _, exists, err := inspectXrayWorkerContainer(ctx, docker, name); err != nil {
			return err
		} else if exists {
			return uncertainTaskOutcome(fmt.Errorf("agent: retained Xray replacement %s requires explicit review", name))
		}
	}
	return nil
}

func prepareXrayWorkerKeepDataUninstall(ctx context.Context, docker threeXUIContainerEngine) error {
	for _, name := range []string{xrayWorkerCandidateContainer, xrayWorkerBackupContainer, xrayWorkerCleanupContainer, xrayWorkerContainer} {
		worker, exists, err := inspectXrayWorkerContainer(ctx, docker, name)
		if err != nil {
			return err
		}
		if !exists || worker.Container.State == nil || !worker.Container.State.Running {
			continue
		}
		if _, err := docker.ContainerStop(ctx, worker.Container.ID, client.ContainerStopOptions{}); err != nil && !errdefs.IsNotModified(err) && !errdefs.IsNotFound(err) {
			return uncertainTaskOutcome(fmt.Errorf("agent: stop Xray worker before preserving state: %w", err))
		}
	}
	return nil
}

func replaceXrayWorkerContainer(ctx context.Context, docker threeXUIContainerEngine, options client.ContainerCreateOptions, beforeStop func() error, validate func(string) (string, error), verify func(string, string) error, restoreState func(context.Context) error) (string, error) {
	if options.Config == nil || options.Config.Labels[xrayWorkerRuntimeLabel] != "xray" {
		return "", errors.New("agent: Xray worker candidate identity is missing")
	}
	applicationID := options.Config.Labels[applicationInstallationLabel]
	if err := validateXrayWorkerOwnership(ctx, docker, applicationID); err != nil {
		return "", err
	}
	if err := requireNoInterruptedXrayWorkerDeploy(ctx, docker); err != nil {
		return "", err
	}
	previous, previousName, previousExists, err := inspectCurrentXrayWorkerRuntime(ctx, docker)
	if err != nil {
		return "", err
	}
	created, err := docker.ContainerCreate(ctx, options)
	if err != nil {
		return "", fmt.Errorf("agent: create Xray worker candidate: %w", err)
	}
	candidateID := created.ID
	previousRunning := previousExists && previous.Container.State != nil && previous.Container.State.Running
	rollback := func(result string, cause error) (string, error) {
		recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		var recoveryErr error
		if _, err := docker.ContainerRemove(recoveryContext, candidateID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("remove failed Xray worker candidate: %w", err))
			return result, uncertainTaskOutcome(errors.Join(cause, recoveryErr))
		}
		if restoreState != nil {
			recoveryErr = errors.Join(recoveryErr, restoreState(recoveryContext))
		}
		if previousExists {
			current, inspectErr := docker.ContainerInspect(recoveryContext, previous.Container.ID, client.ContainerInspectOptions{})
			if inspectErr != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("inspect previous proxy worker during rollback: %w", inspectErr))
			} else if strings.TrimPrefix(current.Container.Name, "/") != previousName {
				if _, renameErr := docker.ContainerRename(recoveryContext, previous.Container.ID, client.ContainerRenameOptions{NewName: previousName}); renameErr != nil {
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("restore previous proxy worker name: %w", renameErr))
				}
			}
			if previousRunning {
				if _, startErr := docker.ContainerStart(recoveryContext, previous.Container.ID, client.ContainerStartOptions{}); startErr != nil && !errdefs.IsNotModified(startErr) {
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("restart previous proxy worker: %w", startErr))
				}
			}
		}
		return result, uncertainTaskOutcome(errors.Join(cause, recoveryErr))
	}
	if beforeStop != nil {
		if err := beforeStop(); err != nil {
			return rollback("", err)
		}
	}
	if previousRunning {
		timeout := 10
		if _, err := docker.ContainerStop(ctx, previous.Container.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
			return rollback("", fmt.Errorf("agent: stop previous proxy worker: %w", err))
		}
	}
	// The first cutover preserves the legacy database both in its volume and in
	// the durable snapshot. Later Xray-only upgrades never fabricate or inspect
	// a panel database volume.
	if previousExists && previous.Container.Config != nil && previous.Container.Config.Labels[xrayWorkerRuntimeLabel] != "xray" {
		snapshot, snapshotErr := snapshotThreeXUIDatabase(ctx, docker, previous.Container.ID)
		if snapshotErr != nil {
			return rollback("", fmt.Errorf("agent: snapshot legacy worker database: %w", snapshotErr))
		}
		if len(snapshot) != 0 {
			if err := persistThreeXUIDatabaseSnapshot(ctx, docker, previous.Container.ID, snapshot); err != nil {
				return rollback("", fmt.Errorf("agent: persist legacy worker rollback: %w", err))
			}
		}
	}
	if previousExists {
		if _, err := docker.ContainerRename(ctx, previous.Container.ID, client.ContainerRenameOptions{NewName: xrayWorkerBackupContainer}); err != nil {
			return rollback("", err)
		}
	}
	if _, err := docker.ContainerStart(ctx, candidateID, client.ContainerStartOptions{}); err != nil {
		return rollback("", fmt.Errorf("agent: start Xray worker candidate: %w", err))
	}
	result, err := validate(candidateID)
	if err != nil {
		return rollback(result, err)
	}
	inspected, err := docker.ContainerInspect(ctx, candidateID, client.ContainerInspectOptions{})
	if err != nil || inspected.Container.State == nil || !inspected.Container.State.Running {
		return rollback(result, errors.Join(errors.New("agent: Xray worker candidate did not remain running"), err))
	}
	if _, err := docker.ContainerRename(ctx, candidateID, client.ContainerRenameOptions{NewName: xrayWorkerContainer}); err != nil {
		return rollback(result, err)
	}
	if err := verify(candidateID, result); err != nil {
		return rollback(result, err)
	}
	if previousExists {
		if _, err := docker.ContainerRename(ctx, previous.Container.ID, client.ContainerRenameOptions{NewName: xrayWorkerCleanupContainer}); err != nil {
			return result, uncertainTaskOutcome(err)
		}
		if _, err := docker.ContainerRemove(ctx, previous.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
			return result, uncertainTaskOutcome(err)
		}
	}
	return result, nil
}

func prepareXrayWorkerState(ctx context.Context, store *Store, task DeploymentTask, address string, panelPort int, requireImport bool) (xrayWorkerState, string, error) {
	current, loadErr := store.loadXrayWorkerState(ctx)
	if loadErr == nil {
		if current.ApplicationID != task.ApplicationID {
			return xrayWorkerState{}, "", errors.New("agent: Xray worker belongs to another application")
		}
		previous := current
		current.Address, current.PanelPort = address, panelPort
		if requireImport {
			fingerprint, err := legacyXrayWorkerFingerprint(ctx, current)
			if err != nil {
				return xrayWorkerState{}, "", err
			}
			if current.LegacyImportSHA256 == "" || fingerprint != current.LegacyImportSHA256 {
				return xrayWorkerState{}, "", errors.New("agent: legacy worker configuration changed after migration was journaled; explicit reconciliation is required")
			}
		}
		current = reconcileXrayWorkerAccounts(current, store.now().UnixMilli())
		changed, err := xrayWorkerConfigChanged(previous, current)
		if err != nil {
			return xrayWorkerState{}, "", err
		}
		if changed {
			current.Revision++
		}
		return current, current.APIToken, current.validate()
	}
	if !errors.Is(loadErr, errApplicationNotInstalled) {
		return xrayWorkerState{}, "", loadErr
	}
	var secrets map[string]string
	_ = json.Unmarshal(task.Secrets, &secrets)
	token := strings.TrimSpace(secrets["api_token"])
	if requireImport && token == "" {
		return xrayWorkerState{}, "", errors.New("agent: existing worker API token is unavailable; refusing a state-less cutover")
	}
	if token == "" {
		var err error
		token, err = randomClientToken()
		if err != nil {
			return xrayWorkerState{}, "", err
		}
	}
	state := xrayWorkerState{ApplicationID: task.ApplicationID, ImageReference: xrayWorkerImageReference, Address: address, PanelPort: panelPort, APIToken: token, Revision: 1, NextInboundID: 1, XraySetting: defaultXrayWorkerSettings()}
	// Upgrade in place by importing the old panel's effective state before the
	// transactional container replacement stops it. A fresh worker legitimately
	// has no old endpoint and starts with an empty inbound set.
	if !requireImport {
		if task.Operation != "install" {
			return xrayWorkerState{}, "", errors.New("agent: Xray worker state and runtime are missing; refusing a state-less upgrade")
		}
		state = reconcileXrayWorkerAccounts(state, store.now().UnixMilli())
		return state, token, state.validate()
	}
	baseURL := "http://" + net.JoinHostPort(address, strconv.Itoa(panelPort))
	if payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil); err == nil {
		var values []json.RawMessage
		if json.Unmarshal(payload, &values) != nil {
			return xrayWorkerState{}, "", errors.New("agent: cannot import legacy worker inbounds")
		}
		state.Inbounds = values
		for _, raw := range values {
			if id := inboundID(raw); id >= state.NextInboundID {
				state.NextInboundID = id + 1
			}
		}
		if payload, readErr := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/", token, "application/json", map[string]any{}); readErr == nil {
			var nested string
			var settings struct {
				XraySetting json.RawMessage `json:"xraySetting"`
			}
			if json.Unmarshal(payload, &nested) != nil || json.Unmarshal([]byte(nested), &settings) != nil || len(settings.XraySetting) == 0 {
				return xrayWorkerState{}, "", errors.New("agent: cannot import legacy worker routing")
			}
			state.XraySetting = settings.XraySetting
		} else if requireImport {
			return xrayWorkerState{}, "", errors.New("agent: cannot read legacy worker routing; refusing cutover")
		}
	} else if requireImport {
		return xrayWorkerState{}, "", errors.New("agent: cannot read legacy worker inbounds; refusing cutover")
	}
	state = reconcileXrayWorkerAccounts(state, store.now().UnixMilli())
	fingerprint, err := xrayWorkerMigrationFingerprint(state.Inbounds, state.XraySetting)
	if err != nil {
		return xrayWorkerState{}, "", err
	}
	state.LegacyImportSHA256 = fingerprint
	return state, token, state.validate()
}

func legacyXrayWorkerFingerprint(ctx context.Context, state xrayWorkerState) (string, error) {
	baseURL := "http://" + net.JoinHostPort(state.Address, strconv.Itoa(state.PanelPort))
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", state.APIToken, "", nil)
	if err != nil {
		return "", errors.New("agent: cannot verify the restored legacy worker inbounds")
	}
	var inbounds []json.RawMessage
	if json.Unmarshal(payload, &inbounds) != nil {
		return "", errors.New("agent: restored legacy worker inbounds are invalid")
	}
	payload, err = threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/xray/", state.APIToken, "application/json", map[string]any{})
	if err != nil {
		return "", errors.New("agent: cannot verify the restored legacy worker routing")
	}
	var nested string
	var settings struct {
		XraySetting json.RawMessage `json:"xraySetting"`
	}
	if json.Unmarshal(payload, &nested) != nil || json.Unmarshal([]byte(nested), &settings) != nil || len(settings.XraySetting) == 0 {
		return "", errors.New("agent: restored legacy worker routing is invalid")
	}
	return xrayWorkerMigrationFingerprint(inbounds, settings.XraySetting)
}

func defaultXrayWorkerSettings() json.RawMessage {
	return json.RawMessage(`{"log":{"loglevel":"warning"},"outbounds":[{"protocol":"freedom","tag":"direct"},{"protocol":"blackhole","tag":"blocked"}],"policy":{"levels":{"0":{"statsUserUplink":true,"statsUserDownlink":true}},"system":{"statsInboundUplink":true,"statsInboundDownlink":true}},"routing":{"domainStrategy":"IPIfNonMatch","rules":[]},"stats":{}}`)
}

func waitForXrayWorkerRuntime(ctx context.Context, docker *client.Client, containerID string) error {
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := runXrayWorkerCommand(ctx, docker, containerID, []string{"xray", "api", "statsquery", "--server=127.0.0.1:10085", "-pattern", "", "-reset=false"}); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("agent: Xray worker did not become ready")
		case <-ticker.C:
		}
	}
}

func runXrayWorkerCommand(ctx context.Context, docker *client.Client, containerID string, command []string) ([]byte, error) {
	execution, err := docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{TTY: true, AttachStdout: true, AttachStderr: true, Cmd: command})
	if err != nil {
		return nil, err
	}
	attached, err := docker.ExecAttach(ctx, execution.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		return nil, err
	}
	defer attached.Close()
	output, err := io.ReadAll(io.LimitReader(attached.Reader, (4<<20)+1))
	if err != nil || len(output) > 4<<20 {
		return nil, errors.New("agent: Xray command response is invalid")
	}
	inspection, err := docker.ExecInspect(ctx, execution.ID, client.ExecInspectOptions{})
	if err != nil {
		return nil, err
	}
	if inspection.Running || inspection.ExitCode != 0 {
		return nil, fmt.Errorf("agent: Xray command exited with status %d", inspection.ExitCode)
	}
	return bytes.TrimSpace(output), nil
}

func dockerXrayWorkerApply(store *Store, dockerSocket string) xrayWorkerApply {
	return func(ctx context.Context, previous, state xrayWorkerState) error {
		if previous.Revision == previous.AppliedRevision && state.Revision == previous.Revision {
			changed, err := xrayWorkerConfigChanged(previous, state)
			if err != nil {
				return err
			}
			if !changed {
				return nil
			}
		}
		socket := dockerSocket
		if socket == "" {
			socket = "unix:///var/run/docker.sock"
		}
		docker, err := client.New(client.WithHost(socket))
		if err != nil {
			return err
		}
		defer docker.Close()
		inspected, _, exists, err := inspectCurrentXrayWorkerRuntime(ctx, docker)
		if err != nil || !exists {
			return errors.Join(errors.New("agent: Xray worker container is unavailable"), err)
		}
		if inspected.Container.Config == nil || inspected.Container.Config.Labels[xrayWorkerRuntimeLabel] != "xray" || inspected.Container.Config.Image != state.ImageReference {
			return errors.New("agent: Xray worker image identity is unavailable")
		}
		staged, active, err := store.stageXrayWorkerConfig(state)
		if err != nil {
			return err
		}
		defer func() { _ = os.Remove(staged) }()
		if err := validateXrayWorkerConfig(ctx, docker, inspected.Container.Config.Image, staged); err != nil {
			return err
		}
		if err := commitXrayWorkerConfig(staged, active); err != nil {
			return fmt.Errorf("agent: commit Xray worker configuration: %w", err)
		}
		timeout := 15
		restartErr := error(nil)
		if _, err := docker.ContainerRestart(ctx, inspected.Container.ID, client.ContainerRestartOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotModified(err) {
			restartErr = fmt.Errorf("agent: restart Xray worker: %w", err)
		} else {
			restartErr = waitForXrayWorkerRuntime(ctx, docker, inspected.Container.ID)
		}
		if restartErr == nil {
			return store.recordXrayWorkerApplied(state)
		}
		_, restoreWriteErr := store.writeXrayWorkerConfig(previous)
		_, restoreRestartErr := docker.ContainerRestart(ctx, inspected.Container.ID, client.ContainerRestartOptions{Timeout: &timeout})
		return errors.Join(restartErr, restoreWriteErr, restoreRestartErr)
	}
}

// validateXrayWorkerConfig invokes the exact declared Xray image without
// network access. No active configuration is replaced until this exits zero.
func validateXrayWorkerConfig(ctx context.Context, docker *client.Client, imageRef, stagedPath string) error {
	if strings.TrimSpace(imageRef) == "" || filepath.Base(stagedPath) == "" {
		return errors.New("agent: Xray configuration validation identity is missing")
	}
	pidsLimit := int64(128)
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: imageRef, Cmd: []string{"run", "-test", "-c", filepath.Join(filepath.Dir(xrayWorkerConfigPath), filepath.Base(stagedPath))}, User: strconv.Itoa(xrayWorkerRuntimeUID())},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode("none"),
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges:true"},
			Resources:      container.Resources{PidsLimit: &pidsLimit},
			Mounts:         []mount.Mount{{Type: mount.TypeBind, Source: filepath.Dir(stagedPath), Target: filepath.Dir(xrayWorkerConfigPath), ReadOnly: true}},
		},
	})
	if err != nil {
		return fmt.Errorf("agent: create Xray configuration validator: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_, _ = docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	wait := docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("agent: start Xray configuration validator: %w", err)
	}
	select {
	case err := <-wait.Error:
		return fmt.Errorf("agent: wait for Xray configuration validation: %w", err)
	case result := <-wait.Result:
		if result.Error != nil || result.StatusCode != 0 {
			return errors.New("agent: Xray rejected the candidate configuration")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Store) ResumeXrayWorker(ctx context.Context, dockerSocket string) error {
	state, err := s.loadXrayWorkerState(ctx)
	if errors.Is(err, errApplicationNotInstalled) {
		return nil
	}
	if err != nil {
		return err
	}
	installation, err := s.AppliedInstallation(ctx, threeXUIKey)
	if errors.Is(err, errApplicationNotInstalled) {
		// A keep-data uninstall intentionally retains the encrypted worker state
		// for explicit recovery, but it must not resurrect an uninstalled runtime.
		return nil
	}
	if err != nil {
		return err
	}
	if installation.ApplicationRole != "worker" {
		return nil
	}
	if installation.ApplicationID != state.ApplicationID {
		return errors.New("agent: Xray worker recovery state belongs to another application")
	}
	socket := dockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	state, err = s.adoptRecreatedXrayWorkerRuntime(ctx, socket, state)
	if err != nil {
		return err
	}
	if state.AppliedRevision < state.Revision {
		if s.xrayWorkerAppliedReceiptMatches(state) {
			observed, observeErr := dockerXrayWorkerObserve(socket)(ctx, state)
			if observeErr != nil {
				return observeErr
			}
			state = observed
		} else {
			if err := dockerXrayWorkerApply(s, socket)(ctx, state, state); err != nil {
				return err
			}
		}
		state.AppliedRevision = state.Revision
		if err := s.saveXrayWorkerState(ctx, state); err != nil {
			return err
		}
	}
	apply := dockerXrayWorkerApply(s, socket)
	observe := dockerXrayWorkerObserve(socket)
	// An applied database revision alone does not prove the named Xray runtime
	// still exists. Confirm and checkpoint it before reopening the receiver.
	if err := s.reconcileXrayWorkerRuntime(ctx, apply, observe); err != nil {
		return err
	}
	state, err = s.loadXrayWorkerState(ctx)
	if err != nil {
		return err
	}
	return s.startXrayWorkerAPI(state, apply, observe)
}

// adoptRecreatedXrayWorkerRuntime repairs only the image identity projected in
// encrypted state. The active configuration receipt, application ownership,
// audited image, hardened bridge and dedicated runtime user must all still
// match before startup recovery may advance that identity.
func (s *Store) adoptRecreatedXrayWorkerRuntime(ctx context.Context, dockerSocket string, state xrayWorkerState) (xrayWorkerState, error) {
	docker, err := client.New(client.WithHost(dockerSocket))
	if err != nil {
		return state, err
	}
	defer docker.Close()
	inspected, _, exists, err := inspectCurrentXrayWorkerRuntime(ctx, docker)
	if err != nil || !exists {
		return state, errors.Join(errors.New("agent: Xray worker container is unavailable"), err)
	}
	updated, changed, err := xrayWorkerRecreatedRuntimeState(state, inspected)
	if err != nil || !changed {
		return updated, err
	}
	if !s.xrayWorkerAppliedReceiptMatches(state) {
		return state, errors.New("agent: recreated Xray worker configuration requires explicit reconciliation")
	}
	if err := s.saveXrayWorkerState(ctx, updated); err != nil {
		return state, err
	}
	return updated, nil
}

func xrayWorkerRecreatedRuntimeState(state xrayWorkerState, inspected client.ContainerInspectResult) (xrayWorkerState, bool, error) {
	if inspected.Container.Config == nil || inspected.Container.HostConfig == nil || inspected.Container.State == nil {
		return state, false, errors.New("agent: managed Xray worker runtime is unavailable")
	}
	if inspected.Container.Config.Image == state.ImageReference {
		return state, false, nil
	}
	labels := inspected.Container.Config.Labels
	uidText := strings.SplitN(inspected.Container.Config.User, ":", 2)[0]
	uid, uidErr := strconv.Atoi(uidText)
	if labels[xrayWorkerRuntimeLabel] != "xray" || labels[applicationInstallationLabel] != state.ApplicationID || inspected.Container.Config.Image != xrayWorkerImageReference || !inspected.Container.State.Running || string(inspected.Container.HostConfig.NetworkMode) != dockerruntime.NetworkName || uidErr != nil || uid != xrayWorkerRuntimeUID() {
		return state, false, errors.New("agent: recreated Xray worker runtime identity changed")
	}
	updated := state
	updated.ImageReference = inspected.Container.Config.Image
	if err := updated.validate(); err != nil {
		return state, false, err
	}
	return updated, true, nil
}

func dockerXrayWorkerObserve(dockerSocket string) xrayWorkerObserve {
	return func(ctx context.Context, state xrayWorkerState) (xrayWorkerState, error) {
		socket := dockerSocket
		if socket == "" {
			socket = "unix:///var/run/docker.sock"
		}
		docker, err := client.New(client.WithHost(socket))
		if err != nil {
			return state, err
		}
		defer docker.Close()
		inspected, _, exists, err := inspectCurrentXrayWorkerRuntime(ctx, docker)
		if err != nil || !exists {
			return state, errors.Join(errors.New("agent: Xray worker container is unavailable"), err)
		}
		if inspected.Container.Config == nil || inspected.Container.Config.Labels[xrayWorkerRuntimeLabel] != "xray" || inspected.Container.Config.Image != state.ImageReference || inspected.Container.State == nil || !inspected.Container.State.Running {
			return state, errors.New("agent: managed Xray worker runtime is not active")
		}
		output, err := runXrayWorkerCommand(ctx, docker, inspected.Container.ID, []string{"xray", "api", "statsquery", "--server=127.0.0.1:10085", "-pattern", "", "-reset=false"})
		if err != nil {
			return state, err
		}
		return applyXrayWorkerStats(state, output)
	}
}

func applyXrayWorkerStats(state xrayWorkerState, encoded []byte) (xrayWorkerState, error) {
	var payload struct {
		Stat []struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		} `json:"stat"`
	}
	if json.Unmarshal(encoded, &payload) != nil {
		return state, errors.New("agent: Xray statistics response is invalid")
	}
	values := map[string]int64{}
	for _, stat := range payload.Stat {
		var number int64
		if json.Unmarshal(stat.Value, &number) != nil {
			var text string
			if json.Unmarshal(stat.Value, &text) != nil {
				continue
			}
			number, _ = strconv.ParseInt(text, 10, 64)
		}
		if number >= 0 {
			values[stat.Name] = number
		}
	}
	if state.RuntimeStats == nil {
		state.RuntimeStats = map[string]int64{}
	}
	deltas := map[string]int64{}
	for name, current := range values {
		previous := state.RuntimeStats[name]
		if current >= previous {
			deltas[name] = current - previous
		} else {
			// Xray was restarted. The persisted cumulative value remains the
			// checkpoint; the new runtime counter starts another delta window.
			deltas[name] = current
		}
		state.RuntimeStats[name] = current
	}
	userTraffic := map[string]map[string]int64{}
	for name, value := range deltas {
		const prefix = "user>>>"
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		for _, direction := range []string{"uplink", "downlink"} {
			suffix := ">>>traffic>>>" + direction
			email := strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix)
			if email == strings.TrimPrefix(name, prefix) || strings.TrimSpace(email) == "" {
				continue
			}
			if userTraffic[email] == nil {
				userTraffic[email] = map[string]int64{}
			}
			userTraffic[email][direction] = value
		}
	}
	if state.AccountStats == nil {
		state.AccountStats = xrayWorkerAccountStats(state)
	}
	for email, traffic := range userTraffic {
		cumulative := state.AccountStats[email]
		var err error
		if cumulative.Up, err = addXrayWorkerCounter(cumulative.Up, traffic["uplink"]); err != nil {
			return state, err
		}
		if cumulative.Down, err = addXrayWorkerCounter(cumulative.Down, traffic["downlink"]); err != nil {
			return state, err
		}
		state.AccountStats[email] = cumulative
	}
	for index, raw := range state.Inbounds {
		var inbound map[string]any
		_ = json.Unmarshal(raw, &inbound)
		tag, _ := inbound["tag"].(string)
		up, _ := jsonInteger64(inbound["up"])
		down, _ := jsonInteger64(inbound["down"])
		up, err := addXrayWorkerCounter(up, deltas["inbound>>>"+tag+">>>traffic>>>uplink"])
		if err != nil {
			return state, err
		}
		down, err = addXrayWorkerCounter(down, deltas["inbound>>>"+tag+">>>traffic>>>downlink"])
		if err != nil {
			return state, err
		}
		inbound["up"], inbound["down"] = up, down
		settings, _ := inbound["settings"].(map[string]any)
		clients, _ := settings["clients"].([]any)
		clientStats := make([]any, 0, len(clients))
		for _, rawClient := range clients {
			client, _ := rawClient.(map[string]any)
			email, _ := client["email"].(string)
			cumulative := state.AccountStats[email]
			clientStats = append(clientStats, map[string]any{"email": email, "up": cumulative.Up, "down": cumulative.Down})
		}
		inbound["clientStats"] = clientStats
		state.Inbounds[index], _ = json.Marshal(inbound)
	}
	return state, nil
}

func addXrayWorkerCounter(current, delta int64) (int64, error) {
	const maxInt64 = int64(^uint64(0) >> 1)
	if current < 0 || delta < 0 || delta > maxInt64-current {
		return 0, errors.New("agent: Xray traffic counter overflow")
	}
	return current + delta, nil
}

func jsonInteger64(value any) (int64, bool) {
	switch number := value.(type) {
	case float64:
		integer := int64(number)
		return integer, number == float64(integer)
	case json.Number:
		integer, err := strconv.ParseInt(number.String(), 10, 64)
		return integer, err == nil
	case int64:
		return number, true
	case int:
		return int64(number), true
	default:
		return 0, false
	}
}
