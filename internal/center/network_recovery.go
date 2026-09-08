package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

// Restoring a profile is not a health verdict. Only after a fresh application
// observation may a network-degraded entry return to the normal verifier.
func publicationsAfterNetworkRecovery(ctx context.Context, tx *sql.Tx, now time.Time) ([]publicationVerificationTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.desired_revision FROM publications p
		JOIN services s ON s.id = p.service_id JOIN applications a ON a.id = s.application_id
		JOIN agent_network_profiles n ON n.agent_id = a.node_id
		WHERE p.status = 'degraded' AND p.last_error = 'Node network is recovering'
		AND p.action_required = 0 AND s.status = 'ready' AND s.last_error = ''`)
	if err != nil {
		return nil, err
	}
	var targets []publicationVerificationTarget
	for rows.Next() {
		var target publicationVerificationTarget
		if err := rows.Scan(&target.id, &target.revision); err != nil {
			rows.Close()
			return nil, err
		}
		targets = append(targets, target)
	}
	readErr := rows.Err()
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if readErr != nil {
		return nil, readErr
	}
	for _, target := range targets {
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'applying', last_error = '', updated_at = ? WHERE id = ?`, now.UTC().Format(time.RFC3339Nano), target.id); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

// Only a profile confirmed for this exact Agent identity is eligible for
// automatic recovery. It remains absent from the active table while invalid.
func recoverableNetworkProfile(ctx context.Context, tx *sql.Tx, agentID string) (*networking.Profile, error) {
	var encoded []byte
	err := tx.QueryRowContext(ctx, `SELECT r.profile_json FROM agent_network_profile_recovery r
		JOIN agents a ON a.id = r.agent_id AND a.x25519_public_key = r.public_key
		WHERE r.agent_id = ? AND a.status = 'active'`, agentID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var profile networking.Profile
	if err := json.Unmarshal(encoded, &profile); err != nil {
		return nil, errors.New("center: retained network profile is invalid")
	}
	return &profile, nil
}

func saveNetworkProfile(ctx context.Context, tx *sql.Tx, agentID string, input networking.Profile) error {
	enabledJSON, err := json.Marshal(input.EnabledKinds)
	if err != nil {
		return err
	}
	verifiedAt := ""
	if !input.PublicVerifiedAt.IsZero() {
		verifiedAt = input.PublicVerifiedAt.Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_network_profiles(agent_id, service_address, lan_address, headscale_address, public_address, public_bind_address, public_mode, enabled_kinds_json, direct_public, public_verified_at, confirmed_at, candidate_observed_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id) DO UPDATE SET service_address = excluded.service_address, lan_address = excluded.lan_address, headscale_address = excluded.headscale_address, public_address = excluded.public_address, public_bind_address = excluded.public_bind_address, public_mode = excluded.public_mode, enabled_kinds_json = excluded.enabled_kinds_json, direct_public = excluded.direct_public, public_verified_at = excluded.public_verified_at, confirmed_at = excluded.confirmed_at, candidate_observed_at = excluded.candidate_observed_at`, agentID, input.ServiceAddress, input.LANAddress, input.HeadscaleAddress, input.PublicAddress, input.PublicBindAddress, input.PublicMode, enabledJSON, input.DirectPublic, verifiedAt, input.ConfirmedAt.Format(time.RFC3339Nano), input.CandidateObserved.Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("center: save network profile: %w", err)
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM agent_network_profile_recovery WHERE agent_id = ?`, agentID)
	return err
}

// A successful deployment stores the actual binding, unlike a guessed address
// or an inbound observation. This permits explicit repair of profiles lost by
// older releases, but never an address change under a running application.
func retainedApplicationBindingMatches(ctx context.Context, tx *sql.Tx, agentID, address string) (bool, error) {
	var count, mismatches int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN COALESCE((
		SELECT d.service_address FROM deployments d WHERE d.application_id = a.id
		AND d.agent_id = a.node_id AND d.state = 'succeeded' AND d.operation <> 'uninstall'
		ORDER BY d.created_at DESC, d.id DESC LIMIT 1), '') <> ? THEN 1 ELSE 0 END), 0)
		FROM applications a WHERE a.node_id = ? AND a.status NOT IN ('stopped', 'failed')`, address, agentID).Scan(&count, &mismatches)
	return count > 0 && mismatches == 0, err
}
