package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/secret"
	"github.com/petauron/vastora/internal/senulun"
)

// renameSenlunNativeIdentity is a one-time authenticated registration change.
// The caller must deliver the target from the verified signed catalog and let
// Center commit the matching result. It never changes or restarts the
// monitoring service or its state directory.
func (e ApplicationExecutor) renameSenlunNativeIdentity(ctx context.Context, applicationID, migrationTaskID, expectedReceiptSHA256 string, target catalog.AppManifest) (*InstanceResources, error) {
	if e.Store == nil || applicationID == "" || migrationTaskID == "" || target.ID != "senlun-agent" {
		return nil, errors.New("agent: Senlun identity migration requires an existing agent instance")
	}
	const oldKey = "vastora-official/pulse-agent"
	const newKey = "vastora-official/senlun-agent"
	installation, err := e.Store.AppliedInstallation(ctx, oldKey)
	if err != nil || installation.ApplicationID != applicationID {
		return nil, errors.New("agent: original applied Senlun identity is unavailable")
	}
	if err := senulun.ValidateIdentityRename(installation.Manifest, target); err != nil {
		return nil, err
	}
	if _, err := e.Store.AppliedInstallation(ctx, newKey); !errors.Is(err, errApplicationNotInstalled) {
		return nil, errors.New("agent: target application identity already exists or is invalid")
	}
	directory := e.packageDirectory()
	executor := PackageExecutor{StateDirectory: directory}
	receipt, err := executor.ReadResources(applicationID)
	if err != nil || receipt.AppKey != oldKey || receipt.Runtime != "systemd" || receipt.State != "ready" || receipt.TaskID != installation.InstanceID || receipt.PackageVersion != installation.Version || receipt.PackageRevision != installation.Manifest.PackageRevision {
		return nil, errors.New("agent: original Senlun resource ownership requires review")
	}
	rawReceipt, err := os.ReadFile(executor.receiptPath(applicationID))
	if err != nil {
		return nil, err
	}
	receiptSHA := sha256.Sum256(rawReceipt)
	if hex.EncodeToString(receiptSHA[:]) != expectedReceiptSHA256 {
		return nil, errors.New("agent: original Senlun resource receipt changed")
	}
	original, err := catalog.CanonicalAppManifest(installation.Manifest)
	if err != nil {
		return nil, err
	}
	rawOriginal, err := json.Marshal(original)
	if err != nil {
		return nil, err
	}
	originalSHA := sha256.Sum256(rawOriginal)
	if receipt.ManifestSHA256 != hex.EncodeToString(originalSHA[:]) {
		return nil, errors.New("agent: original Senlun recipe does not match its resource receipt")
	}
	unit := resourceNamed(receipt, "unit", "service")
	binary := resourceNamed(receipt, "file", "artifact:senlun-agent")
	if unit == nil || unit.Name != "vastora-senlun-agent.service" || binary == nil || binary.Name != "senlun-agent" {
		return nil, errors.New("agent: rename the owned Senlun unit and executable before changing its catalog identity")
	}
	backend := e.PackageBackend
	if backend == nil {
		manager, ok := e.Host.(SystemdHostApplicationManager)
		if !ok {
			return nil, errors.New("agent: systemd ownership inspection is unavailable")
		}
		backend = &SystemdPackageBackend{Manager: manager, StateDirectory: directory}
	}
	oldTask := DeploymentTask{ApplicationID: applicationID, AppKey: oldKey, Operation: "upgrade", Manifest: original}
	if err := backend.Inspect(ctx, oldTask, receipt); err != nil {
		return nil, err
	}
	canonicalTarget, err := catalog.CanonicalAppManifest(target)
	if err != nil {
		return nil, err
	}
	rawTarget, err := json.Marshal(canonicalTarget)
	if err != nil {
		return nil, err
	}
	targetSHA := sha256.Sum256(rawTarget)
	marker := filepath.Join(filepath.Dir(executor.receiptPath(applicationID)), "senlun-identity.pending")
	intent, err := json.Marshal(struct {
		ApplicationID string `json:"applicationId"`
		SourceSHA     string `json:"sourceReceiptSha256"`
		TargetSHA     string `json:"targetManifestSha256"`
	}{applicationID, expectedReceiptSHA256, hex.EncodeToString(targetSHA[:])})
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("agent: unfinished Senlun identity migration requires review")
	}
	_, writeErr := file.Write(intent)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return nil, errors.New("agent: Senlun identity migration intent requires review")
	}
	// A partial write leaves the marker in place. Subsequent package operations
	// fail closed; the monitoring process and its credentials remain untouched.
	receipt.AppKey, receipt.TaskID, receipt.PackageRevision, receipt.ManifestSHA256 = newKey, migrationTaskID, canonicalTarget.PackageRevision, hex.EncodeToString(targetSHA[:])
	if err := executor.save(receipt); err != nil {
		return nil, errors.New("agent: Senlun resource registration requires review")
	}
	if err := e.Store.renameSenlunApplied(ctx, installation, migrationTaskID, canonicalTarget); err != nil {
		return nil, errors.New("agent: Senlun applied identity requires review")
	}
	if err := os.Remove(marker); err != nil {
		return nil, errors.New("agent: completed Senlun identity migration requires review")
	}
	return receipt, nil
}

// Preserve the encrypted application values while atomically replacing the old
// startup registration with the real migration task identity.
func (s *Store) renameSenlunApplied(ctx context.Context, previous AppliedInstallation, taskID string, target catalog.AppManifest) error {
	config, err := canonicalJSONObject(previous.Config)
	if err != nil {
		return err
	}
	secrets, err := canonicalJSONObject(previous.Secrets)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(sealedApplicationState{ApplicationID: previous.ApplicationID, Config: config, Secrets: secrets, ServiceAddress: previous.ServiceAddress, Manifest: target, ApplicationRole: previous.ApplicationRole})
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, encoded, applicationStateContext(taskID))
	clear(encoded)
	if err != nil {
		return err
	}
	configSHA := sha256.Sum256(config)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldCount, newCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM applied_installations WHERE app_key=? AND instance_id=?`, previous.AppKey, previous.InstanceID).Scan(&oldCount); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM applied_installations WHERE app_key=? OR instance_id=?`, "vastora-official/senlun-agent", taskID).Scan(&newCount); err != nil {
		return err
	}
	if oldCount != 1 || newCount != 0 {
		return errors.New("agent: Senlun applied registration changed during migration")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO applied_installations(instance_id,app_key,version,sealed_state,config_hash,applied_at) VALUES(?,?,?,?,?,?)`, taskID, "vastora-official/senlun-agent", target.Version, sealed, hex.EncodeToString(configSHA[:]), s.now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM applied_installations WHERE app_key=? AND instance_id=?`, previous.AppKey, previous.InstanceID); err != nil {
		return err
	}
	return tx.Commit()
}

func senlunNativeIdentityTask(task DeploymentTask) bool {
	return task.AppKey == "vastora-official/senlun-agent" && task.SourceReceiptSHA256 != "" && task.Operation == "install"
}

func (s *Store) completeSenlunNativeIdentity(ctx context.Context, task DeploymentTask) error {
	installed, err := s.AppliedInstallation(ctx, task.AppKey)
	if err != nil || installed.InstanceID != task.ID || installed.ApplicationID != task.ApplicationID {
		return errors.New("agent: Senlun identity migration was not committed")
	}
	receipt, err := (PackageExecutor{StateDirectory: s.dataDir}).ReadResources(task.ApplicationID)
	if err != nil || receipt.AppKey != task.AppKey || receipt.TaskID != task.ID || receipt.ManifestSHA256 != task.ManifestSHA256 || receipt.PackageRevision != task.PackageRevision || receipt.State != "ready" {
		return errors.New("agent: Senlun migrated resource receipt is unavailable")
	}
	return nil
}
