package center

import (
	"context"
	"database/sql"
	"errors"
)

// The claimed outcome is evidence, not authority to mark a rejected business
// projection successful. Check the exact task attempt inside its transaction.
func validateExecutionProjection(ctx context.Context, tx *sql.Tx, id string, succeeded bool) error {
	var kind, taskID, agentID string
	var attempt int64
	if err := tx.QueryRowContext(ctx, `SELECT kind,task_id,agent_id,attempt FROM task_executions WHERE id=?`, id).Scan(&kind, &taskID, &agentID, &attempt); err != nil {
		return err
	}
	var query string
	args := []any{taskID, agentID, attempt}
	switch kind {
	case "application.apply":
		query = `SELECT state FROM deployments WHERE id=? AND agent_id=? AND attempt=?`
	case "application.command":
		query = `SELECT state FROM application_commands WHERE id=? AND agent_id=? AND attempt=?`
	case "agent.update":
		query = `SELECT state FROM agent_updates WHERE id=? AND agent_id=? AND attempt=?`
	case "agent.decommission":
		query = `SELECT state FROM agent_decommissions WHERE agent_id=? AND attempt=?`
		args = []any{agentID, attempt}
	case "landing.proxy.apply":
		query = `SELECT status FROM landing_proxy_states WHERE node_id=? AND attempt=?`
		args = []any{agentID, attempt}
	case "landing.server.apply":
		query = `SELECT status FROM landing_server_states WHERE node_id=? AND attempt=?`
		args = []any{agentID, attempt}
	case "gateway.routes.apply":
		query = `SELECT status FROM gateway_states WHERE gateway_node_id=? AND attempt=?`
		args = []any{agentID, attempt}
	case "gateway.component.apply":
		query = `SELECT status FROM gateway_components WHERE gateway_node_id=? AND attempt=?`
		args = []any{agentID, attempt}
	case "node.listener.apply":
		query = `SELECT status FROM node_listener_states WHERE node_id=? AND attempt=?`
		args = []any{agentID, attempt}
	case "tunnel.state.apply":
		query = `SELECT status FROM cloudflare_tunnels WHERE agent_id=? AND attempt=?`
		args = []any{agentID, attempt}
	default:
		return errors.New("center: unsupported execution projection")
	}
	var state string
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&state); err != nil {
		return err
	}
	if succeeded && (state == "succeeded" || state == "ready" || state == "stopped") || !succeeded && state == "failed" {
		return nil
	}
	return errors.New("center: execution result does not match the business projection")
}
