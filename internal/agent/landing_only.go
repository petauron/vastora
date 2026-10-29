package agent

import (
	"context"
	"database/sql"
	"errors"
)

// ValidateNodePurpose binds local runtime flags to the Center-issued enrollment.
// Existing ordinary nodes may still configure their optional gateway role.
func (s *Store) ValidateNodePurpose(ctx context.Context, capabilities Capabilities) error {
	var landingOnly bool
	err := s.db.QueryRowContext(ctx, `SELECT landing_only FROM control_plane_connection WHERE id=1`).Scan(&landingOnly)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if landingOnly != capabilities.LandingOnly ||
		(capabilities.LandingOnly && (capabilities.Docker || capabilities.Gateway || capabilities.Tunnel)) {
		return errors.New("agent: runtime purpose differs from Center enrollment")
	}
	return nil
}

func landingOnlyTaskAllowed(kind string) bool {
	switch kind {
	case "landing.server.apply", "agent.update", "agent.decommission", "node.security", "tailscale.update":
		return true
	default:
		return false
	}
}

// PurgeNativeLanding reuses the owned native lifecycle without opening Docker.
func PurgeNativeLanding(ctx context.Context) error {
	journal, err := readLandingHostJournal()
	if err != nil || journal == nil {
		return err
	}
	if journal.Revision == ^uint64(0) {
		return errors.New("agent: landing revision exhausted")
	}
	return RemoveLanding(ctx, journal.NodeID, journal.Revision+1)
}
