package center

import (
	"context"
	"database/sql"
	"github.com/petauron/vastora/internal/landing"
	"time"
)

const landingHealthFreshness = 45 * time.Second

func recordLandingHealth(ctx context.Context, tx *sql.Tx, nodeID string, health *landing.Health, now time.Time) error {
	if health == nil || health.Revision == 0 || health.Revision > 1<<62 || health.CheckedAt.IsZero() || health.CheckedAt.After(now.Add(5*time.Second)) || health.CheckedAt.Before(now.Add(-landingHealthFreshness)) {
		_, err := tx.ExecContext(ctx, `UPDATE landing_proxy_states SET health_ok=0 WHERE node_id=?`, nodeID)
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE landing_proxy_states SET health_revision=?,health_ok=?,health_checked_at=?,health_received_at=?
 WHERE node_id=? AND desired_revision=? AND applied_revision=? AND status='ready' AND json_extract(desired_json,'$.proxy') IS NOT NULL
 AND (health_revision<>? OR health_checked_at<=?)`, health.Revision, health.Healthy, health.CheckedAt.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), nodeID, health.Revision, health.Revision, health.Revision, health.CheckedAt.UTC().Format(time.RFC3339Nano))
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
