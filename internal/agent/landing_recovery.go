package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// An update snapshot is private, bounded by landingPaths/readLandingOwnedFile,
// and independent of Center availability. It never adopts foreign host files.
type landingRecovery struct {
	directory string
	journal   *landingHostJournal
	stopped   bool
}

func prepareLandingRecovery(ctx context.Context, journal *landingHostJournal) (*landingRecovery, error) {
	r := &landingRecovery{}
	if journal.Removed || journal.Policy == nil || journal.PreviousPolicy != nil || len(journal.PendingFiles) != 0 {
		return r, nil
	}
	state, err := landingCommand(ctx, "systemctl", "show", "-p", "ActiveState", "--value", "vastora-landing.service")
	if err != nil {
		return nil, err
	}
	if state != "active" {
		return r, nil
	}
	if err := journal.Policy.Verify(ctx); err != nil {
		return nil, err // Do not stop a working daemon on ownership drift.
	}
	encoded, err := json.Marshal(journal)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &r.journal); err != nil {
		return nil, err
	}
	r.directory, err = os.MkdirTemp(filepath.Dir(landingHostJournalPath), ".landing-update-")
	if err != nil {
		return nil, err
	}
	for i, path := range landingPaths() {
		data, err := readLandingOwnedFile(path)
		if err == nil && landingDigest(data) != journal.Files[path] {
			err = errors.New("agent: previous landing files are incomplete")
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(r.directory, strconv.Itoa(i)), data, 0o600)
		}
		if err != nil {
			r.cleanup()
			return nil, err
		}
	}
	return r, nil
}

func (r *landingRecovery) cleanup() {
	if r != nil && r.directory != "" {
		_ = os.RemoveAll(r.directory)
	}
}

func (r *landingRecovery) restore(ctx context.Context, current *landingHostJournal) error {
	if err := verifyLandingFiles(current); err != nil {
		return err
	}
	if err := stopLandingService(ctx); err != nil {
		return err
	}
	policy := current.Policy
	previous := r.journal.Policy
	if policy == nil || policy.Address != previous.Address || policy.EgressIP != previous.EgressIP || policy.Interface != previous.Interface || policy.UID != previous.UID {
		return errors.New("agent: landing binding changed; refusing to restore a different exit")
	}
	// Restore the previous daemon, not revoked source access. The current
	// firewall remains authoritative even if its allowlist has narrowed.
	if err := policy.Install(ctx); err != nil {
		return err
	}
	activePolicy, _ := json.Marshal(policy)
	for _, obsolete := range []*landing.ServerFirewall{previous, current.PreviousPolicy} {
		encoded, _ := json.Marshal(obsolete)
		if obsolete != nil && string(encoded) != string(activePolicy) {
			if err := obsolete.Remove(ctx); err != nil {
				return err
			}
		}
	}
	for i, path := range landingPaths() {
		data, err := os.ReadFile(filepath.Join(r.directory, strconv.Itoa(i)))
		if err != nil || landingDigest(data) != r.journal.Files[path] {
			return errors.New("agent: landing recovery snapshot is invalid")
		}
		mode := os.FileMode(0o644)
		if path == landingBinaryPath {
			mode = 0o755
		}
		if err := writeHostFileAtomic(path, data, mode); err != nil {
			return err
		}
	}
	// Keep the attempted revision fence; the restored runtime is NOT reported
	// as a successful application of that revision. Center receives the error.
	r.journal.Revision, r.journal.PlanDigest = current.Revision, current.PlanDigest
	r.journal.Policy = policy
	if err := saveLandingHostJournal(r.journal); err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "systemctl", "start", "vastora-landing.service"); err != nil {
		return err
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile("/proc/net/tcp")
		if err == nil && landingListenerPresent(data, r.journal.Policy.Address, r.journal.UID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("agent: previous landing listener did not become ready")
		case <-ticker.C:
		}
	}
}
