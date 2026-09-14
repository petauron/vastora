package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

func (s *Store) prepareTunnelExecution(ctx context.Context, agentID, sessionID, executionID string) error {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed_task FROM task_executions WHERE id=? AND agent_id=? AND session_id=? AND kind='tunnel.state.apply'`, executionID, agentID, sessionID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-task:"+executionID))
	if err != nil {
		return err
	}
	var task AgentTask
	if json.Unmarshal(raw, &task) != nil || task.TunnelState == nil || task.TunnelState.Revision != task.Revision {
		return errors.New("center: invalid authorized Tunnel task")
	}
	if task.TunnelState.Status == "stopped" {
		return nil
	}
	// Serialize with publication updates/deletions; never hold SQLite over HTTP.
	s.publicationCleanupMu.Lock()
	defer s.publicationCleanupMu.Unlock()
	current := func() (string, error) {
		var tunnelID string
		err := s.db.QueryRowContext(ctx, `SELECT t.tunnel_id FROM cloudflare_tunnels t JOIN task_executions e ON e.agent_id=t.agent_id
			WHERE e.id=? AND e.session_id=? AND e.state='running' AND e.disposition='' AND e.expires_at>?
			AND t.desired_revision=? AND t.attempt=? AND t.status='applying'
			AND EXISTS(SELECT 1 FROM agent_execution_sessions a WHERE a.agent_id=e.agent_id AND a.session_id=e.session_id)`,
			executionID, sessionID, s.now().UTC().Format(time.RFC3339Nano), task.Revision, task.Attempt).Scan(&tunnelID)
		return tunnelID, err
	}
	tunnelID, err := current()
	if err != nil {
		return err
	}
	client, err := s.cloudflare(ctx)
	if err != nil {
		return err
	}
	if err := client.syncTunnelConfiguration(ctx, tunnelID, task.TunnelState.Ingress); err != nil {
		return err
	}
	_, err = current()
	return err
}

// A connector restart is not evidence that its remotely managed ingress changed.
// Read/modify/write preserves origin settings on retained rules and global config.
// A failed/lost write or read-back is never automatically replayed.
func (client cloudflareClient) syncTunnelConfiguration(ctx context.Context, tunnelID string, ingress []TunnelTaskIngress) error {
	path := "/accounts/" + url.PathEscape(client.accountID) + "/cfd_tunnel/" + url.PathEscape(tunnelID) + "/configurations"
	read := func() (map[string]json.RawMessage, []map[string]json.RawMessage, []TunnelTaskIngress, error) {
		var result struct {
			Source string                     `json:"source"`
			Config map[string]json.RawMessage `json:"config"`
		}
		if err := client.do(ctx, http.MethodGet, path, nil, &result); err != nil {
			return nil, nil, nil, err
		}
		var rules []map[string]json.RawMessage
		var canonical []TunnelTaskIngress
		if result.Source != "cloudflare" || json.Unmarshal(result.Config["ingress"], &rules) != nil || json.Unmarshal(result.Config["ingress"], &canonical) != nil || len(rules) == 0 {
			return nil, nil, nil, errors.New("center: invalid remotely managed Tunnel configuration")
		}
		for i, rule := range rules {
			if rule == nil || canonical[i].Service == "" {
				return nil, nil, nil, errors.New("center: invalid Tunnel ingress rule")
			}
		}
		return result.Config, rules, canonical, nil
	}
	config, rules, current, err := read()
	if err != nil {
		return err
	}
	wanted := append(append([]TunnelTaskIngress{}, ingress...), TunnelTaskIngress{Service: "http_status:404"})
	if reflect.DeepEqual(current, wanted) {
		return nil
	}
	next := make([]map[string]json.RawMessage, 0, len(wanted))
	for _, value := range wanted {
		rule := map[string]json.RawMessage{}
		for i, previous := range current {
			if previous.Hostname == value.Hostname && previous.Path == value.Path {
				rule = rules[i]
				break
			}
		}
		rule["service"], _ = json.Marshal(value.Service)
		if value.Hostname != "" {
			rule["hostname"], _ = json.Marshal(value.Hostname)
		}
		if value.Path != "" {
			rule["path"], _ = json.Marshal(value.Path)
		}
		next = append(next, rule)
	}
	config["ingress"], _ = json.Marshal(next)
	var ignored json.RawMessage
	if err := client.do(ctx, http.MethodPut, path, map[string]any{"config": config}, &ignored); err != nil {
		return err
	}
	_, _, observed, err := read()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(observed, wanted) {
		return errors.New("center: Tunnel origin read-back differs from the authorized task")
	}
	return nil
}
