package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func recordLandingServerHealth(ctx context.Context, tx *sql.Tx, id string, h *landing.ServerHealth, now time.Time) error {
	raw := []byte(`{}`)
	if h != nil && h.Revision <= 1<<62 && h.Fresh(now, h.Revision) {
		var err error
		raw, err = json.Marshal(h)
		if err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE landing_server_states SET health_json=?,health_received_at=? WHERE node_id=?`, raw, now.UTC().Format(time.RFC3339Nano), id)
	return err
}

func nativeLandingHealthy(raw []byte, received string, revision uint64, now time.Time) bool {
	var h landing.ServerHealth
	at, err := time.Parse(time.RFC3339Nano, received)
	return err == nil && !at.After(now.Add(5*time.Second)) && now.Sub(at) <= landing.ServerHealthFreshness && json.Unmarshal(raw, &h) == nil && h.Healthy && h.Fresh(now, revision)
}
