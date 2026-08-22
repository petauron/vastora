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
	"list": true, "create": true, "update": true, "set_enabled": true,
	"delete": true, "reset_traffic": true, "reveal_link": true,
	"reveal_subscription": true, "reveal_clash_subscription": true,
}

func normalizeThreeXUIClientCommandInput(input ThreeXUIClientCommandInput) (ThreeXUIClientCommandInput, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.Action = strings.TrimSpace(input.Action)
	input.Email = strings.TrimSpace(input.Email)
	input.NewEmail = strings.TrimSpace(input.NewEmail)
	if input.ApplicationID == "" || !threeXUIClientActions[input.Action] {
		return input, errors.New("center: application and a valid 3x-ui client operation are required")
	}
	if input.TotalBytes < 0 || input.ExpiryTime < 0 || input.LimitIP < 0 || input.LimitIP > 1000 {
		return input, errors.New("center: client quota, expiry, or IP limit is invalid")
	}
	switch input.Action {
	case "create":
		if !validThreeXUIClientName(input.NewEmail) || input.InboundID < 1 {
			return input, errors.New("center: client name and REALITY inbound are required")
		}
	case "update":
		if !validThreeXUIClientName(input.Email) || !validThreeXUIClientName(input.NewEmail) {
			return input, errors.New("center: current and new client names are required")
		}
	case "set_enabled", "delete", "reset_traffic", "reveal_subscription", "reveal_clash_subscription":
		if !validThreeXUIClientName(input.Email) {
			return input, errors.New("center: a valid client name is required")
		}
	case "reveal_link":
		if !validThreeXUIClientName(input.Email) || input.InboundID < 1 {
			return input, errors.New("center: client name and REALITY inbound are required")
		}
	}
	return input, nil
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
	var agentID, appKey, status string
	if err := tx.QueryRowContext(ctx, `SELECT node_id, app_key, status FROM applications WHERE id = ?`, input.ApplicationID).Scan(&agentID, &appKey, &status); errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: application not found")
	} else if err != nil {
		return ApplicationCommandView{}, err
	}
	if appKey != threeXUIAppKey || status != "running" {
		return ApplicationCommandView{}, errors.New("center: client management requires a running official 3x-ui application")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id = ? AND state IN ('pending', 'running')`, input.ApplicationID).Scan(&active); err != nil {
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
	subscriptionBaseURI, err := threeXUISubscriptionBaseURI(ctx, tx, input.ApplicationID)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	task := ThreeXUIClientCommandTask{
		Action: input.Action, Email: input.Email, NewEmail: input.NewEmail, InboundID: input.InboundID,
		Enabled: input.Enabled, TotalBytes: input.TotalBytes, ExpiryTime: input.ExpiryTime, LimitIP: input.LimitIP,
		Inbounds: inbounds, SubscriptionBaseURI: subscriptionBaseURI,
	}
	encoded, _ := json.Marshal(task)
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	now := s.now().UTC()
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
	rows, err := tx.QueryContext(ctx, `SELECT CAST(SUBSTR(s.name, 9) AS INTEGER), s.name,
		COALESCE((SELECT p.hostname FROM publications p WHERE p.service_id = s.id AND p.kind = 'public_shared_443' AND p.status = 'ready' ORDER BY p.updated_at DESC LIMIT 1), ''),
		COALESCE((SELECT p.sni_hostname FROM publications p WHERE p.service_id = s.id AND p.kind = 'public_shared_443' AND p.status = 'ready' ORDER BY p.updated_at DESC LIMIT 1), '')
		FROM services s WHERE s.application_id = ? AND s.name GLOB 'inbound-[0-9]*' AND s.app_protocol = 'vless/tcp/reality' AND s.status IN ('running', 'ready')
		ORDER BY CAST(SUBSTR(s.name, 9) AS INTEGER)`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ThreeXUIClientInbound{}
	for rows.Next() {
		var value ThreeXUIClientInbound
		if err := rows.Scan(&value.ID, &value.Name, &value.ConnectHostname, &value.SNIHostname); err != nil {
			return nil, err
		}
		if value.ID > 0 {
			values = append(values, value)
		}
	}
	return values, rows.Err()
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
	if len(result.Clients) > 10000 {
		return errors.New("center: Agent returned too many 3x-ui clients")
	}
	allowedInbounds := map[int]ThreeXUIClientInbound{}
	for _, inbound := range input.Inbounds {
		allowedInbounds[inbound.ID] = inbound
	}
	for _, client := range result.Clients {
		if !validThreeXUIClientName(strings.TrimSpace(client.Email)) || client.TotalBytes < 0 || client.UsedBytes < 0 || client.ExpiryTime < 0 || client.LimitIP < 0 || client.LimitIP > 1000 {
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
		if expected, ok := allowedInbounds[inbound.ID]; !ok || expected != inbound {
			return errors.New("center: Agent changed the available 3x-ui inbounds")
		}
		resultIDs = append(resultIDs, inbound.ID)
	}
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
	expectedSecret := input.Action == "reveal_link" || input.Action == "reveal_subscription" || input.Action == "reveal_clash_subscription"
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
	expectedKind := "subscription"
	expectedPath := "/sub/"
	if input.Action == "reveal_clash_subscription" {
		expectedKind = "clash_subscription"
		expectedPath = "/clash/"
	}
	clientID := strings.TrimPrefix(secret.Path, expectedPath)
	if baseErr != nil || base.Scheme != "https" || base.Path != "/sub/" || result.SecretKind != expectedKind || secret.Scheme != "https" || secret.Host != base.Host || secret.User != nil || secret.RawQuery != "" || secret.Fragment != "" || clientID == "" || strings.Contains(clientID, "/") {
		return errors.New("center: Agent returned an invalid 3x-ui subscription link")
	}
	return nil
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
			if result.Secret != "" {
				secretID, err := s.putSecret(ctx, tx, []byte(result.Secret), "application-command:"+taskID)
				if err != nil {
					return err
				}
				resultSecretID = secretID
			}
			result.Secret = ""
			result.SecretKind = ""
			publicResult, _ = json.Marshal(result)
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
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	return tx.Commit()
}
