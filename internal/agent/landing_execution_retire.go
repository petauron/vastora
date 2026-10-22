package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func requireNativeLandingExecution() error {
	journal, err := readLandingHostJournal()
	if err != nil {
		return err
	}
	if journal != nil && journal.ExecutionRetired {
		return errors.New("agent: legacy landing execution retired; retained data is read-only")
	}
	return nil
}

// RetireLandingExecution preserves the original installation and journals.
// Agent remains usable for generic application lifecycle management, but must
// no longer restore, monitor, mutate or remove the old landing service.
func (e ApplicationExecutor) RetireLandingExecution(ctx context.Context, nativeState string) (string, error) {
	if e.Store == nil || !filepath.IsAbs(nativeState) || filepath.Clean(nativeState) != nativeState {
		return "", errors.New("agent: explicit owned native landing state required")
	}
	e.Store.landingMutationMu.Lock()
	defer e.Store.landingMutationMu.Unlock()
	journal, err := readLandingHostJournal()
	if err != nil || journal == nil || journal.Removed {
		return "", errors.New("agent: retained landing ownership required")
	}
	connection, err := e.Store.Connection(ctx)
	if err != nil || connection.AgentID != journal.NodeID {
		return "", errors.New("agent: landing identity differs")
	}
	if journal.ExecutionRetired {
		return "already-retired", nil
	}
	installation, err := e.Store.AppliedInstallation(ctx, "vastora-official/glivela-node")
	if err != nil {
		return "", err
	}
	packages := PackageExecutor{StateDirectory: e.packageDirectory()}
	receipt, err := packages.ReadResources(installation.ApplicationID)
	if err != nil || receipt.Runtime != "systemd" || receipt.State != "ready" {
		return "", errors.New("agent: ready native landing ownership required")
	}
	owned, units := false, 0
	for _, resource := range receipt.Resources {
		if resource.Kind == "directory" && resource.Path == nativeState && resource.Persistent {
			owned = true
		}
		if resource.Kind == "unit" {
			out, err := exec.CommandContext(ctx, "systemctl", "is-active", resource.Name).Output()
			if err != nil || strings.TrimSpace(string(out)) != "active" {
				return "", errors.New("agent: native service inactive")
			}
			units++
		}
	}
	if !owned || units < 2 {
		return "", errors.New("agent: owned native services required")
	}
	raw, err := readRetirementPrivateFile(filepath.Join(nativeState, "legacy-cutover.json"), 32<<20)
	if err != nil {
		return "", err
	}
	var cutover struct {
		Version          int       `json:"version"`
		NodeID           string    `json:"nodeId"`
		Phase            string    `json:"phase"`
		Checkpoint       string    `json:"checkpoint"`
		AcceptedSnapshot string    `json:"acceptedSnapshot"`
		AcceptedAt       time.Time `json:"acceptedAt"`
	}
	if json.Unmarshal(raw, &cutover) != nil || cutover.Version != 1 || cutover.NodeID != journal.NodeID || cutover.Phase != "accepted" || !filepath.IsAbs(cutover.Checkpoint) || cutover.AcceptedSnapshot == "" || cutover.AcceptedAt.IsZero() {
		return "", errors.New("agent: accepted landing handover required")
	}
	checkpoint, err := readRetirementPrivateFile(cutover.Checkpoint, 96<<20)
	if err != nil {
		return "", err
	}
	var saved struct {
		Version int    `json:"version"`
		NodeID  string `json:"nodeId"`
		Journal []byte `json:"journal"`
	}
	original, err := readLandingOwnedFile(landingHostJournalPath)
	if err != nil || json.Unmarshal(checkpoint, &saved) != nil || saved.Version != 1 || saved.NodeID != journal.NodeID || !bytes.Equal(saved.Journal, original) {
		return "", errors.New("agent: retained landing checkpoint differs")
	}
	for _, unit := range []string{"vastora-landing.service", "vastora-landing-firewall.service"} {
		out, err := exec.CommandContext(ctx, "systemctl", "show", unit, "--property=ActiveState,MainPID,ControlPID,UnitFileState").Output()
		if err != nil {
			return "", err
		}
		fields := map[string]string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			k, v, ok := strings.Cut(line, "=")
			if ok {
				fields[k] = v
			}
		}
		bootDisabled := fields["UnitFileState"] == "disabled" || unit == "vastora-landing-firewall.service" && fields["UnitFileState"] == "static"
		if fields["ActiveState"] != "inactive" || fields["MainPID"] != "0" || fields["ControlPID"] != "0" || !bootDisabled {
			return "", errors.New("agent: old landing must be stopped and boot-disabled")
		}
	}
	backup, err := os.MkdirTemp(filepath.Dir(landingHostJournalPath), "landing-execution-retirement-")
	if err != nil {
		return "", err
	}
	manifest := map[string]string{}
	for name, contents := range map[string][]byte{"landing-host.json": original, "accepted-cutover.json": raw, "snapshot.json": checkpoint} {
		if err := os.WriteFile(filepath.Join(backup, name), contents, 0600); err != nil {
			return "", err
		}
		digest := sha256.Sum256(contents)
		manifest[name] = hex.EncodeToString(digest[:])
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	if err := writeHostFileAtomic(filepath.Join(backup, "sha256.json"), encoded, 0600); err != nil {
		return "", err
	}
	journal.ExecutionRetired = true
	if err := saveLandingHostJournal(journal); err != nil {
		return "", err
	}
	return backup, nil
}
