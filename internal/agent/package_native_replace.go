package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/moby/moby/client"
)

// Activate an explicitly staged installation through the ordinary updater.
// No resources are discovered/adopted by guessed name, no source container is
// deleted, and installed application configuration is never initialized again.
func (e ApplicationExecutor) replaceStagedNative(ctx context.Context, task DeploymentTask) (result ApplicationTaskResult, resultErr error) {
	if e.Store == nil || task.Operation != "install" || task.Manifest.Runtime == nil || task.Manifest.Runtime.Kind != "systemd" || task.Manifest.Runtime.Version != 2 {
		return result, errors.New("agent: staged native handover requires explicit installation authorization")
	}
	manager, ok := e.Host.(SystemdHostApplicationManager)
	if !ok {
		return result, errors.New("agent: native installation manager unavailable")
	}
	directory := e.packageDirectory()
	stage, err := readNativeStage(directory, task.ApplicationID)
	if err != nil || stage.SourceSHA256 != task.SourceReceiptSHA256 || stage.SourceInstanceID == "" || stage.Candidate.State != "staged" || stage.Candidate.AppKey != task.AppKey || stage.Candidate.PackageVersion != task.Manifest.Version || stage.Candidate.PackageRevision != task.PackageRevision || stage.Candidate.ManifestSHA256 != task.ManifestSHA256 {
		return result, errors.New("agent: verified staged native installation does not match authorization")
	}
	digest := sha256.Sum256(stage.SourceOriginal)
	var original InstanceResources
	if hex.EncodeToString(digest[:]) != task.SourceReceiptSHA256 || json.Unmarshal(stage.SourceOriginal, &original) != nil || original.ApplicationID != task.ApplicationID || original.Runtime != "docker" || original.State != "ready" || original.AppKey != stage.Source.AppKey || original.AppKey == task.AppKey || !slices.Equal(slices.Sorted(slices.Values(stage.Candidate.AuthorizedCapabilities)), slices.Sorted(slices.Values(task.AuthorizedCapabilities))) {
		return result, errors.New("agent: original handover evidence or reviewed permissions differ")
	}
	// The frozen database and key remain private recovery resources, never
	// substituted for the live database or attached to a newer native backup.
	expectedBackup := filepath.Join(directory, "packages", packageIdentity(task.ApplicationID), "native-source-"+packageIdentity(stage.Candidate.TaskID))
	if stage.DatabaseBackup != expectedBackup || checkPackageParents(filepath.Join(expectedBackup, "agent.db")) != nil {
		return result, errors.New("agent: frozen source database ownership differs")
	}
	for _, name := range []string{"agent.db", "agent.key"} {
		info, err := os.Lstat(filepath.Join(expectedBackup, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() == 0 {
			return result, errors.New("agent: complete private source recovery backup required")
		}
	}
	backupSHA, err := privateStageFileSHA256(filepath.Join(expectedBackup, "agent.db"))
	backupKey, keyErr := os.ReadFile(filepath.Join(expectedBackup, "agent.key"))
	keyMatches := keyErr == nil && bytes.Equal(backupKey, e.Store.key)
	clear(backupKey)
	if err != nil || backupSHA != stage.DatabaseSHA256 || !keyMatches {
		return result, errors.New("agent: frozen source database or encryption key integrity differs")
	}
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return result, err
	}
	defer docker.Close()
	previous := DeploymentTask{ID: stage.Candidate.TaskID + "-source", ApplicationID: task.ApplicationID, AppKey: original.AppKey}
	old := &DockerPackageBackend{Docker: docker, StateDirectory: directory}
	if err := old.Inspect(ctx, previous, &original); err != nil {
		return result, err
	}
	for _, resource := range original.Resources {
		if resource.Kind != "container" {
			continue
		}
		value, err := docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
		if err != nil || value.Container.State == nil || value.Container.HostConfig == nil || value.Container.State.Running || value.Container.State.Restarting || value.Container.State.Paused || value.Container.State.Dead || value.Container.State.Pid != 0 || value.Container.HostConfig.AutoRemove || value.Container.HostConfig.RestartPolicy.Name != "no" {
			return result, errors.New("agent: source writer stopped state changed before handover")
		}
	}
	for _, backup := range stage.Source.Backups {
		if backup.ID == previous.ID {
			if err := verifyStageBackup(directory, previous, backup); err != nil {
				return result, err
			}
		}
	}
	native := &NativeServicesPackageBackend{Manager: manager, StateDirectory: directory}
	if err := native.Prepare(ctx, task, stage.Candidate); err != nil {
		return result, err
	}
	// Compare every staged file with the signed recipe and reviewed inputs.
	// This includes application-owned bootstrap/certificate files: adoption is
	// not permission to change them. Ordinary later upgrades preserve edits.
	for _, file := range native.prepared.Files {
		resource := resourceNamed(stage.Candidate, file.Kind, file.LogicalName)
		if resource == nil || resource.Path != file.Path {
			return result, errors.New("agent: staged installation is incomplete")
		}
		snapshot, err := captureHostFile(manager.path(file.Path))
		if err != nil || !snapshot.Exists || !bytes.Equal(snapshot.Data, file.Data) {
			return result, errors.New("agent: staged file differs from reviewed installation inputs")
		}
	}
	executor := PackageExecutor{StateDirectory: directory, Backend: native}
	active, err := executor.ReadResources(task.ApplicationID)
	if err != nil {
		return result, err
	}
	if active.Runtime == "docker" {
		raw, err := os.ReadFile(executor.receiptPath(task.ApplicationID))
		if err != nil || !bytes.Equal(raw, stage.SourceOriginal) {
			return result, errors.New("agent: original active installation changed")
		}
		encoded, err := json.Marshal(stage.Candidate)
		if err != nil {
			return result, err
		}
		var replacement InstanceResources
		if json.Unmarshal(encoded, &replacement) != nil {
			return result, errors.New("agent: invalid staged registration")
		}
		// This is retained ownership, not a successful installation. Only the
		// normal updater may publish a ready receipt after real health checks.
		replacement.TaskID, replacement.State = task.ID, "retained"
		if err := executor.save(&replacement); err != nil {
			return result, uncertainTaskOutcome(err)
		}
	} else if active.Runtime != "systemd" || active.RuntimeVersion != 2 || active.AppKey != task.AppKey || active.TaskID != task.ID || active.ManifestSHA256 != task.ManifestSHA256 {
		return result, errors.New("agent: another installation already owns this application identity")
	}
	// From here a failure must use the existing reviewed execution-recovery
	// flow. Do not turn a partial installation into an automatic fresh install.
	defer func() {
		if resultErr != nil {
			resultErr = uncertainTaskOutcome(resultErr)
		}
	}()
	task.Operation = "upgrade"
	result, resultErr = executor.Deploy(ctx, task)
	if resultErr == nil {
		result.SourceReceiptSHA256 = stage.SourceSHA256
	}
	return result, resultErr
}

// Remove only the superseded startup pointer after the replacement has been
// recorded. Its frozen encrypted database, resource receipt and all history
// remain intact. Do not call product-specific RemoveApplied cleanup here.
func (s *Store) completeNativeReplacement(ctx context.Context, task DeploymentTask) error {
	if task.SourceReceiptSHA256 == "" {
		return nil
	}
	stage, err := readNativeStage(s.dataDir, task.ApplicationID)
	if err != nil || stage.SourceSHA256 != task.SourceReceiptSHA256 || stage.SourceInstanceID == "" || stage.Source.AppKey == task.AppKey {
		return errors.New("agent: native handover startup evidence missing")
	}
	current, err := s.AppliedInstallation(ctx, task.AppKey)
	if err != nil || current.InstanceID != task.ID || current.ApplicationID != task.ApplicationID {
		return errors.New("agent: replacement startup state is not committed")
	}
	var instance string
	err = s.db.QueryRowContext(ctx, `SELECT instance_id FROM applied_installations WHERE app_key=?`, stage.Source.AppKey).Scan(&instance)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || instance != stage.SourceInstanceID {
		return errors.New("agent: original startup identity changed during handover")
	}
	removed, err := s.db.ExecContext(ctx, `DELETE FROM applied_installations WHERE instance_id=? AND app_key=?`, instance, stage.Source.AppKey)
	if err != nil {
		return err
	}
	count, err := removed.RowsAffected()
	if err != nil || count != 1 {
		return errors.New("agent: original startup identity changed before retirement")
	}
	return nil
}
