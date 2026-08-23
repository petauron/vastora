package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

var threeXUIClientActions = map[string]bool{
	"list": true, "list_inbounds": true, "create": true, "update": true, "set_enabled": true,
	"delete": true, "reset_traffic": true, "reveal_link": true,
	"reveal_subscription": true, "update_inbound": true, "reset_inbound_plan": true,
}

func normalizeThreeXUIClientCommandInput(input ThreeXUIClientCommandInput) (ThreeXUIClientCommandInput, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.Action = strings.TrimSpace(input.Action)
	input.Email = strings.TrimSpace(input.Email)
	input.NewEmail = strings.TrimSpace(input.NewEmail)
	input.ServiceID = strings.TrimSpace(input.ServiceID)
	for _, inboundID := range input.InboundIDs {
		if inboundID < 1 {
			return input, errors.New("center: selected REALITY node is invalid")
		}
	}
	input.InboundIDs = normalizedThreeXUIInboundIDs(input.InboundIDs)
	if input.ApplicationID == "" || !threeXUIClientActions[input.Action] {
		return input, errors.New("center: application and a valid 3x-ui client operation are required")
	}
	if input.TotalBytes < 0 || input.ResetDays < 0 || input.ResetDays > maxThreeXUIResetDays || input.ExpiryTime < 0 || input.LimitIP < 0 || input.LimitIP > 1000 || input.InboundTotalBytes < 0 || input.InboundResetDays < 0 || input.InboundResetDays > maxThreeXUIResetDays {
		return input, errors.New("center: client quota, expiry, or IP limit is invalid")
	}
	switch input.Action {
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
		return ApplicationCommandView{}, errors.New("center: client management is available only on the running Site 3x-ui controller")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id = ? AND kind <> ? AND state IN ('pending', 'running')`, agentID, controllerCommandKind).Scan(&active); err != nil {
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
				if input.InboundResetDays == inbound.ResetDays && inbound.PlanStatus == "active" {
					nextResetAt = inbound.NextResetAt
				}
				break
			}
		}
		if planRevision < 2 {
			return ApplicationCommandView{}, errors.New("center: REALITY inbound traffic plan is unavailable")
		}
		if input.InboundResetDays > 0 && nextResetAt == "" {
			nextResetAt, err = nextThreeXUIInboundResetAt(ctx, tx, input.ServiceID, now, input.InboundResetDays)
			if err != nil {
				return ApplicationCommandView{}, err
			}
		}
		if input.InboundResetDays == 0 {
			nextResetAt = ""
		}
	}
	task := ThreeXUIClientCommandTask{
		Action: input.Action, Email: input.Email, NewEmail: input.NewEmail, InboundID: input.InboundID,
		InboundIDs: input.InboundIDs,
		Enabled:    input.Enabled, TotalBytes: input.TotalBytes, ResetDays: input.ResetDays, ExpiryTime: input.ExpiryTime, LimitIP: input.LimitIP,
		ServiceID: input.ServiceID, InboundTotalBytes: input.InboundTotalBytes, InboundResetDays: input.InboundResetDays, ExpectedNextResetAt: nextResetAt, PlanRevision: planRevision,
		Inbounds: inbounds, SubscriptionBaseURI: subscriptionBaseURI,
	}
	if input.Action == "update_inbound" {
		for index := range task.Inbounds {
			if task.Inbounds[index].ServiceID == input.ServiceID && task.Inbounds[index].ID == input.InboundID {
				task.InboundTag = task.Inbounds[index].InboundTag
				task.Inbounds[index].TotalBytes = input.InboundTotalBytes
				task.Inbounds[index].ResetDays = input.InboundResetDays
				task.Inbounds[index].NextResetAt = nextResetAt
				task.Inbounds[index].PlanStatus = "active"
				task.Inbounds[index].PlanError = ""
			}
		}
	}
	encoded, _ := json.Marshal(task)
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
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
		COALESCE(plan.total_bytes, 0), COALESCE(plan.reset_days, 0), COALESCE(plan.next_reset_at, ''),
		COALESCE(plan.status, 'active'), COALESCE(plan.last_error, ''), COALESCE(plan.revision, 0), COALESCE(plan.inbound_tag, '')
		FROM services s JOIN applications target ON target.id = s.application_id JOIN agents agent ON agent.id = target.node_id
		LEFT JOIN three_x_ui_inbound_plans plan ON plan.service_id = s.id
		WHERE target.site_id = (SELECT site_id FROM applications WHERE id = ?)
		AND target.app_key = ? AND target.status = 'running'
		AND s.name GLOB 'inbound-[0-9]*' AND s.app_protocol = 'vless/tcp/reality' AND s.status IN ('running', 'ready')
		ORDER BY agent.name, CAST(SUBSTR(s.name, 9) AS INTEGER)`, applicationID, threeXUIAppKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ThreeXUIClientInbound{}
	for rows.Next() {
		var value ThreeXUIClientInbound
		if err := rows.Scan(&value.ID, &value.ServiceID, &value.Name, &value.DisplayName, &value.ApplicationID, &value.NodeID, &value.NodeName, &value.ConnectHostname, &value.SNIHostname, &value.TotalBytes, &value.ResetDays, &value.NextResetAt, &value.PlanStatus, &value.PlanError, &value.PlanRevision, &value.InboundTag); err != nil {
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

func validateThreeXUIClientCommandResult(input ThreeXUIClientCommandTask, result ThreeXUIClientCommandResult) error {
	if input.Action == "list" && !result.ClientsObserved {
		return errors.New("center: Agent did not return the requested 3x-ui client list")
	}
	if (input.Action == "list" || input.Action == "list_inbounds" || input.Action == "update_inbound") && !result.InboundsObserved {
		return errors.New("center: Agent did not return the requested 3x-ui inbound traffic plans")
	}
	if input.Action == "reset_inbound_plan" && result.InboundsObserved {
		return errors.New("center: Agent incorrectly claimed a full inbound observation after a targeted traffic reset")
	}
	if len(result.Clients) > 10000 {
		return errors.New("center: Agent returned too many 3x-ui clients")
	}
	allowedInbounds := map[int]ThreeXUIClientInbound{}
	for _, inbound := range input.Inbounds {
		allowedInbounds[inbound.ID] = inbound
	}
	for _, client := range result.Clients {
		if !validThreeXUIClientName(strings.TrimSpace(client.Email)) || client.TotalBytes < 0 || client.UsedBytes < 0 || client.ResetDays < 0 || client.ResetDays > maxThreeXUIResetDays || client.ExpiryTime < 0 || client.LimitIP < 0 || client.LimitIP > 1000 {
			return errors.New("center: Agent returned invalid 3x-ui client metadata")
		}
		for _, id := range client.InboundIDs {
			if id < 1 {
				return errors.New("center: Agent returned invalid 3x-ui inbound metadata")
			}
		}
	}
	resultIDs := make([]int, 0, len(result.Inbounds))
	for _, inbound := range result.Inbounds {
		expected, ok := allowedInbounds[inbound.ID]
		if !ok || !sameThreeXUIInboundReference(expected, inbound) {
			return errors.New("center: Agent changed the available 3x-ui inbounds")
		}
		trafficObserved := result.InboundsObserved || input.Action == "reset_inbound_plan"
		if trafficObserved && (inbound.TotalBytes < 0 || inbound.UsedBytes < 0) {
			return errors.New("center: Agent returned invalid 3x-ui inbound traffic metadata")
		}
		if trafficObserved && (!validThreeXUIInboundTag(inbound.InboundTag) || (expected.InboundTag != "" && inbound.InboundTag != expected.InboundTag)) {
			return errors.New("center: Agent returned an invalid managed 3x-ui inbound tag")
		}
		if !trafficObserved && inbound != expected {
			return errors.New("center: Agent returned unobserved 3x-ui inbound traffic metadata")
		}
		resultIDs = append(resultIDs, inbound.ID)
	}
	if input.Action == "update_inbound" || input.Action == "reset_inbound_plan" {
		matched := false
		for _, inbound := range result.Inbounds {
			if inbound.ServiceID == input.ServiceID && inbound.ID == input.InboundID {
				matched = inbound.TotalBytes == input.InboundTotalBytes
				break
			}
		}
		if !matched {
			return errors.New("center: Agent did not apply the requested 3x-ui inbound traffic plan")
		}
	}
	if input.Action == "reset_inbound_plan" {
		if len(resultIDs) != 1 || resultIDs[0] != input.InboundID {
			return errors.New("center: Agent returned incomplete 3x-ui inbound metadata")
		}
	} else {
		expectedIDs := make([]int, 0, len(input.Inbounds))
		for _, inbound := range input.Inbounds {
			expectedIDs = append(expectedIDs, inbound.ID)
		}
		sort.Ints(resultIDs)
		sort.Ints(expectedIDs)
		if len(resultIDs) != len(expectedIDs) {
			return errors.New("center: Agent returned incomplete 3x-ui inbound metadata")
		}
		for index := range resultIDs {
			if resultIDs[index] != expectedIDs[index] {
				return errors.New("center: Agent returned incomplete 3x-ui inbound metadata")
			}
		}
	}
	expectedSecret := input.Action == "reveal_link" || input.Action == "reveal_subscription"
	if expectedSecret != (strings.TrimSpace(result.Secret) != "") {
		return errors.New("center: Agent returned an unexpected 3x-ui client secret")
	}
	if !expectedSecret {
		return nil
	}
	secret, err := url.Parse(strings.TrimSpace(result.Secret))
	if err != nil {
		return errors.New("center: Agent returned an invalid 3x-ui client secret")
	}
	if input.Action == "reveal_link" {
		inbound, ok := allowedInbounds[input.InboundID]
		query := secret.Query()
		hasPassword := false
		if secret.User != nil {
			_, hasPassword = secret.User.Password()
		}
		if !ok || result.SecretKind != "client_link" || secret.Scheme != "vless" || secret.User == nil || secret.User.Username() == "" || hasPassword || secret.Hostname() != inbound.ConnectHostname || secret.Port() != "443" || query.Get("type") != "tcp" || query.Get("security") != "reality" || query.Get("flow") != "xtls-rprx-vision" || query.Get("sni") != inbound.SNIHostname || query.Get("pbk") == "" || query.Get("sid") == "" {
			return errors.New("center: Agent returned an invalid 3x-ui client link")
		}
		return nil
	}
	base, baseErr := url.Parse(input.SubscriptionBaseURI)
	clientID := strings.TrimPrefix(secret.Path, "/sub/")
	if baseErr != nil || base.Scheme != "https" || base.Path != "/sub/" || result.SecretKind != "subscription" || secret.Scheme != "https" || secret.Host != base.Host || secret.User != nil || secret.RawQuery != "" || secret.Fragment != "" || clientID == "" || strings.Contains(clientID, "/") {
		return errors.New("center: Agent returned an invalid 3x-ui subscription link")
	}
	return nil
}

func sameThreeXUIInboundReference(expected, actual ThreeXUIClientInbound) bool {
	return expected.ID == actual.ID && expected.ServiceID == actual.ServiceID && expected.Name == actual.Name && expected.DisplayName == actual.DisplayName && expected.ApplicationID == actual.ApplicationID && expected.NodeID == actual.NodeID && expected.NodeName == actual.NodeName && expected.ConnectHostname == actual.ConnectHostname && expected.SNIHostname == actual.SNIHostname && expected.ResetDays == actual.ResetDays && expected.NextResetAt == actual.NextResetAt && expected.PlanStatus == actual.PlanStatus && expected.PlanError == actual.PlanError
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

func (s *Store) completeThreeXUIClientCommand(ctx context.Context, tx *sql.Tx, taskID, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input ThreeXUIClientCommandTask
	var envelope ApplicationTaskResult
	if json.Unmarshal(inputJSON, &input) != nil || !threeXUIClientActions[input.Action] {
		return errors.New("center: stored 3x-ui client operation is invalid")
	}
	if succeeded {
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.ClientCommand == nil {
			succeeded = false
			taskError = "center: Agent returned an invalid 3x-ui client result"
		}
	}
	now := s.now().UTC()
	publicResult := []byte(`{}`)
	var resultSecretID any
	if succeeded {
		result := *envelope.ClientCommand
		if err := validateThreeXUIClientCommandResult(input, result); err != nil {
			succeeded = false
			taskError = err.Error()
		} else {
			if result.InboundsObserved {
				for _, inbound := range result.Inbounds {
					if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET inbound_tag = ?, updated_at = ? WHERE service_id = ? AND inbound_tag = ''`, inbound.InboundTag, now.Format(time.RFC3339Nano), inbound.ServiceID); err != nil {
						return err
					}
				}
			}
			if input.Action == "update_inbound" {
				if err := completeThreeXUIInboundPlanUpdate(ctx, tx, input, &result, now); err != nil {
					succeeded = false
					taskError = err.Error()
				}
			}
			if succeeded && input.Action == "reset_inbound_plan" {
				if err := s.completeThreeXUIInboundPlanReset(ctx, tx, input, &result, true, "", now); err != nil {
					succeeded = false
					taskError = err.Error()
				}
			}
			if !succeeded {
				result = ThreeXUIClientCommandResult{}
			}
			if succeeded {
				if result.Secret != "" {
					secretID, err := s.putSecret(ctx, tx, []byte(result.Secret), "application-command:"+taskID)
					if err != nil {
						return err
					}
					resultSecretID = secretID
				}
				for index := range result.Inbounds {
					result.Inbounds[index].InboundTag = ""
				}
				result.Secret = ""
				result.SecretKind = ""
				publicResult, _ = json.Marshal(result)
			}
		}
	}
	if !succeeded && input.Action == "reset_inbound_plan" {
		if err := s.completeThreeXUIInboundPlanReset(ctx, tx, input, nil, false, taskError, now); err != nil {
			if strings.Contains(err.Error(), "stale") {
				taskError = err.Error()
			} else {
				return err
			}
		}
	}
	state, event, message := "succeeded", "succeeded", "3x-ui client operation completed"
	if !succeeded {
		state, event = "failed", "failed"
		if taskError == "" {
			taskError = "3x-ui client operation failed"
		}
		message = taskError
		resultSecretID = nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = ?, result_secret_id = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'running'`, state, publicResult, resultSecretID, taskError, now.Format(time.RFC3339Nano), taskID); err != nil {
		return err
	}
	eventRevision := int64(1)
	if input.PlanRevision > 0 {
		eventRevision = input.PlanRevision
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", eventRevision, event, message); err != nil {
		return err
	}
	return tx.Commit()
}
