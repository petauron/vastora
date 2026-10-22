package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RetireLegacyExecution only changes the local execution receipt. Center's
// endpoint, subscription, identity and accounting records are never touched.
// The caller keeps the Agent stopped; the application-owned runtime stays up.
func (e ApplicationExecutor) RetireLegacyExecution(ctx context.Context, applicationID, nativeState string) (string, error) {
	if e.Store == nil || applicationID == "" || !filepath.IsAbs(nativeState) || filepath.Clean(nativeState) != nativeState {
		return "", errors.New("agent: explicit legacy identity and native state required")
	}
	e.Store.landingMutationMu.Lock()
	defer e.Store.landingMutationMu.Unlock()
	old, err := e.Store.AppliedInstallation(ctx, meridianKey)
	if err != nil || old.ApplicationID != applicationID {
		return "", errors.New("agent: retained legacy installation identity differs")
	}
	packages := PackageExecutor{StateDirectory: e.packageDirectory()}
	receipt, err := packages.ReadResources(applicationID)
	if err != nil || receipt.AppKey != meridianKey || receipt.Runtime != "docker" || receipt.State != "ready" {
		return "", errors.New("agent: ready legacy Docker ownership receipt required")
	}
	if receipt.IntegrationState == "execution-retired" {
		return "already-retired", nil
	}
	ready, err := e.verifyStoppedLegacyMeridian(ctx)
	if err != nil || !ready {
		return "", errors.Join(errors.New("agent: native installation and deliberately stopped legacy runtime required"), err)
	}
	native, err := e.Store.AppliedInstallation(ctx, "vastora-official/glivela-entry")
	if err != nil {
		return "", err
	}
	nativeReceipt, err := packages.ReadResources(native.ApplicationID)
	if err != nil {
		return "", err
	}
	owned, units := false, 0
	for _, resource := range nativeReceipt.Resources {
		if resource.Kind == "directory" && resource.Path == nativeState && resource.Persistent {
			owned = true
		}
		if resource.Kind == "unit" {
			out, err := exec.CommandContext(ctx, "systemctl", "is-active", resource.Name).Output()
			if err != nil || strings.TrimSpace(string(out)) != "active" {
				return "", errors.New("agent: native service is not active")
			}
			units++
		}
	}
	if !owned || units < 2 {
		return "", errors.New("agent: owned native entry and Xray services required")
	}
	proof, err := readRetirementPrivateFile(filepath.Join(nativeState, "entry-source.json"), 1<<20)
	if err != nil {
		return "", err
	}
	var source struct {
		Version       int    `json:"version"`
		ApplicationID string `json:"applicationId"`
		Checkpoint    string `json:"checkpoint"`
		SourceSHA256  string `json:"sourceSha256"`
	}
	if json.Unmarshal(proof, &source) != nil || source.Version != 1 || source.ApplicationID != applicationID || !filepath.IsAbs(source.Checkpoint) {
		return "", errors.New("agent: native handover proof does not match legacy identity")
	}
	checkpoint, err := readRetirementPrivateFile(source.Checkpoint, 32<<20)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(checkpoint)
	if hex.EncodeToString(digest[:]) != source.SourceSHA256 {
		return "", errors.New("agent: retained handover backup integrity differs")
	}
	backup, err := os.MkdirTemp(filepath.Dir(packages.receiptPath(applicationID)), "execution-retirement-")
	if err != nil {
		return "", err
	}
	if _, err := e.Store.db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(backup, "agent.db")); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Join(backup, "agent.db"), 0600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(backup, "agent.key"), e.Store.key, 0600); err != nil {
		return "", err
	}
	original, err := os.ReadFile(packages.receiptPath(applicationID))
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(backup, "resources.json"), original, 0600); err != nil {
		return "", err
	}
	manifest := make(map[string]string)
	for _, name := range []string{"agent.db", "agent.key", "resources.json"} {
		digest, err := privateStageFileSHA256(filepath.Join(backup, name))
		if err != nil {
			return "", err
		}
		manifest[name] = digest
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err := writeHostFileAtomic(filepath.Join(backup, "sha256.json"), encoded, 0600); err != nil {
		return "", err
	}
	receipt.IntegrationState = "execution-retired"
	if err := packages.save(receipt); err != nil {
		return "", err
	}
	return backup, nil
}

func readRetirementPrivateFile(path string, limit int64) ([]byte, error) {
	if err := checkPackageParents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errors.New("agent: bounded private retirement proof required")
	}
	return os.ReadFile(path)
}
