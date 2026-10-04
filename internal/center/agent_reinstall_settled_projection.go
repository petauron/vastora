package center

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"

	"github.com/petauron/vastora/internal/gateway"
)

// Preserve historical rows and their failure evidence. Only an exact explicit
// abandonment, or a never-installed empty gateway projection, is not open work.
func reinstallSettledProjection(ctx context.Context, tx *sql.Tx, kind, agentID, state string, attempt, revision int64, intent string) (bool, error) {
	if kind == "landing.proxy.apply" && state == "failed" && attempt > 0 && revision > 0 {
		var abandoned bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND task_id=? AND kind=? AND attempt=? AND disposition='abandon')`, agentID, landingProxyTaskID(agentID, revision), kind, attempt).Scan(&abandoned)
		return abandoned, err
	}
	if kind != "gateway.routes.apply" || state != "pending" || attempt != 0 || revision <= 0 {
		return false, nil
	}
	var fields []json.RawMessage
	var encoded string
	var applied int64
	if json.Unmarshal([]byte(intent), &fields) != nil || len(fields) != 2 || json.Unmarshal(fields[0], &encoded) != nil || json.Unmarshal(fields[1], &applied) != nil || applied != 0 {
		return false, nil
	}
	var desired gateway.DesiredState
	decoder := json.NewDecoder(bytes.NewBufferString(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&desired) != nil || desired.Validate() != nil || desired.Revision != revision || len(desired.Routes) != 0 || len(desired.Listeners) != 0 || desired.SharedHTTPS != nil {
		return false, nil
	}
	var unused bool
	err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM gateway_components WHERE gateway_node_id=?) AND NOT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND kind='gateway.routes.apply')`, agentID, agentID).Scan(&unused)
	return unused, err
}
