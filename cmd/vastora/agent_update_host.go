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
	hostUpdateDir           = "/var/lib/vastora-agent-update"
	hostUpdateBinary        = hostUpdateDir + "/vastora"
	hostUpdateOperationPath = hostUpdateDir + "/operation.json"
	hostUpdateResultPath    = hostUpdateDir + "/result.json"
	hostUpdateCompleted     = hostUpdateDir + "/completed"
	hostUpdateUnit          = "/etc/systemd/system/vastora-agent-update.service"
	hostUpdateUnitName      = "vastora-agent-update.service"
	hostUpdateEnabledLink   = "/etc/systemd/system/multi-user.target.wants/" + hostUpdateUnitName
)

type systemHostUpdater struct {
	dataDir    string
	executable string
}

type hostUpdateOperation struct {
	Version          int    `json:"version"`
	TaskID           string `json:"taskId"`
	Attempt          int64  `json:"attempt"`
	TargetVersion    string `json:"targetVersion"`
	SourceVersion    string `json:"sourceVersion"`
	DataDir          string `json:"dataDir"`
	Executable       string `json:"executable"`
	AgentID          string `json:"agentId"`
	CenterURL        string `json:"centerUrl"`
	Credential       string `json:"credential"`
	CAFingerprint    string `json:"caFingerprint"`
	CACertificatePEM string `json:"caCertificatePem,omitempty"`
}

type hostUpdateResult struct {
	Succeeded bool   `json:"succeeded"`
	Error     string `json:"error"`
}

func (u systemHostUpdater) ScheduleUpdate(ctx context.Context, request agent.HostUpdateRequest) error {
	if cancelled, err := hostUpdateCancelled(hostUpdateOperationPath); err != nil {
		return err
	} else if cancelled {
		return errors.New("agent: uninstall cancelled the pending Agent update")
	}
	executable, err := filepath.Abs(u.executable)
	if err != nil {
		return fmt.Errorf("agent: resolve update executable: %w", err)
	}
	if info, err := os.Stat(executable); err != nil || !info.Mode().IsRegular() {
		return errors.New("agent: update executable is not a regular file")
	}
	if err := os.MkdirAll(hostUpdateDir, 0o700); err != nil {
		return fmt.Errorf("agent: create persistent update directory: %w", err)
	}
	if err := os.Chmod(hostUpdateDir, 0o700); err != nil {
		return fmt.Errorf("agent: protect persistent update directory: %w", err)
	}
	client, err := agent.CenterHTTPClient(request.Connection, 2*time.Minute)
	if err != nil {
		return err
	}
	candidate, version, err := downloadAgentUpdateCandidate(ctx, client, request.Connection, hostUpdateDir)
	if err != nil {
		return err
	}
	defer os.Remove(candidate)
	if version != request.TargetVersion {
		return fmt.Errorf("agent: Center offered version %s for update task targeting %s", version, request.TargetVersion)
	}
	operation := hostUpdateOperation{
		Version: 1, TaskID: request.TaskID, Attempt: request.Attempt, TargetVersion: version, SourceVersion: agent.Version,
		DataDir: u.dataDir, Executable: executable, AgentID: request.Connection.AgentID, CenterURL: request.Connection.CenterURL,
		Credential: request.Connection.Credential, CAFingerprint: request.Connection.CAFingerprint, CACertificatePEM: request.Connection.CACertificatePEM,
	}
	if err := persistHostUpdate(candidate, operation); err != nil {
		return err
	}
	for _, arguments := range [][]string{{"daemon-reload"}, {"enable", "--now", "--no-block", hostUpdateUnitName}} {
		output, err := exec.CommandContext(ctx, "systemctl", arguments...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("agent: start persistent host update: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	return nil
}

func persistHostUpdate(candidate string, operation hostUpdateOperation) error {
	if cancelled, err := hostUpdateCancelled(hostUpdateOperationPath); err != nil {
		return err
	} else if cancelled {
		return errors.New("agent: refusing to replace an Agent update cancelled by uninstall")
	}
	if err := validateHostUpdateOperation(operation); err != nil {
		return err
	}
	binary, err := os.ReadFile(candidate)
	if err != nil {
		return fmt.Errorf("agent: read staged update executable: %w", err)
	}
	if err := writeRootFileAtomic(hostUpdateBinary, binary, 0o700); err != nil {
		return fmt.Errorf("agent: persist update executable: %w", err)
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		return fmt.Errorf("agent: encode persistent update operation: %w", err)
	}
	if err := writeRootFileAtomic(hostUpdateOperationPath, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("agent: persist update operation: %w", err)
	}
	for _, path := range []string{hostUpdateResultPath, hostUpdateCompleted} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agent: clear previous update state: %w", err)
		}
	}
	unit := hostUpdateServiceUnit()
	if strings.Contains(unit, operation.Credential) {
		return errors.New("agent: refusing to expose update credentials in systemd")
	}
	if err := writeRootFileAtomic(hostUpdateUnit, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("agent: persist update service: %w", err)
	}
	return nil
}

func runPersistentHostUpdate(ctx context.Context, operationPath string) error {
	if cancelled, err := hostUpdateCancelled(operationPath); err != nil || cancelled {
		return err
	}
	completionPath := filepath.Join(filepath.Dir(operationPath), filepath.Base(hostUpdateCompleted))
	if completed, err := protectedCleanupMarkerExists(completionPath, "completed\n"); err != nil {
		return err
	} else if completed {
		return nil
	}
	operation, err := readHostUpdateOperation(operationPath)
	if err != nil {
		return err
	}
	connection := agent.Connection{AgentID: operation.AgentID, CenterURL: operation.CenterURL, Credential: operation.Credential, CAFingerprint: operation.CAFingerprint, CACertificatePEM: operation.CACertificatePEM}
	client := agent.Client{}
	requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = client.BeginHostUpdate(requestContext, connection, operation.TaskID, operation.Attempt)
	cancel()
	if err != nil {
		return fmt.Errorf("agent: transfer update responsibility to Center: %w", err)
	}
	resultPath := filepath.Join(filepath.Dir(operationPath), filepath.Base(hostUpdateResultPath))
	result, exists, err := readHostUpdateResult(resultPath)
	if err != nil {
		return err
	}
	if !exists {
		updateErr := activateHostUpdate(ctx, operation)
		result = hostUpdateResult{Succeeded: updateErr == nil}
		if updateErr != nil {
			result.Error = updateErr.Error()
		}
		if err := writeHostUpdateResult(resultPath, result); err != nil {
			return err
		}
	} else if result.Succeeded {
		if version, versionErr := executableVersion(ctx, operation.Executable); versionErr != nil || version != operation.TargetVersion || !agentServiceActive(ctx) {
			rollbackErr := rollbackHostUpdate(ctx, operation)
			result = hostUpdateResult{Error: "updated Agent did not remain active"}
			if rollbackErr != nil {
				result.Error += "; rollback failed: " + rollbackErr.Error()
			}
			if err := writeHostUpdateResult(resultPath, result); err != nil {
				return err
			}
		}
	}
	var updateErr error
	if !result.Succeeded {
		updateErr = errors.New(result.Error)
	}
	requestContext, cancel = context.WithTimeout(ctx, 30*time.Second)
	err = client.CompleteHostUpdate(requestContext, connection, operation.TaskID, operation.Attempt, updateErr)
	cancel()
	if err != nil {
		return fmt.Errorf("agent: report host update result: %w", err)
	}
	return writeRootFileAtomic(completionPath, []byte("completed\n"), 0o600)
}

func activateHostUpdate(ctx context.Context, operation hostUpdateOperation) error {
	currentVersion, currentErr := executableVersion(ctx, operation.Executable)
	if currentErr != nil {
		return fmt.Errorf("agent: inspect installed Agent before update: %w", currentErr)
	}
	if currentVersion == operation.TargetVersion {
		return ensureAgentServiceActive(ctx)
	}
	if currentVersion != operation.SourceVersion {
		return fmt.Errorf("agent: installed version changed from %s to %s before update", operation.SourceVersion, currentVersion)
	}
	if candidateVersion, err := executableVersion(ctx, hostUpdateBinary); err != nil || candidateVersion != operation.TargetVersion {
		return errors.New("agent: persistent update executable does not match the target version")
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "stop", "vastora-agent.service").CombinedOutput(); err != nil {
		return fmt.Errorf("agent: stop Agent for update: %s: %w", strings.TrimSpace(string(output)), err)
	}
	previous := operation.Executable + ".previous"
	if err := copyExecutableAtomic(operation.Executable, previous); err != nil {
		_ = ensureAgentServiceActive(ctx)
		return fmt.Errorf("agent: preserve previous Agent executable: %w", err)
	}
	if err := copyExecutableAtomic(hostUpdateBinary, operation.Executable); err != nil {
		_ = rollbackHostUpdate(ctx, operation)
		return fmt.Errorf("agent: install Agent update: %w", err)
	}
	if err := ensureAgentServiceActive(ctx); err != nil {
		rollbackErr := rollbackHostUpdate(ctx, operation)
		if rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("agent: rollback failed: %w", rollbackErr))
		}
		return err
	}
	return nil
}

func rollbackHostUpdate(ctx context.Context, operation hostUpdateOperation) error {
	previous := operation.Executable + ".previous"
	if version, err := executableVersion(ctx, previous); err != nil || version != operation.SourceVersion {
		return errors.New("agent: previous Agent executable is unavailable")
	}
	if output, err := exec.CommandContext(ctx, "systemctl", "stop", "vastora-agent.service").CombinedOutput(); err != nil {
		return fmt.Errorf("agent: stop Agent for rollback: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := copyExecutableAtomic(previous, operation.Executable); err != nil {
		return err
	}
	return ensureAgentServiceActive(ctx)
}

func ensureAgentServiceActive(ctx context.Context) error {
	if output, err := exec.CommandContext(ctx, "systemctl", "start", "vastora-agent.service").CombinedOutput(); err != nil {
		return fmt.Errorf("agent: start updated Agent: %s: %w", strings.TrimSpace(string(output)), err)
	}
	stable := 0
	for range 10 {
		if agentServiceActive(ctx) {
			stable++
			if stable == 3 {
				return nil
			}
		} else {
			stable = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("agent: updated Agent did not become stable")
}

func agentServiceActive(ctx context.Context) bool {
	return exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", "vastora-agent.service").Run() == nil
}

func executableVersion(ctx context.Context, path string) (string, error) {
	output, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func copyExecutableAtomic(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".vastora-update-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o755); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, io.LimitReader(input, (256<<20)+1)); err != nil {
		_ = temporary.Close()
		return err
	}
	if info, err := temporary.Stat(); err != nil || info.Size() > 256<<20 {
		_ = temporary.Close()
		return errors.New("agent: update executable exceeds 256 MiB")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
}

func readHostUpdateOperation(path string) (hostUpdateOperation, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return hostUpdateOperation{}, fmt.Errorf("agent: inspect persistent update operation: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return hostUpdateOperation{}, errors.New("agent: persistent update operation is not a protected regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return hostUpdateOperation{}, fmt.Errorf("agent: read persistent update operation: %w", err)
	}
	var operation hostUpdateOperation
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&operation) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return hostUpdateOperation{}, errors.New("agent: invalid persistent update operation")
	}
	if err := validateHostUpdateOperation(operation); err != nil {
		return hostUpdateOperation{}, err
	}
	return operation, nil
}

func validateHostUpdateOperation(operation hostUpdateOperation) error {
	if operation.Version != 1 || !strings.HasPrefix(operation.TaskID, "agent-update-") || operation.Attempt <= 0 || strings.TrimSpace(operation.TargetVersion) == "" || strings.TrimSpace(operation.SourceVersion) == "" || strings.TrimSpace(operation.AgentID) == "" || strings.TrimSpace(operation.Credential) == "" {
		return errors.New("agent: invalid persistent update operation")
	}
	if _, err := safeAgentDataDir(operation.DataDir); err != nil {
		return err
	}
	if !filepath.IsAbs(operation.Executable) || filepath.Clean(operation.Executable) != operation.Executable || operation.Executable == "/" {
		return errors.New("agent: invalid update executable path")
	}
	return nil
}

func readHostUpdateResult(path string) (hostUpdateResult, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return hostUpdateResult{}, false, nil
	}
	if err != nil {
		return hostUpdateResult{}, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return hostUpdateResult{}, false, errors.New("agent: persistent update result is not protected")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return hostUpdateResult{}, false, err
	}
	var result hostUpdateResult
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(&struct{}{}) != io.EOF || result.Succeeded == (strings.TrimSpace(result.Error) != "") {
		return hostUpdateResult{}, false, errors.New("agent: persistent update result is invalid")
	}
	return result, true, nil
}

func writeHostUpdateResult(path string, result hostUpdateResult) error {
	if result.Succeeded == (strings.TrimSpace(result.Error) != "") {
		return errors.New("agent: invalid update result")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return writeRootFileAtomic(path, append(raw, '\n'), 0o600)
}

func cleanPersistentHostUpdate(operationPath string) error {
	if cancelled, err := hostUpdateCancelled(operationPath); err != nil || cancelled {
		return err
	}
	if _, err := readHostUpdateOperation(operationPath); err != nil {
		return err
	}
	completionPath := filepath.Join(filepath.Dir(operationPath), filepath.Base(hostUpdateCompleted))
	completed, err := protectedCleanupMarkerExists(completionPath, "completed\n")
	if err != nil || !completed {
		return err
	}
	if output, err := exec.Command("systemctl", "disable", hostUpdateUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("disable persistent Agent update: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := os.Remove(hostUpdateUnit); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if output, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("reload systemd after Agent update: %s: %w", strings.TrimSpace(string(output)), err)
	}
	var result error
	for _, path := range []string{operationPath, hostUpdateResultPath, completionPath, hostUpdateBinary} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	if err := os.Remove(hostUpdateDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	return result
}

func hostUpdateServiceUnit() string {
	return "[Unit]\nDescription=Vastora Agent update\nWants=network-online.target\nAfter=network-online.target\nStartLimitIntervalSec=0\n\n[Service]\nType=oneshot\nExecStart=" + hostUpdateBinary + " agent finish-update --operation-file " + hostUpdateOperationPath + "\nExecStopPost=" + hostUpdateBinary + " agent cleanup-update --operation-file " + hostUpdateOperationPath + "\nRestart=on-failure\nRestartSec=5s\n\n[Install]\nWantedBy=multi-user.target\n"
}

func hostUpdateCancelled(operationPath string) (bool, error) {
	return protectedCleanupMarkerExists(filepath.Join(filepath.Dir(operationPath), "cancelled"), "cancelled\n")
}

func hostUpdateDataDir(path string) (string, error) {
	operation, err := readHostUpdateOperation(path)
	return operation.DataDir, err
}
