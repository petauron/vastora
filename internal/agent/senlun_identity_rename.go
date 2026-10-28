package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/senulun"
)

// renameSenlunNativeIdentity is a one-time, offline local registration change.
// The caller must stop the Vastora Agent, verify target against the signed
// catalog, and coordinate the Center record before restarting the Agent. It
// never changes or restarts the monitoring service or its state directory.
func (e ApplicationExecutor) renameSenlunNativeIdentity(ctx context.Context, applicationID, expectedReceiptSHA256 string, target catalog.AppManifest) (*InstanceResources, error) {
	if e.Store == nil || applicationID == "" || target.ID != "senlun-agent" {
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
	receipt.AppKey, receipt.PackageRevision, receipt.ManifestSHA256 = newKey, canonicalTarget.PackageRevision, hex.EncodeToString(targetSHA[:])
	if err := executor.save(receipt); err != nil {
		return nil, errors.New("agent: Senlun resource registration requires review")
	}
	installation.AppKey, installation.Manifest = newKey, canonicalTarget
	if _, err := e.Store.RecordApplied(ctx, installation); err != nil {
		return nil, errors.New("agent: Senlun applied identity requires review")
	}
	if err := os.Remove(marker); err != nil {
		return nil, errors.New("agent: completed Senlun identity migration requires review")
	}
	return receipt, nil
}
