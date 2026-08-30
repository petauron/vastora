package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
)

const (
	hostDecommissionDir           = "/var/lib/vastora-decommission"
	hostDecommissionBinary        = hostDecommissionDir + "/vastora"
	hostDecommissionOperationPath = hostDecommissionDir + "/operation.json"
	hostDecommissionCompleted     = hostDecommissionDir + "/completed"
	hostDecommissionUnit          = "/etc/systemd/system/vastora-agent-decommission.service"
	hostDecommissionUnitName      = "vastora-agent-decommission.service"
)

type systemHostDecommissioner struct {
	dataDir    string
	executable string
}

func (d systemHostDecommissioner) Prepare(ctx context.Context, deleteData bool) error {
	return agent.PurgeManagedRuntime(ctx, deleteData)
}

func (d systemHostDecommissioner) ScheduleFinalRemoval(ctx context.Context, request agent.HostDecommissionRequest) error {
	executable, err := filepath.Abs(d.executable)
	if err != nil {
		return fmt.Errorf("agent: resolve decommission executable: %w", err)
	}
	operation := hostDecommissionOperation{
		Version: 1, TaskID: request.TaskID, Attempt: request.Attempt, DeleteData: request.DeleteData, DataDir: d.dataDir,
		AgentID: request.Connection.AgentID, CenterURL: request.Connection.CenterURL, Credential: request.Connection.Credential, CAFingerprint: request.Connection.CAFingerprint,
	}
	if err := persistHostDecommission(executable, operation); err != nil {
		return err
	}
	for _, arguments := range [][]string{{"daemon-reload"}, {"enable", "--now", hostDecommissionUnitName}} {
		output, err := exec.CommandContext(ctx, "systemctl", arguments...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("agent: start persistent host cleanup: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	return nil
}

type hostDecommissionOperation struct {
	Version       int    `json:"version"`
	TaskID        string `json:"taskId"`
	Attempt       int64  `json:"attempt"`
	DeleteData    bool   `json:"deleteData"`
	DataDir       string `json:"dataDir"`
	AgentID       string `json:"agentId"`
	CenterURL     string `json:"centerUrl"`
	Credential    string `json:"credential"`
	CAFingerprint string `json:"caFingerprint"`
}

func persistHostDecommission(executable string, operation hostDecommissionOperation) error {
	if operation.Version != 1 || operation.TaskID != "agent-decommission-"+operation.AgentID || operation.Attempt <= 0 || strings.TrimSpace(operation.Credential) == "" {
		return errors.New("agent: invalid persistent host cleanup operation")
	}
	if _, err := safeAgentDataDir(operation.DataDir); err != nil {
		return err
	}
	if err := os.MkdirAll(hostDecommissionDir, 0o700); err != nil {
		return fmt.Errorf("agent: create persistent host cleanup directory: %w", err)
	}
	if err := os.Chmod(hostDecommissionDir, 0o700); err != nil {
		return fmt.Errorf("agent: protect persistent host cleanup directory: %w", err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		return fmt.Errorf("agent: read host cleanup executable: %w", err)
	}
	if err := writeRootFileAtomic(hostDecommissionBinary, binary, 0o700); err != nil {
		return fmt.Errorf("agent: persist host cleanup executable: %w", err)
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		return fmt.Errorf("agent: encode persistent host cleanup operation: %w", err)
	}
	if err := writeRootFileAtomic(hostDecommissionOperationPath, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("agent: persist host cleanup operation: %w", err)
	}
	unit := "[Unit]\nDescription=Vastora Agent host decommission\nWants=network-online.target\nAfter=network-online.target\nStartLimitIntervalSec=0\n\n[Service]\nType=simple\nExecStart=" + hostDecommissionBinary + " agent finish-decommission --operation-file " + hostDecommissionOperationPath + "\nExecStopPost=" + hostDecommissionBinary + " agent cleanup-decommission --operation-file " + hostDecommissionOperationPath + "\nRestart=on-failure\nRestartSec=5s\n\n[Install]\nWantedBy=multi-user.target\n"
	if strings.Contains(unit, operation.Credential) {
		return errors.New("agent: refusing to expose host cleanup credentials in systemd")
	}
	if err := writeRootFileAtomic(hostDecommissionUnit, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("agent: persist host cleanup service: %w", err)
	}
	return nil
}

func runPersistentHostDecommission(ctx context.Context, operationPath string) error {
	completionPath := filepath.Join(filepath.Dir(operationPath), filepath.Base(hostDecommissionCompleted))
	if completed, err := protectedCompletionMarkerExists(completionPath); err != nil {
		return err
	} else if completed {
		return nil
	}
	operation, err := readHostDecommissionOperation(operationPath)
	if err != nil {
		return err
	}
	connection := agent.Connection{AgentID: operation.AgentID, CenterURL: operation.CenterURL, Credential: operation.Credential, CAFingerprint: operation.CAFingerprint}
	client := agent.Client{}
	requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = client.BeginHostDecommission(requestContext, connection, operation.TaskID, operation.Attempt)
	cancel()
	if err != nil {
		return fmt.Errorf("agent: transfer host cleanup responsibility to Center: %w", err)
	}
	if err := uninstallAgentHost(ctx, operation.DataDir, operation.DeleteData, true, false); err != nil {
		return err
	}
	requestContext, cancel = context.WithTimeout(ctx, 30*time.Second)
	err = client.CompleteHostDecommission(requestContext, connection, operation.TaskID, operation.Attempt, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("agent: report completed host cleanup: %w", err)
	}
	return writeRootFileAtomic(completionPath, []byte("completed\n"), 0o600)
}

func readHostDecommissionOperation(path string) (hostDecommissionOperation, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return hostDecommissionOperation{}, fmt.Errorf("agent: inspect persistent host cleanup operation: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return hostDecommissionOperation{}, errors.New("agent: persistent host cleanup operation is not a protected regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return hostDecommissionOperation{}, fmt.Errorf("agent: read persistent host cleanup operation: %w", err)
	}
	var operation hostDecommissionOperation
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&operation) != nil || decoder.Decode(&struct{}{}) != io.EOF || operation.Version != 1 || operation.TaskID != "agent-decommission-"+operation.AgentID || operation.Attempt <= 0 || strings.TrimSpace(operation.Credential) == "" {
		return hostDecommissionOperation{}, errors.New("agent: invalid persistent host cleanup operation")
	}
	if _, err := safeAgentDataDir(operation.DataDir); err != nil {
		return hostDecommissionOperation{}, err
	}
	return operation, nil
}

func cleanPersistentHostDecommission(operationPath string) error {
	if _, err := readHostDecommissionOperation(operationPath); err != nil {
		return err
	}
	completionPath := filepath.Join(filepath.Dir(operationPath), filepath.Base(hostDecommissionCompleted))
	completed, err := protectedCompletionMarkerExists(completionPath)
	if err != nil {
		return err
	}
	if !completed {
		return nil
	}
	if output, err := exec.Command("systemctl", "disable", hostDecommissionUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("disable persistent host cleanup: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := os.Remove(hostDecommissionUnit); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove persistent host cleanup service: %w", err)
	}
	if output, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("reload systemd after persistent host cleanup: %s: %w", strings.TrimSpace(string(output)), err)
	}
	var result error
	for _, path := range []string{operationPath, completionPath, hostDecommissionBinary} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, fmt.Errorf("remove persistent host cleanup state %s: %w", path, err))
		}
	}
	if err := os.Remove(hostDecommissionDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, fmt.Errorf("remove persistent host cleanup directory: %w", err))
	}
	return result
}

func protectedCompletionMarkerExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("agent: inspect persistent host cleanup completion: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return false, errors.New("agent: persistent host cleanup completion is not a protected regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("agent: read persistent host cleanup completion: %w", err)
	}
	if string(raw) != "completed\n" {
		return false, errors.New("agent: invalid persistent host cleanup completion")
	}
	return true, nil
}

func writeRootFileAtomic(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".vastora-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func uninstallAgentHost(ctx context.Context, dataDir string, deleteData, runtimeCleaned, keepBinary bool) error {
	dataDir, err := safeAgentDataDir(dataDir)
	if err != nil {
		return err
	}
	return uninstallAgentHostWithEnvironment(ctx, deleteData, runtimeCleaned, keepBinary, agentUninstallEnvironment{
		dataDir:              dataDir,
		unitPath:             vastoraAgentUnitPath,
		binaryPaths:          []string{"/usr/local/bin/vastora", "/usr/local/bin/vastora.previous"},
		tailscalePaths:       []string{"/etc/apt/sources.list.d/tailscale.list", "/usr/share/keyrings/tailscale-archive-keyring.gpg", "/var/lib/tailscale"},
		tailscalePrivacyPath: "/etc/systemd/system/tailscaled.service.d/90-vastora-privacy.conf",
		tailscaleEndpointPaths: []string{
			"/etc/vastora/tailscaled.json",
			"/etc/systemd/system/tailscaled.service.d/91-vastora-endpoint.conf",
		},
		tailscaleHostsPath: "/etc/hosts",
		purgeRuntime:       agent.PurgeManagedRuntime,
		run:                runHostCommand,
	})
}

func safeAgentDataDir(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", fmt.Errorf("resolve Agent state directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	for _, unsafe := range []string{"/", "/tmp", "/var", "/var/lib", "/var/lib/vastora"} {
		if absolute == unsafe {
			return "", fmt.Errorf("refusing unsafe Agent state directory: %s", absolute)
		}
	}
	if info, err := os.Lstat(absolute); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("refusing an Agent state directory reached through a symbolic link")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect Agent state directory: %w", err)
	}
	return absolute, nil
}

type agentUninstallEnvironment struct {
	dataDir                string
	unitPath               string
	binaryPaths            []string
	tailscalePaths         []string
	tailscalePrivacyPath   string
	tailscaleEndpointPaths []string
	tailscaleHostsPath     string
	purgeRuntime           func(context.Context, bool) error
	run                    func(context.Context, string, ...string) ([]byte, error)
}

func runHostCommand(ctx context.Context, name string, arguments ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, arguments...).CombinedOutput()
}

func uninstallAgentHostWithEnvironment(ctx context.Context, deleteData, runtimeCleaned, keepBinary bool, environment agentUninstallEnvironment) error {
	state, err := agent.ReadHostInstallState(environment.dataDir)
	if err != nil {
		return err
	}
	statePath := filepath.Join(environment.dataDir, agent.HostInstallStateName)
	_, stateFileErr := os.Stat(statePath)
	stateRecorded := stateFileErr == nil
	if stateFileErr != nil && !errors.Is(stateFileErr, os.ErrNotExist) {
		return fmt.Errorf("inspect Agent host ownership state: %w", stateFileErr)
	}
	if !runtimeCleaned {
		if err := environment.purgeRuntime(ctx, deleteData); err != nil {
			return fmt.Errorf("remove managed Agent runtime: %w", err)
		}
	}
	if state.TailscaleEnrolled {
		output, logoutErr := environment.run(ctx, "tailscale", "logout")
		if logoutErr != nil && state.TailscaleOwnership != "managed" && !errors.Is(logoutErr, exec.ErrNotFound) {
			return fmt.Errorf("disconnect the external Tailscale installation from Vastora: %s: %w", strings.TrimSpace(string(output)), logoutErr)
		}
	}
	if state.TailscaleOwnership == "managed" {
		_, _ = environment.run(ctx, "systemctl", "disable", "--now", "tailscaled.service")
		if output, err := environment.run(ctx, "apt-get", "purge", "-y", "tailscale", "tailscale-archive-keyring"); err != nil {
			return fmt.Errorf("remove Vastora-managed Tailscale: %s: %w", strings.TrimSpace(string(output)), err)
		}
		for _, path := range environment.tailscalePaths {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove Vastora-managed Tailscale state %s: %w", path, err)
			}
		}
		for _, path := range environment.tailscaleEndpointPaths {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove Vastora-managed Tailscale endpoint state %s: %w", path, err)
			}
		}
	}
	if environment.tailscalePrivacyPath != "" {
		privacyStateRemoved := false
		for _, path := range []string{environment.tailscalePrivacyPath, tailscalePrivacyAppliedPath(environment.tailscalePrivacyPath)} {
			if err := os.Remove(path); err == nil {
				privacyStateRemoved = true
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove Vastora Tailscale privacy state %s: %w", path, err)
			}
		}
		_ = os.Remove(filepath.Dir(environment.tailscalePrivacyPath))
		if privacyStateRemoved {
			if output, err := environment.run(ctx, "systemctl", "daemon-reload"); err != nil {
				return fmt.Errorf("reload systemd after removing the Tailscale privacy override: %s: %w", strings.TrimSpace(string(output)), err)
			}
		}
	}
	if environment.tailscaleHostsPath != "" {
		if _, err := removeTailscaleControlHosts(environment.tailscaleHostsPath); err != nil {
			return fmt.Errorf("remove Vastora Headscale resolver pin: %w", err)
		}
	}
	unitOwned, err := stopAndRemoveAgentUnit(ctx, environment)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(environment.dataDir); err != nil {
		return fmt.Errorf("remove Agent state: %w", err)
	}
	if !keepBinary && (unitOwned || stateRecorded) {
		for _, path := range environment.binaryPaths {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove Agent command %s: %w", path, err)
			}
		}
	}
	return nil
}

func stopAndRemoveAgentUnit(ctx context.Context, environment agentUninstallEnvironment) (bool, error) {
	raw, err := os.ReadFile(environment.unitPath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect Agent service: %w", err)
	}
	if !strings.Contains(string(raw), "Description=Vastora Agent") || !strings.Contains(string(raw), " agent serve ") {
		return false, errors.New("refusing to remove an unrecognized vastora-agent.service")
	}
	if output, err := environment.run(ctx, "systemctl", "disable", "--now", "vastora-agent.service"); err != nil {
		return false, fmt.Errorf("stop Agent service: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := os.Remove(environment.unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove Agent service: %w", err)
	}
	if output, err := environment.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return false, fmt.Errorf("reload systemd after Agent removal: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return true, nil
}
