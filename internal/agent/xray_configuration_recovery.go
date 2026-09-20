package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/xrayrecovery"
)

func (e ApplicationExecutor) InspectXrayConfiguration(ctx context.Context, task xrayrecovery.Task) (xrayrecovery.Result, error) {
	if e.Store == nil || task.Validate(xrayrecovery.InspectKind) != nil {
		return xrayrecovery.Result{}, errors.New("agent: invalid Xray configuration inspection")
	}
	e.Store.xrayWorkerStateMu.Lock()
	defer e.Store.xrayWorkerStateMu.Unlock()
	state, active, docker, err := e.xrayRecoverySnapshot(ctx, task.ApplicationID)
	if err != nil {
		return xrayrecovery.Result{}, err
	}
	defer docker.Close()
	return inspectXrayConfiguration(state, active)
}

func (e ApplicationExecutor) ApplyXrayConfigurationRecovery(ctx context.Context, task xrayrecovery.Task) (xrayrecovery.Result, error) {
	if e.Store == nil || task.Validate(xrayrecovery.ApplyKind) != nil {
		return xrayrecovery.Result{}, errors.New("agent: invalid Xray configuration recovery")
	}
	e.Store.xrayWorkerStateMu.Lock()
	defer e.Store.xrayWorkerStateMu.Unlock()
	state, active, docker, err := e.xrayRecoverySnapshot(ctx, task.ApplicationID)
	if err != nil {
		return xrayrecovery.Result{}, err
	}
	defer docker.Close()
	inspection, err := inspectXrayConfiguration(state, active)
	if err != nil {
		return xrayrecovery.Result{}, err
	}
	if inspection.RuntimeSHA256 != task.ExpectedRuntimeSHA256 || inspection.AgentSHA256 != task.ExpectedAgentSHA256 || inspection.AgentRevision != task.ExpectedAgentRevision {
		return xrayrecovery.Result{}, errors.New("agent: Xray configuration changed after inspection; inspect again before recovery")
	}
	if task.Action == xrayrecovery.RuntimeSource {
		if !inspection.RuntimeImportable {
			return xrayrecovery.Result{}, errors.New("agent: running Xray configuration cannot be represented by Agent state")
		}
		candidate, err := xrayWorkerStateFromRuntime(state, active)
		if err != nil {
			return xrayrecovery.Result{}, err
		}
		candidate.Revision = state.Revision + 1
		candidate.AppliedRevision = state.AppliedRevision
		if err := e.Store.saveXrayWorkerState(ctx, candidate); err != nil {
			return xrayrecovery.Result{}, err
		}
		normalized, err := renderXrayWorkerConfig(candidate)
		if err != nil {
			return xrayrecovery.Result{}, uncertainTaskOutcome(err)
		}
		if err := e.Store.writeExactXrayWorkerConfig(normalized); err != nil {
			return xrayrecovery.Result{}, uncertainTaskOutcome(err)
		}
		if err := e.Store.recordXrayWorkerApplied(candidate); err != nil {
			return xrayrecovery.Result{}, uncertainTaskOutcome(err)
		}
		candidate.AppliedRevision = candidate.Revision
		if err := e.Store.saveXrayWorkerState(ctx, candidate); err != nil {
			return xrayrecovery.Result{}, uncertainTaskOutcome(err)
		}
		state = candidate
	} else {
		if err := e.applyAgentXrayState(ctx, docker, state, active); err != nil {
			return xrayrecovery.Result{}, err
		}
		state.AppliedRevision = state.Revision
		if err := e.Store.saveXrayWorkerState(ctx, state); err != nil {
			return xrayrecovery.Result{}, uncertainTaskOutcome(err)
		}
	}
	if err := e.Store.startXrayWorkerAPI(state, dockerXrayWorkerApply(e.Store, e.DockerSocket), dockerXrayWorkerObserve(e.DockerSocket)); err != nil {
		return xrayrecovery.Result{}, uncertainTaskOutcome(err)
	}
	active, err = os.ReadFile(filepath.Join(e.Store.dataDir, "xray-worker", "config.json"))
	if err != nil {
		return xrayrecovery.Result{}, uncertainTaskOutcome(err)
	}
	result, err := inspectXrayConfiguration(state, active)
	if err != nil {
		return xrayrecovery.Result{}, uncertainTaskOutcome(err)
	}
	if !result.Matches {
		return xrayrecovery.Result{}, uncertainTaskOutcome(errors.New("agent: Xray recovery did not converge"))
	}
	result.Action, result.AppliedSource = task.Action, task.Action
	e.Store.clearApplicationRecovery(threeXUIKey)
	return result, nil
}

func (e ApplicationExecutor) xrayRecoverySnapshot(ctx context.Context, applicationID string) (xrayWorkerState, []byte, *client.Client, error) {
	state, err := e.Store.loadXrayWorkerState(ctx)
	if err != nil || state.ApplicationID != applicationID {
		return xrayWorkerState{}, nil, nil, errors.Join(errors.New("agent: Xray recovery state does not match the application"), err)
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return xrayWorkerState{}, nil, nil, err
	}
	inspected, name, exists, err := inspectCurrentXrayWorkerRuntime(ctx, docker)
	if err != nil || !exists || name != xrayWorkerContainer || inspected.Container.Config == nil || inspected.Container.HostConfig == nil || inspected.Container.State == nil || !inspected.Container.State.Running {
		docker.Close()
		return xrayWorkerState{}, nil, nil, errors.Join(errors.New("agent: managed Xray runtime is unavailable"), err)
	}
	labels := inspected.Container.Config.Labels
	image := inspected.Container.Config.Image
	if labels[xrayWorkerRuntimeLabel] != "xray" || labels[applicationInstallationLabel] != applicationID || image != state.ImageReference && image != xrayWorkerImageReference || inspected.Container.Config.User != strconv.Itoa(xrayWorkerRuntimeUID()) || inspected.Container.HostConfig.NetworkMode != container.NetworkMode(dockerruntime.NetworkName) {
		docker.Close()
		return xrayWorkerState{}, nil, nil, errors.New("agent: managed Xray runtime identity is invalid")
	}
	// A transactional container replacement may have reached the audited image
	// before the encrypted state and receipt were advanced. Recovery must be
	// able to compare those two configurations without silently adopting the
	// new image outside the operator-authorized recovery task.
	state.ImageReference = image
	active, err := os.ReadFile(filepath.Join(e.Store.dataDir, "xray-worker", "config.json"))
	if err != nil || len(active) == 0 || len(active) > xrayWorkerMaxBody {
		docker.Close()
		return xrayWorkerState{}, nil, nil, errors.Join(errors.New("agent: active Xray configuration is unavailable"), err)
	}
	return state, active, docker, nil
}

func inspectXrayConfiguration(state xrayWorkerState, active []byte) (xrayrecovery.Result, error) {
	desired, err := renderXrayWorkerConfig(state)
	if err != nil {
		return xrayrecovery.Result{}, err
	}
	runtimeHash, err := canonicalXrayConfigSHA256(active)
	if err != nil {
		return xrayrecovery.Result{}, errors.New("agent: active Xray configuration is invalid")
	}
	agentHash, err := canonicalXrayConfigSHA256(desired)
	if err != nil {
		return xrayrecovery.Result{}, err
	}
	runtimeInbounds, err := summarizeXrayInbounds(active)
	if err != nil {
		return xrayrecovery.Result{}, err
	}
	agentInbounds, err := summarizeXrayInbounds(desired)
	if err != nil {
		return xrayrecovery.Result{}, err
	}
	_, importErr := xrayWorkerStateFromRuntime(state, active)
	result := xrayrecovery.Result{Action: "inspect", ApplicationID: state.ApplicationID, RuntimeSHA256: runtimeHash, AgentSHA256: agentHash, AgentRevision: state.Revision, RuntimeImportable: importErr == nil, Matches: runtimeHash == agentHash, RuntimeInbounds: runtimeInbounds, AgentInbounds: agentInbounds}
	result.Differences = xrayConfigurationDifferences(runtimeInbounds, agentInbounds, runtimeHash != agentHash)
	return result, nil
}

func canonicalXrayConfigSHA256(raw []byte) (string, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil || value == nil {
		return "", errors.New("invalid JSON")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

func summarizeXrayInbounds(raw []byte) ([]xrayrecovery.InboundSummary, error) {
	var config struct {
		Inbounds []map[string]any `json:"inbounds"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&config) != nil {
		return nil, errors.New("agent: invalid Xray configuration")
	}
	values := make([]xrayrecovery.InboundSummary, 0, len(config.Inbounds))
	for _, inbound := range config.Inbounds {
		tag, _ := inbound["tag"].(string)
		if tag == "api" {
			continue
		}
		protocol, _ := inbound["protocol"].(string)
		port, _ := jsonInteger(inbound["port"])
		settings, _ := inbound["settings"].(map[string]any)
		clients, _ := settings["clients"].([]any)
		values = append(values, xrayrecovery.InboundSummary{Tag: tag, Protocol: protocol, Port: port, Clients: len(clients)})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Tag < values[j].Tag })
	return values, nil
}

func xrayConfigurationDifferences(runtime, agent []xrayrecovery.InboundSummary, configChanged bool) []xrayrecovery.Difference {
	left, right := map[string]xrayrecovery.InboundSummary{}, map[string]xrayrecovery.InboundSummary{}
	for _, value := range runtime {
		left[value.Tag] = value
	}
	for _, value := range agent {
		right[value.Tag] = value
	}
	keys := make([]string, 0, len(left)+len(right))
	for key := range left {
		keys = append(keys, key)
	}
	for key := range right {
		if _, exists := left[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	differences := []xrayrecovery.Difference{}
	for _, key := range keys {
		a, aOK := left[key]
		b, bOK := right[key]
		if !aOK {
			differences = append(differences, xrayrecovery.Difference{Kind: "missing_runtime", Inbound: key})
			continue
		}
		if !bOK {
			differences = append(differences, xrayrecovery.Difference{Kind: "missing_agent", Inbound: key})
			continue
		}
		for _, field := range []struct{ name, a, b string }{{"protocol", a.Protocol, b.Protocol}, {"port", strconv.Itoa(a.Port), strconv.Itoa(b.Port)}, {"clients", strconv.Itoa(a.Clients), strconv.Itoa(b.Clients)}} {
			if field.a != field.b {
				differences = append(differences, xrayrecovery.Difference{Kind: "changed", Inbound: key, Field: field.name, Runtime: field.a, Agent: field.b})
			}
		}
	}
	if configChanged && len(differences) == 0 {
		differences = append(differences, xrayrecovery.Difference{Kind: "settings", Field: "routing_or_policy"})
	}
	return differences
}

func xrayWorkerStateFromRuntime(previous xrayWorkerState, active []byte) (xrayWorkerState, error) {
	var config map[string]any
	decoder := json.NewDecoder(bytes.NewReader(active))
	decoder.UseNumber()
	if decoder.Decode(&config) != nil || config == nil {
		return xrayWorkerState{}, errors.New("agent: active Xray configuration is invalid")
	}
	rawInbounds, ok := config["inbounds"].([]any)
	if !ok {
		return xrayWorkerState{}, errors.New("agent: active Xray inbounds are unavailable")
	}
	delete(config, "inbounds")
	inbounds := []json.RawMessage{}
	nextID := 1
	for _, raw := range rawInbounds {
		inbound, ok := raw.(map[string]any)
		if !ok {
			return xrayWorkerState{}, errors.New("agent: active Xray inbound is invalid")
		}
		tag, _ := inbound["tag"].(string)
		if tag == "api" {
			continue
		}
		inbound["id"], inbound["enable"], inbound["remark"] = nextID, true, tag
		encoded, err := json.Marshal(inbound)
		if err != nil {
			return xrayWorkerState{}, err
		}
		inbounds = append(inbounds, encoded)
		nextID++
	}
	settings, err := json.Marshal(config)
	if err != nil {
		return xrayWorkerState{}, err
	}
	candidate := previous
	candidate.Inbounds, candidate.NextInboundID, candidate.XraySetting = inbounds, nextID, settings
	candidate.LegacyImportSHA256 = ""
	if err := candidate.validate(); err != nil {
		return xrayWorkerState{}, err
	}
	rendered, err := renderXrayWorkerConfig(candidate)
	if err != nil {
		return xrayWorkerState{}, err
	}
	activeHash, activeErr := canonicalXrayConfigSHA256(active)
	renderedHash, renderedErr := canonicalXrayConfigSHA256(rendered)
	if activeErr != nil || renderedErr != nil || activeHash != renderedHash {
		return xrayWorkerState{}, errors.New("agent: active Xray configuration cannot be represented without changes")
	}
	return candidate, nil
}

func (e ApplicationExecutor) applyAgentXrayState(ctx context.Context, docker *client.Client, state xrayWorkerState, active []byte) error {
	inspected, _, exists, err := inspectCurrentXrayWorkerRuntime(ctx, docker)
	if err != nil || !exists {
		return errors.Join(errors.New("agent: Xray worker container is unavailable"), err)
	}
	staged, path, err := e.Store.stageXrayWorkerConfig(state)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if err := validateXrayWorkerConfig(ctx, docker, state.ImageReference, staged); err != nil {
		return err
	}
	if err := commitXrayWorkerConfig(staged, path); err != nil {
		return err
	}
	timeout := 15
	restartErr := error(nil)
	if _, err := docker.ContainerRestart(ctx, inspected.Container.ID, client.ContainerRestartOptions{Timeout: &timeout}); err != nil && !errdefs.IsNotModified(err) {
		restartErr = err
	} else {
		restartErr = waitForXrayWorkerRuntime(ctx, docker, inspected.Container.ID)
	}
	if restartErr == nil {
		if err := e.Store.recordXrayWorkerApplied(state); err != nil {
			return uncertainTaskOutcome(err)
		}
		return nil
	}
	restoreErr := e.Store.writeExactXrayWorkerConfig(active)
	_, rollbackRestartErr := docker.ContainerRestart(ctx, inspected.Container.ID, client.ContainerRestartOptions{Timeout: &timeout})
	if rollbackRestartErr == nil {
		rollbackRestartErr = waitForXrayWorkerRuntime(ctx, docker, inspected.Container.ID)
	}
	combined := errors.Join(restartErr, restoreErr, rollbackRestartErr)
	if restoreErr != nil || rollbackRestartErr != nil {
		return uncertainTaskOutcome(combined)
	}
	return combined
}

func (s *Store) writeExactXrayWorkerConfig(encoded []byte) error {
	directory := filepath.Join(s.dataDir, "xray-worker")
	if len(encoded) == 0 || len(encoded) > xrayWorkerMaxBody {
		return errors.New("agent: invalid Xray configuration body")
	}
	temporary, err := os.CreateTemp(directory, "recovery-*.json")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if os.Geteuid() == 0 {
		err = temporary.Chown(xrayWorkerRuntimeUID(), -1)
	}
	if err == nil {
		err = temporary.Chmod(0o600)
	}
	if err == nil {
		_, err = temporary.Write(encoded)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return commitXrayWorkerConfig(name, filepath.Join(directory, "config.json"))
}
