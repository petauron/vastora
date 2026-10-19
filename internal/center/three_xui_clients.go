package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"sort"
	"strings"
	"time"
	"unicode"
)

var threeXUIClientActions = map[string]bool{
	"landing_grant": true,
	"list":          true, "list_inbounds": true, "create": true, "update": true, "set_enabled": true,
	"delete": true, "reset_traffic": true, "reveal_link": true,
	"reveal_subscription": true, "update_inbound": true, "reset_inbound_plan": true,
}

func normalizeThreeXUIClientCommandInput(input ThreeXUIClientCommandInput) (ThreeXUIClientCommandInput, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.Action = strings.TrimSpace(input.Action)
	input.Email = strings.TrimSpace(input.Email)
	input.NewEmail = strings.TrimSpace(input.NewEmail)
	input.ServiceID = strings.TrimSpace(input.ServiceID)
	if strings.HasPrefix(input.Email, "vastora-combination-") || strings.HasPrefix(input.NewEmail, "vastora-combination-") {
		return input, errors.New("center: manage combination identities through their parent client's landing grants")
	}
	for _, inboundID := range input.InboundIDs {
		if inboundID < 1 {
			return input, errors.New("center: selected REALITY node is invalid")
		}
	}
	input.InboundIDs = normalizedThreeXUIInboundIDs(input.InboundIDs)
	if input.ApplicationID == "" || !threeXUIClientActions[input.Action] {
		return input, errors.New("center: application and a valid 3x-ui client operation are required")
	}
	if input.TotalBytes < 0 || input.ResetDays < 0 || input.ResetDays > maxThreeXUIResetDays || input.ExpiryTime < 0 || input.LimitIP < 0 || input.LimitIP > 1000 || input.InboundTotalBytes < 0 || input.InboundResetDay < 0 || input.InboundResetDay > maxThreeXUIResetDay {
		return input, errors.New("center: client quota, expiry, or IP limit is invalid")
	}
	switch input.Action {
	case "landing_grant":
		return input, errors.New("center: use the scoped client landing interface")
	case "list", "list_inbounds":
	case "create":
		if !validThreeXUIClientName(input.NewEmail) || len(input.InboundIDs) == 0 {
			return input, errors.New("center: client name and at least one REALITY node are required")
		}
	case "update":
		if !validThreeXUIClientName(input.Email) || !validThreeXUIClientName(input.NewEmail) || len(input.InboundIDs) == 0 {
			return input, errors.New("center: client name and at least one REALITY node are required")
		}
	case "set_enabled", "delete", "reset_traffic", "reveal_subscription":
		if !validThreeXUIClientName(input.Email) {
			return input, errors.New("center: a valid client name is required")
		}
	case "reveal_link":
		if !validThreeXUIClientName(input.Email) || input.InboundID < 1 {
			return input, errors.New("center: client name and REALITY inbound are required")
		}
	case "update_inbound":
		if input.ServiceID == "" || input.InboundID < 1 {
			return input, errors.New("center: REALITY service and inbound are required")
		}
	case "reset_inbound_plan":
		return input, errors.New("center: scheduled REALITY traffic resets cannot be requested directly")
	}
	return input, nil
}

func normalizedThreeXUIInboundIDs(values []int) []int {
	seen := make(map[int]bool, len(values))
	result := make([]int, 0, len(values))
	for _, value := range values {
		if value < 1 || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func validThreeXUIClientName(value string) bool {
	if value == "" || len([]rune(value)) > 64 || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func (s *Store) CreateThreeXUIClientCommand(ctx context.Context, input ThreeXUIClientCommandInput) (ApplicationCommandView, error) {
	var err error
	input, err = normalizeThreeXUIClientCommandInput(input)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	var agentID, appKey, status, role string
	if err := tx.QueryRowContext(ctx, `SELECT node_id, app_key, status, role FROM applications WHERE id = ?`, input.ApplicationID).Scan(&agentID, &appKey, &status, &role); errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: application not found")
	} else if err != nil {
		return ApplicationCommandView{}, err
	}
	if appKey != threeXUIAppKey || status != "running" || role != threeXUIRoleMaster {
		return ApplicationCommandView{}, errors.New("center: client management is available only on the running global 3x-ui controller")
	}
	controllerApplicationID, controllerAgentID, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil || controllerApplicationID != input.ApplicationID || controllerAgentID != agentID {
		return ApplicationCommandView{}, errors.New("center: client management is available only on the running global 3x-ui controller")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE (agent_id = ? OR kind='3xui.protocols.configure') AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, controllerCommandKind).Scan(&active); err != nil {
		return ApplicationCommandView{}, err
	}
	if active != 0 {
		return ApplicationCommandView{}, errors.New("center: this 3x-ui application already has an operation in progress")
	}
	inbounds, err := threeXUIClientInbounds(ctx, tx, input.ApplicationID)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	if input.InboundID > 0 && !threeXUIClientInboundExists(inbounds, input.InboundID) {
		return ApplicationCommandView{}, errors.New("center: selected REALITY inbound is unavailable")
	}
	if input.Action == "update_inbound" && !threeXUIClientInboundMatches(inbounds, input.ServiceID, input.InboundID) {
		return ApplicationCommandView{}, errors.New("center: selected REALITY service does not match the inbound")
	}
	if input.Action == "update_inbound" {
		for _, inbound := range inbounds {
			if inbound.ServiceID == input.ServiceID && inbound.ID == input.InboundID && strings.TrimSpace(inbound.InboundTag) == "" {
				return ApplicationCommandView{}, errors.New("center: refresh REALITY nodes before updating this migrated traffic plan")
			}
		}
	}
	for _, inboundID := range input.InboundIDs {
		if !threeXUIClientInboundExists(inbounds, inboundID) {
			return ApplicationCommandView{}, errors.New("center: selected REALITY node is unavailable")
		}
	}
	subscriptionBaseURI, err := threeXUISubscriptionBaseURI(ctx, tx, input.ApplicationID)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	now := s.now().UTC()
	if (input.Action == "create" || input.Action == "update") && input.ResetDays > 0 && input.ExpiryTime <= now.UnixMilli() {
		return ApplicationCommandView{}, errors.New("center: a renewable subscription traffic plan requires a future expiry time")
	}
	nextResetAt := ""
	planRevision := int64(0)
	if input.Action == "update_inbound" {
		for _, inbound := range inbounds {
			if inbound.ServiceID == input.ServiceID && inbound.ID == input.InboundID {
				planRevision = inbound.PlanRevision + 1
				if input.InboundResetDay == inbound.ResetDay && inbound.PlanStatus == "active" {
					nextResetAt = inbound.NextResetAt
				}
				break
			}
		}
		if planRevision < 2 {
			return ApplicationCommandView{}, errors.New("center: REALITY inbound traffic plan is unavailable")
		}
		if input.InboundResetDay > 0 && nextResetAt == "" {
			nextResetAt, err = nextThreeXUIInboundResetAt(ctx, tx, input.ServiceID, now, input.InboundResetDay)
			if err != nil {
				return ApplicationCommandView{}, err
			}
		}
		if input.InboundResetDay == 0 {
			nextResetAt = ""
		}
	}
	task := ThreeXUIClientCommandTask{
		Action: input.Action, Email: input.Email, NewEmail: input.NewEmail, InboundID: input.InboundID,
		InboundIDs: input.InboundIDs,
		Enabled:    input.Enabled, TotalBytes: input.TotalBytes, ResetDays: input.ResetDays, ExpiryTime: input.ExpiryTime, LimitIP: input.LimitIP,
		ServiceID: input.ServiceID, InboundTotalBytes: input.InboundTotalBytes, InboundResetDay: input.InboundResetDay, ExpectedNextResetAt: nextResetAt, PlanRevision: planRevision,
		Inbounds: inbounds, SubscriptionBaseURI: subscriptionBaseURI,
	}
	if input.Action == "update_inbound" {
		for index := range task.Inbounds {
			if task.Inbounds[index].ServiceID == input.ServiceID && task.Inbounds[index].ID == input.InboundID {
				task.InboundTag = task.Inbounds[index].InboundTag
				task.Inbounds[index].TotalBytes = input.InboundTotalBytes
				task.Inbounds[index].ResetDay = input.InboundResetDay
				task.Inbounds[index].NextResetAt = nextResetAt
				task.Inbounds[index].PlanStatus = "active"
				task.Inbounds[index].PlanError = ""
			}
		}
	}
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	if err := s.prepareLandingParentMutation(ctx, tx, id, input, &task); err != nil {
		return ApplicationCommandView{}, err
	}
	task.OperationKey = id
	encoded, _ := json.Marshal(task)
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, input.ApplicationID, agentID, agentID, clientCommandKind, encoded, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return ApplicationCommandView{}, fmt.Errorf("center: create 3x-ui client operation: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "queued", "3x-ui client operation queued"); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}

func threeXUIClientInbounds(ctx context.Context, tx *sql.Tx, applicationID string) ([]ThreeXUIClientInbound, error) {
	rows, err := tx.QueryContext(ctx, `SELECT CAST(SUBSTR(s.name, 9) AS INTEGER), s.id, s.name, s.display_name, target.id, target.node_id, agent.name,
		COALESCE((SELECT p.hostname FROM publications p WHERE p.service_id = s.id AND p.kind = 'public_shared_443' AND p.status = 'ready' ORDER BY p.updated_at DESC LIMIT 1), ''),
		COALESCE((SELECT p.sni_hostname FROM publications p WHERE p.service_id = s.id AND p.kind = 'public_shared_443' AND p.status = 'ready' ORDER BY p.updated_at DESC LIMIT 1), ''),
		COALESCE(plan.total_bytes, 0), COALESCE(plan.reset_day, 0), COALESCE(plan.next_reset_at, ''),
		COALESCE(plan.status, 'active'), COALESCE(plan.last_error, ''), COALESCE(plan.revision, 0), COALESCE(plan.inbound_tag, ''),COALESCE(protocols.hy2_inbound_id,0),COALESCE(protocols.vless_enabled,1)=0
		FROM services s JOIN applications target ON target.id = s.application_id JOIN agents agent ON agent.id = target.node_id
		LEFT JOIN three_x_ui_inbound_plans plan ON plan.service_id = s.id
		LEFT JOIN three_x_ui_node_protocols protocols ON protocols.service_id = s.id
		WHERE target.app_key = ? AND target.status = 'running'
		AND (target.id = ? OR EXISTS (
			SELECT 1 FROM three_x_ui_nodes topology
			WHERE topology.worker_application_id = target.id
			AND topology.master_application_id = ? AND topology.status = 'ready'
		))
		AND s.name GLOB 'inbound-[0-9]*' AND s.app_protocol = 'vless/tcp/reality' AND s.status IN ('running', 'ready')
		ORDER BY agent.name, CAST(SUBSTR(s.name, 9) AS INTEGER)`, threeXUIAppKey, applicationID, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ThreeXUIClientInbound{}
	for rows.Next() {
		var value ThreeXUIClientInbound
		if err := rows.Scan(&value.ID, &value.ServiceID, &value.Name, &value.DisplayName, &value.ApplicationID, &value.NodeID, &value.NodeName, &value.ConnectHostname, &value.SNIHostname, &value.TotalBytes, &value.ResetDay, &value.NextResetAt, &value.PlanStatus, &value.PlanError, &value.PlanRevision, &value.InboundTag, &value.HY2InboundID, &value.VLESSDisabled); err != nil {
			return nil, err
		}
		if value.ID > 0 {
			values = append(values, value)
		}
	}
	return values, rows.Err()
}

func threeXUIClientInboundMatches(inbounds []ThreeXUIClientInbound, serviceID string, id int) bool {
	for _, inbound := range inbounds {
		if inbound.ServiceID == serviceID && inbound.ID == id {
			return true
		}
	}
	return false
}

func threeXUIClientInboundExists(inbounds []ThreeXUIClientInbound, id int) bool {
	for _, inbound := range inbounds {
		if inbound.ID == id {
			return true
		}
	}
	return false
}

func threeXUISubscriptionBaseURI(ctx context.Context, tx *sql.Tx, applicationID string) (string, error) {
	var hostname string
	err := tx.QueryRowContext(ctx, `SELECT p.hostname FROM services s JOIN publications p ON p.service_id = s.id
		WHERE s.application_id = ? AND s.name = 'subscription' AND p.kind IN ('public_direct', 'cloudflare_tunnel') AND p.status = 'ready'
		ORDER BY p.updated_at DESC LIMIT 1`, applicationID).Scan(&hostname)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return "https://" + hostname + "/sub/", nil
}

func validThreeXUIInboundTag(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 160 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
