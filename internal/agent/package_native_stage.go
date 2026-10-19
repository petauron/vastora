package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/moby/moby/client"
)

// NativePackageStage is private installation evidence for the one-time local
// Docker-to-native handover. It contains no bootstrap values or secret bytes.
// The active registration is unchanged until a separate adoption completes.
type NativePackageStage struct {
	Version          int                `json:"version"`
	SourceSHA256     string             `json:"sourceSha256"`
	Source           *InstanceResources `json:"source"`
	SourceOriginal   json.RawMessage    `json:"sourceOriginal"`
	SourceInstanceID string             `json:"sourceInstanceId"`
	InputSHA256      string             `json:"inputSha256"`
	DatabaseBackup   string             `json:"databaseBackup"`
	DatabaseSHA256   string             `json:"databaseSha256"`
	Candidate        *InstanceResources `json:"candidate"`
}

func nativeStagePath(directory, applicationID string) string {
	return filepath.Join(directory, "packages", packageIdentity(applicationID), "native-stage.json")
}

func readNativeStage(directory, applicationID string) (*NativePackageStage, error) {
	path := nativeStagePath(directory, applicationID)
	if err := checkPackageParents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8<<20 {
		return nil, errors.New("agent: unsafe native handover record")
	}
	raw, err := os.ReadFile(path)
	var stage NativePackageStage
	if err != nil || json.Unmarshal(raw, &stage) != nil || stage.Version != 1 || stage.Source == nil || stage.Candidate == nil || stage.Source.ApplicationID != applicationID || stage.Candidate.ApplicationID != applicationID {
		return nil, errors.New("agent: invalid native handover record")
	}
	return &stage, nil
}

func writeNativeStage(directory string, stage *NativePackageStage) error {
	path := nativeStagePath(directory, stage.Candidate.ApplicationID)
	if err := checkPackageParents(path); err != nil {
		return err
	}
	raw, err := json.Marshal(stage)
	if err != nil {
		return err
	}
	return writeHostFileAtomic(path, raw, 0600)
}

// StageNativePackage is called only by the explicit local maintenance command,
// after independently authenticating the target catalog. It never changes the
// active receipt, removes a source resource, or starts a candidate service.
// The caller must keep the Agent stopped until ownership handover is complete.
func (e ApplicationExecutor) StageNativePackage(ctx context.Context, task DeploymentTask, sourceSHA256 string, resumeReviewed bool) (*NativePackageStage, error) {
	if e.Store == nil || task.Operation != "install" || task.Manifest.Runtime == nil || task.Manifest.Runtime.Kind != "systemd" || task.Manifest.Runtime.Version != 2 {
		return nil, errors.New("agent: explicit native installation handover required")
	}
	if err := validatePackageTask(task); err != nil {
		return nil, err
	}
	manager, ok := e.Host.(SystemdHostApplicationManager)
	if !ok {
		return nil, errors.New("agent: native installation manager unavailable")
	}
	directory := e.packageDirectory()
	executor := PackageExecutor{StateDirectory: directory}
	source, err := executor.ReadResources(task.ApplicationID)
	if err != nil || source.State != "ready" || source.Runtime != "docker" || source.AppKey == task.AppKey {
		return nil, errors.New("agent: original adopted Docker registration required")
	}
	original, err := os.ReadFile(executor.receiptPath(task.ApplicationID))
	digest := sha256.Sum256(original)
	if err != nil || len(sourceSHA256) != 64 || hex.EncodeToString(digest[:]) != sourceSHA256 {
		return nil, errors.New("agent: original installation receipt changed")
	}
	history, err := e.Store.AppliedInstallation(ctx, source.AppKey)
	if err != nil || history.ApplicationID != task.ApplicationID || history.Version != source.PackageVersion {
		return nil, errors.New("agent: original encrypted installation identity differs")
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return nil, errors.New("agent: source resource inspection unavailable")
	}
	defer docker.Close()
	previous := DeploymentTask{ID: task.ID + "-source", ApplicationID: source.ApplicationID, AppKey: source.AppKey, Manifest: history.Manifest, Config: history.Config, Secrets: history.Secrets, Operation: "upgrade"}
	old := &DockerPackageBackend{Docker: docker, StateDirectory: directory}
	verifySource := func() error {
		if err := old.Inspect(ctx, previous, source); err != nil {
			return err
		}
		containers := 0
		for _, resource := range source.Resources {
			if resource.Kind != "container" {
				continue
			}
			containers++
			observed, err := docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
			if err != nil || observed.Container.State == nil || observed.Container.HostConfig == nil || observed.Container.State.Running || observed.Container.State.Restarting || observed.Container.State.Paused || observed.Container.State.Dead || observed.Container.State.Pid != 0 || observed.Container.HostConfig.AutoRemove || observed.Container.HostConfig.RestartPolicy.Name != "no" {
				return errors.New("agent: every original writer must be stopped with automatic restart disabled")
			}
		}
		if containers == 0 {
			return errors.New("agent: original stopped runtime evidence missing")
		}
		return nil
	}
	if err := verifySource(); err != nil {
		return nil, err
	}
	input, err := json.Marshal(task)
	if err != nil {
		return nil, err
	}
	inputDigest := sha256.Sum256(input)
	clear(input)
	inputSHA := hex.EncodeToString(inputDigest[:])
	stage, err := readNativeStage(directory, task.ApplicationID)
	if err == nil {
		if stage.SourceSHA256 != sourceSHA256 || stage.InputSHA256 != inputSHA || stage.Candidate.TaskID != task.ID || stage.Candidate.AppKey != task.AppKey || stage.Candidate.Runtime != "systemd" || stage.Candidate.RuntimeVersion != 2 {
			return nil, errors.New("agent: native handover scope changed; previous evidence retained")
		}
		if stage.Candidate.State != "staged" && !resumeReviewed {
			return nil, errors.New("agent: interrupted native staging requires explicit review before retry")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	} else {
		stage = &NativePackageStage{Version: 1, SourceSHA256: sourceSHA256, Source: source, SourceOriginal: bytes.Clone(original), SourceInstanceID: history.InstanceID, InputSHA256: inputSHA, Candidate: &InstanceResources{
			Version: 1, ApplicationID: task.ApplicationID, AppKey: task.AppKey, Runtime: "systemd", RuntimeVersion: 2,
			PackageVersion: task.Manifest.Version, PackageRevision: task.PackageRevision, ManifestSHA256: task.ManifestSHA256,
			AuthorizedCapabilities: slices.Clone(task.AuthorizedCapabilities), TaskID: task.ID, State: "staging",
		}}
	}
	native := &NativeServicesPackageBackend{Manager: manager, StateDirectory: directory}
	if err := native.Prepare(ctx, task, stage.Candidate); err != nil {
		return nil, err
	}
	if stage.DatabaseBackup == "" {
		// Reuse the package backup writer, with all source processes already
		// stopped. Never resume a source writer as a side effect of preparation.
		if err := old.Backup(ctx, previous, source); err != nil {
			return nil, err
		}
		backup := filepath.Join(directory, "packages", packageIdentity(task.ApplicationID), "native-source-"+packageIdentity(task.ID))
		if err := checkPackageParents(filepath.Join(backup, "agent.db")); err != nil {
			return nil, err
		}
		if err := os.Mkdir(backup, 0700); err != nil {
			return nil, errors.New("agent: source backup already exists; inspect before retry")
		}
		if _, err := e.Store.db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(backup, "agent.db")); err != nil {
			return nil, err
		}
		if err := os.Chmod(filepath.Join(backup, "agent.db"), 0600); err != nil {
			return nil, err
		}
		if err := writeHostFileAtomic(filepath.Join(backup, "agent.key"), e.Store.key, 0600); err != nil {
			return nil, err
		}
		stage.DatabaseBackup = backup
		stage.DatabaseSHA256, err = privateStageFileSHA256(filepath.Join(backup, "agent.db"))
		if err != nil {
			return nil, err
		}
		if err := writeNativeStage(directory, stage); err != nil {
			return nil, err
		}
	}
	for _, backup := range stage.Source.Backups {
		if backup.ID != previous.ID {
			continue
		}
		if err := verifyStageBackup(directory, previous, backup); err != nil {
			return nil, err
		}
	}
	if err := verifySource(); err != nil {
		return nil, err
	}
	if stage.Candidate.State != "staged" {
		stage.Candidate.State = "staging"
		if err := writeNativeStage(directory, stage); err != nil {
			return nil, err
		}
		if err := installNativePackage(ctx, manager, task, *native.prepared, stage.Candidate, func() error { return writeNativeStage(directory, stage) }, false); err != nil {
			stage.Candidate.State = "review-required"
			return nil, errors.Join(err, writeNativeStage(directory, stage))
		}
	}
	if err := native.Inspect(ctx, task, stage.Candidate); err != nil {
		return nil, err
	}
	for _, unit := range native.prepared.Plan.Units {
		if err := confirmNativeStageStopped(ctx, manager, unit.Name); err != nil {
			return nil, errors.New("agent: staged native service is not confirmed stopped")
		}
	}
	current, err := os.ReadFile(executor.receiptPath(task.ApplicationID))
	if err != nil || !bytes.Equal(current, original) {
		return nil, errors.New("agent: active installation changed during native staging")
	}
	stage.Candidate.State = "staged"
	return stage, writeNativeStage(directory, stage)
}

func confirmNativeStageStopped(ctx context.Context, manager SystemdHostApplicationManager, name string) error {
	args := []string{"show", name, "--property=LoadState,ActiveState,SubState,MainPID,ControlPID,UnitFileState"}
	var raw []byte
	var err error
	if manager.ReadCommand != nil {
		raw, err = manager.ReadCommand(ctx, "systemctl", args...)
	} else {
		raw, err = exec.CommandContext(ctx, "systemctl", args...).Output()
	}
	if err != nil || len(raw) > 16<<10 {
		return errors.New("agent: staged service state unavailable")
	}
	want := map[string]string{"LoadState": "loaded", "ActiveState": "inactive", "SubState": "dead", "MainPID": "0", "ControlPID": "0", "UnitFileState": "disabled"}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		expected, known := want[key]
		if !ok || !known || seen[key] || value != expected {
			return errors.New("agent: staged service must be inactive and disabled")
		}
		seen[key] = true
	}
	if len(seen) != len(want) {
		return errors.New("agent: staged service state incomplete")
	}
	return nil
}

func privateStageFileSHA256(path string) (string, error) {
	if err := checkPackageParents(path); err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() == 0 || info.Size() > 2<<30 {
		return "", errors.New("agent: private recovery file is missing or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Stream verification avoids allocating a full (up to 512 MiB) volume archive
// on a low-memory node. The existing bounded tar validator is reused.
func verifyStageBackup(directory string, task DeploymentTask, backup RuntimeBackup) error {
	expected := filepath.Join(directory, "packages", packageIdentity(task.ApplicationID), "backups", packageIdentity(backup.ID), packageIdentity(backup.Resource)+".tar")
	if backup.ID == "" || backup.Path != expected || checkPackageParents(expected) != nil {
		return errors.New("agent: source backup ownership differs")
	}
	info, err := os.Lstat(expected)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 512<<20 {
		return errors.New("agent: source backup is missing or unsafe")
	}
	file, err := os.Open(expected)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	source := io.TeeReader(file, digest)
	if err := validateBackupTarStream(source); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, source); err != nil {
		return err
	}
	if hex.EncodeToString(digest.Sum(nil)) != backup.SHA256 {
		return errors.New("agent: source backup integrity differs")
	}
	return nil
}
