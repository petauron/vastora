package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	ExecutionID      string `json:"executionId"`
	SessionID        string `json:"sessionId"`
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

var errHostUpdateCandidatePending = errors.New("agent: update requires explicit maintenance; protected recovery state retained")
var errHostUpdateExecutableInstalled = errors.New("agent: update executable was installed but its directory sync failed")

type hostUpdateActivationEnvironment struct {
	authorize         func(context.Context, string) error
	candidatePath     string
	recoveryDirectory string
	run               func(context.Context, string, ...string) ([]byte, error)
	version           func(context.Context, string) (string, error)
	serviceActive     func(context.Context) bool
	wait              func(context.Context) error
	prepareRecovery   func(context.Context, hostUpdateOperation, string) error
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
	operation := hostUpdateOperation{
		ExecutionID: request.ExecutionID, SessionID: request.SessionID,
		Version: 1, TaskID: request.TaskID, Attempt: request.Attempt, TargetVersion: request.TargetVersion, SourceVersion: agent.Version,
		DataDir: u.dataDir, Executable: executable, AgentID: request.Connection.AgentID, CenterURL: request.Connection.CenterURL,
		Credential: request.Connection.Credential, CAFingerprint: request.Connection.CAFingerprint, CACertificatePEM: request.Connection.CACertificatePEM,
	}
	if err := prepareHostUpdateDirectory(ctx, hostUpdateDir, operation, runHostCommand, executableVersion); err != nil {
		return err
	}
	client, err := agent.CenterHTTPClient(request.Connection, 2*time.Minute)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	candidate, version, err := downloadAgentUpdateCandidate(ctx, client, request.Connection, hostUpdateDir)
	if err != nil {
		return err
	}
	defer os.Remove(candidate)
	if version != request.TargetVersion {
		return fmt.Errorf("agent: Center offered version %s for update task targeting %s", version, request.TargetVersion)
	}
	if err := persistHostUpdate(candidate, operation); err != nil {
		return err
	}
	return startHostUpdateHelper(ctx, runHostCommand)
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
	if existing, err := readHostUpdateOperation(hostUpdateOperationPath); err == nil {
		// Never overwrite the executable, credentials or recovery ownership of
		// an unfinished helper. Re-delivery of its exact operation is harmless.
		if existing != operation {
			return errors.New("agent: a previous host update still owns the update directory")
		}
		staged, stagedErr := hashHostUpdateExecutable(candidate)
		persisted, persistedErr := hashHostUpdateExecutable(hostUpdateBinary)
		if stagedErr != nil || persistedErr != nil || staged != persisted {
			return errors.New("agent: repeated host update candidate changed")
		}
		return persistHostUpdateUnit(operation)
	} else if !errors.Is(err, os.ErrNotExist) {
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
	for _, path := range []string{hostUpdateResultPath, hostUpdateCompleted} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agent: clear previous update state: %w", err)
		}
	}
	if err := writeRootFileAtomic(hostUpdateOperationPath, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("agent: persist update operation: %w", err)
	}
	return persistHostUpdateUnit(operation)
}

func persistHostUpdateUnit(operation hostUpdateOperation) error {
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
	operation, err := readHostUpdateOperation(operationPath)
	if err != nil {
		return err
	}
	return runPersistentHostUpdateWithEnvironment(ctx, operationPath, defaultHostUpdateActivationEnvironment(filepath.Dir(operationPath), operation), agent.Client{})
}

func runPersistentHostUpdateWithEnvironment(ctx context.Context, operationPath string, environment hostUpdateActivationEnvironment, client agent.Client) error {
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
	resultPath := filepath.Join(filepath.Dir(operationPath), filepath.Base(hostUpdateResultPath))
	result, exists, err := readHostUpdateResult(resultPath)
	if err != nil {
		return err
	}
	connection := agent.Connection{AgentID: operation.AgentID, CenterURL: operation.CenterURL, Credential: operation.Credential, CAFingerprint: operation.CAFingerprint, CACertificatePEM: operation.CACertificatePEM}
	requestContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	if !exists {
		err = client.BeginHostUpdate(requestContext, connection, operation.TaskID, operation.Attempt, operation.ExecutionID, operation.SessionID)
	}
	cancel()
	if err != nil {
		return fmt.Errorf("agent: transfer update responsibility to Center: %w", err)
	}
	reportRecoveryRequired := func(updateErr error) error {
		recoveryRequired := fmt.Errorf("agent: recovery required; update state and protected pre-migration recovery are retained at %s: %v", environment.recoveryDirectory, updateErr)
		requestContext, cancel = context.WithTimeout(ctx, 30*time.Second)
		reportErr := client.CompleteHostUpdate(requestContext, connection, operation.TaskID, operation.Attempt, recoveryRequired, true, operation.ExecutionID, operation.SessionID)
		cancel()
		if reportErr != nil {
			return errors.Join(updateErr, fmt.Errorf("agent: report recovery-required host update: %w", reportErr))
		}
		return updateErr
	}
	if !exists {
		environment.authorize = func(stepContext context.Context, phase string) error {
			requestContext, cancel := context.WithTimeout(stepContext, 30*time.Second)
			defer cancel()
			return client.CheckHostUpdateStep(requestContext, connection, operation.ExecutionID, operation.SessionID, phase)
		}
		updateErr := activateHostUpdate(ctx, operation, environment)
		var terminal bool
		result, terminal = hostUpdateActivationResult(updateErr)
		if !terminal {
			// The candidate may already have migrated agent.db. Keep the helper
			// stopped and do not publish a local terminal result that would
			// trigger cleanup or restore the source executable. Center still gets
			// an actionable recovery error; only an explicit maintenance decision may
			// authorize another activation.
			return reportRecoveryRequired(updateErr)
		}
		if err := writeHostUpdateResult(resultPath, result); err != nil {
			return err
		}
	}
	var updateErr error
	if !result.Succeeded {
		updateErr = errors.New(result.Error)
	} else {
		observationContext, stopObservation := context.WithTimeout(ctx, 30*time.Second)
		defer stopObservation()
		observed := false
		for range 30 {
			ready, err := client.HostUpdateObserved(observationContext, connection, operation.ExecutionID, operation.SessionID)
			if err != nil {
				return fmt.Errorf("agent: observe updated Agent: %w", err)
			}
			if ready {
				observed = true
				break
			}
			if err := environment.wait(observationContext); err != nil {
				return err
			}
		}
		if !observed {
			return errors.New("agent: updated Agent was not observed before the deadline; verification required")
		}
	}
	requestContext, cancel = context.WithTimeout(ctx, 30*time.Second)
	err = client.CompleteHostUpdate(requestContext, connection, operation.TaskID, operation.Attempt, updateErr, false, operation.ExecutionID, operation.SessionID)
	cancel()
	if err != nil {
		return fmt.Errorf("agent: report host update result: %w", err)
	}
	return writeRootFileAtomic(completionPath, []byte("completed\n"), 0o600)
}

func hostUpdateActivationResult(updateErr error) (hostUpdateResult, bool) {
	if errors.Is(updateErr, errHostUpdateCandidatePending) {
		return hostUpdateResult{}, false
	}
	result := hostUpdateResult{Succeeded: updateErr == nil}
	if updateErr != nil {
		result.Error = updateErr.Error()
	}
	return result, true
}

func defaultHostUpdateActivationEnvironment(directory string, operation hostUpdateOperation) hostUpdateActivationEnvironment {
	return hostUpdateActivationEnvironment{
		candidatePath:     filepath.Join(directory, filepath.Base(hostUpdateBinary)),
		recoveryDirectory: hostUpdateRecoveryDirectory(directory, operation),
		run:               runHostCommand,
		version:           executableVersion,
		serviceActive:     agentServiceActive,
		wait: func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
				return nil
			}
		},
		prepareRecovery: func(ctx context.Context, operation hostUpdateOperation, recoveryDirectory string) error {
			return prepareHostUpdateRecovery(ctx, operation, recoveryDirectory, filepath.Join(directory, filepath.Base(hostUpdateBinary)))
		},
	}
}

func activateHostUpdate(ctx context.Context, operation hostUpdateOperation, environment hostUpdateActivationEnvironment) error {
	if _, err := os.Lstat(environment.recoveryDirectory); err == nil {
		return fmt.Errorf("%w: previous activation is unresolved; explicit maintenance is required", errHostUpdateCandidatePending)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect pre-migration recovery point: %v", errHostUpdateCandidatePending, err)
	}
	currentVersion, currentErr := environment.version(ctx, operation.Executable)
	if currentErr != nil {
		return fmt.Errorf("agent: inspect installed Agent before update: %w", currentErr)
	}
	if currentVersion == operation.TargetVersion {
		return fmt.Errorf("%w: target executable is installed without its pre-migration recovery point", errHostUpdateCandidatePending)
	}
	if currentVersion != operation.SourceVersion {
		return fmt.Errorf("agent: installed version changed from %s to %s before update", operation.SourceVersion, currentVersion)
	}
	if candidateVersion, err := environment.version(ctx, environment.candidatePath); err != nil || candidateVersion != operation.TargetVersion {
		return errors.New("agent: persistent update executable does not match the target version")
	}
	check := func(phase string) error {
		if environment.authorize == nil {
			return errors.New("agent: update step authorization is missing")
		}
		if err := environment.authorize(ctx, phase); err != nil {
			return fmt.Errorf("%w: update step %s was not authorized: %v", errHostUpdateCandidatePending, phase, err)
		}
		return ctx.Err()
	}
	if err := check("stop"); err != nil {
		return err
	}
	if output, err := environment.run(ctx, "systemctl", "stop", "vastora-agent.service"); err != nil {
		return fmt.Errorf("agent: stop Agent for update: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := check("backup"); err != nil {
		return err
	}
	if err := environment.prepareRecovery(ctx, operation, environment.recoveryDirectory); err != nil {
		return fmt.Errorf("%w: prepare pre-migration recovery point: %v", errHostUpdateCandidatePending, err)
	}
	previous := operation.Executable + ".previous"
	if err := check("preserve"); err != nil {
		return err
	}
	if err := copyExecutableAtomic(operation.Executable, previous); err != nil {
		return fmt.Errorf("%w: preserve previous Agent executable: %v", errHostUpdateCandidatePending, err)
	}
	if err := check("install"); err != nil {
		return err
	}
	if err := copyExecutableAtomic(environment.candidatePath, operation.Executable); err != nil {
		if errors.Is(err, errHostUpdateExecutableInstalled) {
			return fmt.Errorf("%w: %v", errHostUpdateCandidatePending, err)
		}
		return fmt.Errorf("%w: install Agent update: %v", errHostUpdateCandidatePending, err)
	}
	// Installing the candidate is the durable update commit point. Starting it
	// may open and migrate agent.db before health verification completes, so
	// every later failure retains it for explicit maintenance, never auto-replay.
	if err := check("start"); err != nil {
		return err
	}
	if err := ensureAgentServiceActive(ctx, environment); err != nil {
		return fmt.Errorf("%w: %v", errHostUpdateCandidatePending, err)
	}
	return nil
}

func ensureAgentServiceActive(ctx context.Context, environment hostUpdateActivationEnvironment) error {
	if output, err := environment.run(ctx, "systemctl", "start", "vastora-agent.service"); err != nil {
		return fmt.Errorf("agent: start updated Agent: %s: %w", strings.TrimSpace(string(output)), err)
	}
	stable := 0
	for range 10 {
		if environment.serviceActive(ctx) {
			stable++
			if stable == 3 {
				return nil
			}
		} else {
			stable = 0
		}
		if err := environment.wait(ctx); err != nil {
			return err
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

func hashHostUpdateExecutable(path string) (hostUpdateRecoveryFile, error) {
	if err := requireHostUpdateRegularFile(path); err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return hostUpdateRecoveryFile{}, errors.New("agent: installed update executable is writable by another user")
	}
	file, err := os.Open(path)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, file)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	return hostUpdateRecoveryFile{Size: size, SHA256: fmt.Sprintf("%x", digest.Sum(nil))}, nil
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
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	if err := syncHostUpdateDirectory(filepath.Dir(destination)); err != nil {
		return fmt.Errorf("%w: %v", errHostUpdateExecutableInstalled, err)
	}
	return nil
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
	operation, err := readHostUpdateOperation(operationPath)
	if err != nil {
		return err
	}
	completionPath := filepath.Join(filepath.Dir(operationPath), filepath.Base(hostUpdateCompleted))
	completed, err := protectedCleanupMarkerExists(completionPath, "completed\n")
	if err != nil || !completed {
		return err
	}
	recoveryDirectory := hostUpdateRecoveryDirectory(filepath.Dir(operationPath), operation)
	for _, directory := range []string{recoveryDirectory, recoveryDirectory + ".partial"} {
		if err := removeHostUpdateRecovery(directory); err != nil {
			return err
		}
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
	for _, path := range []string{operationPath, hostUpdateResultPath, completionPath, hostUpdateBinary, operation.Executable + ".previous"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	// Other attempts' protected recovery points may remain for an operator;
	// they must not make this already acknowledged update restart forever.
	if err := os.Remove(hostUpdateDir); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTEMPTY) {
		result = errors.Join(result, err)
	}
	return result
}

func hostUpdateServiceUnit() string {
	return "[Unit]\nDescription=Vastora Agent update\nWants=network-online.target\nAfter=network-online.target\n\n[Service]\nType=oneshot\nExecStart=" + hostUpdateBinary + " agent finish-update --operation-file " + hostUpdateOperationPath + "\nExecStopPost=" + hostUpdateBinary + " agent cleanup-update --operation-file " + hostUpdateOperationPath + "\nRestart=no\n"
}

func hostUpdateCancelled(operationPath string) (bool, error) {
	return protectedCleanupMarkerExists(filepath.Join(filepath.Dir(operationPath), "cancelled"), "cancelled\n")
}

func hostUpdateDataDir(path string) (string, error) {
	operation, err := readHostUpdateOperation(path)
	return operation.DataDir, err
}
