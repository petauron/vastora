package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

const landingHealthFreshness = 45 * time.Second

func recordLandingHealth(ctx context.Context, tx *sql.Tx, nodeID string, health *landing.Health, now time.Time) error {
	if err := recordLandingPeerHealth(ctx, tx, nodeID, health, now); err != nil {
		return err
	}
	if health == nil || health.Revision == 0 || health.Revision > 1<<62 || health.CheckedAt.IsZero() || health.CheckedAt.After(now.Add(5*time.Second)) || health.CheckedAt.Before(now.Add(-landingHealthFreshness)) {
		_, err := tx.ExecContext(ctx, `UPDATE landing_proxy_states SET health_ok=0 WHERE node_id=?`, nodeID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE landing_proxy_states SET health_revision=?,health_ok=?,health_checked_at=?,health_received_at=?
 WHERE node_id=? AND desired_revision=? AND applied_revision=? AND status='ready' AND json_extract(desired_json,'$.proxy') IS NOT NULL
 AND (health_revision<>? OR health_checked_at<=?)`, health.Revision, health.Healthy, health.CheckedAt.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), nodeID, health.Revision, health.Revision, health.Revision, health.CheckedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func recordLandingPeerHealth(ctx context.Context, tx *sql.Tx, nodeID string, health *landing.Health, now time.Time) error {
	var desiredJSON []byte
	var desiredRevision, appliedRevision uint64
	var status string
	err := tx.QueryRowContext(ctx, `SELECT desired_json,desired_revision,applied_revision,status FROM landing_proxy_states WHERE node_id=?`, nodeID).Scan(&desiredJSON, &desiredRevision, &appliedRevision, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	clear := func() error {
		_, err := tx.ExecContext(ctx, `UPDATE landing_proxy_states SET peer_health_json='[]' WHERE node_id=?`, nodeID)
		return err
	}
	var desired landing.DesiredState
	if json.Unmarshal(desiredJSON, &desired) != nil || desired.Validate() != nil || desired.Revision != desiredRevision || appliedRevision != desiredRevision || status != "ready" {
		return clear()
	}
	expected := map[string]landing.PeerIdentity{}
	for _, use := range desired.PeerUses() {
		if use.Active {
			expected[use.Peer.ID] = use.Peer
		}
	}
	reported := map[string]landing.PeerHealth{}
	validReport := true
	if health != nil {
		for _, peer := range health.Peers {
			want, ok := expected[peer.Peer.ID]
			if !ok || want != peer.Peer || peer.Revision != desiredRevision || peer.CheckedAt.IsZero() || peer.CheckedAt.After(now.Add(5*time.Second)) || peer.CheckedAt.Before(now.Add(-landingHealthFreshness)) || (peer.State != "healthy" && peer.State != "blocked") || len(peer.Reason) > 128 || strings.TrimSpace(peer.Reason) != peer.Reason {
				validReport = false
				break
			}
			if _, duplicate := reported[peer.Peer.ID]; duplicate {
				validReport = false
				break
			}
			reported[peer.Peer.ID] = peer
		}
	}
	if !validReport {
		reported = map[string]landing.PeerHealth{}
	}
	peers := make([]landing.PeerHealth, 0, len(expected))
	for id, peer := range expected {
		value, ok := reported[id]
		if !ok {
			value = landing.PeerHealth{Peer: peer, Revision: desiredRevision, State: "blocked", Reason: "runtime_unreported"}
		}
		peers = append(peers, value)
	}
	slices.SortFunc(peers, func(a, b landing.PeerHealth) int { return strings.Compare(a.Peer.ID, b.Peer.ID) })
	raw, err := json.Marshal(peers)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE landing_proxy_states SET peer_health_json=? WHERE node_id=? AND desired_revision=? AND applied_revision=? AND status='ready'`, raw, nodeID, desiredRevision, desiredRevision)
	return err
}

func landingConnectionStatus(enabled bool, configuration string, revision, healthRevision uint64, healthy bool, checked, received string, now time.Time) string {
	if !enabled && configuration == "stopped" {
		return "disabled"
	}
	if configuration == "pending" || configuration == "applying" {
		return "pending"
	}
	if !enabled || configuration != "ready" || revision != healthRevision || !healthy {
		return "unhealthy"
	}
	checkedAt, err := time.Parse(time.RFC3339Nano, checked)
	if err != nil {
		return "unhealthy"
	}
	receivedAt, err := time.Parse(time.RFC3339Nano, received)
	if err != nil {
		return "unhealthy"
	}
	if now.Sub(checkedAt) > landingHealthFreshness || now.Sub(receivedAt) > landingHealthFreshness || checkedAt.After(now.Add(5*time.Second)) || receivedAt.After(now.Add(5*time.Second)) {
		return "unhealthy"
	}
	return "healthy"
}
