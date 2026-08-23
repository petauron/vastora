package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

func (s *Store) claimApplicationCommand(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, kind string
	var inputJSON []byte
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT id, kind, input_json, attempt FROM application_commands
		WHERE agent_id = ? AND state = 'pending'
		ORDER BY CASE WHEN kind = ? AND COALESCE(json_extract(input_json, '$.migrationId'), '') = '' THEN 1 ELSE 0 END,
		created_at, rowid LIMIT 1`, agentID, controllerCommandKind).Scan(&id, &kind, &inputJSON, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read pending application operation: %w", err)
	}
	var reality *RealityCommandTask
	var subscription *SubscriptionCommandTask
	var client *ThreeXUIClientCommandTask
	var node *ThreeXUINodeCommandTask
	var controller *ThreeXUIControllerCommandTask
	switch kind {
	case realityCommandKind, realityRenameCommandKind:
		var command RealityCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || command.TargetApplicationID == "" || !validThreeXUIClientName(command.DisplayName) {
			return nil, errors.New("center: stored REALITY operation is invalid")
		}
		if kind == realityCommandKind && (command.Action != "create" || !validThreeXUIClientName(command.ClientName) || command.ConnectHostname == "" || net.ParseIP(command.TargetAddress) == nil) {
			return nil, errors.New("center: stored REALITY creation operation is invalid")
		}
		if kind == realityRenameCommandKind && (command.Action != "rename" || command.InboundID < 1) {
			return nil, errors.New("center: stored REALITY rename operation is invalid")
		}
		if kind == realityCommandKind && command.TargetNodeID > 0 {
			command.TargetAPIToken, err = s.threeXUIAPISecret(ctx, tx, command.TargetApplicationID)
			if err != nil {
				return nil, err
			}
		}
		reality = &command
	case subscriptionCommandKind:
		var command SubscriptionCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || command.Domain == "" || command.BaseURI == "" || command.PublicationID == "" {
			return nil, errors.New("center: stored subscription operation is invalid")
		}
		subscription = &command
	case clientCommandKind:
		var command ThreeXUIClientCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || !threeXUIClientActions[command.Action] {
			return nil, errors.New("center: stored 3x-ui client operation is invalid")
		}
		client = &command
	case nodeCommandKind:
		var command ThreeXUINodeCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || command.WorkerApplicationID == "" || (command.Action != "reconcile" && command.Action != "remove") {
			return nil, errors.New("center: stored 3x-ui node operation is invalid")
		}
		if command.Action == "reconcile" {
			command.APIToken, err = s.threeXUIAPISecret(ctx, tx, command.WorkerApplicationID)
			if err != nil {
				return nil, err
			}
		}
		node = &command
	case controllerCommandKind:
		var command ThreeXUIControllerCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || command.ApplicationID == "" || (command.Action != "backup" && command.Action != "promote" && command.Action != "demote") {
			return nil, errors.New("center: stored 3x-ui controller operation is invalid")
		}
		if command.Action == "promote" {
			command.SourceAPIToken, err = s.threeXUIAPISecret(ctx, tx, command.SourceApplicationID)
		} else {
			command.SourceAPIToken, err = s.threeXUIAPISecret(ctx, tx, command.ApplicationID)
		}
		if err != nil {
			return nil, err
		}
		controller = &command
	default:
		return nil, errors.New("center: stored application operation kind is invalid")
	}
	now := s.now().UTC()
	result, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'running', attempt = attempt + 1, lease_expires_at = ?, error = '', updated_at = ? WHERE id = ? AND state = 'pending' AND attempt = ?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, attempt)
	if err != nil {
		return nil, fmt.Errorf("center: claim application operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, errors.New("center: application operation changed while claiming")
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "claimed", fmt.Sprintf("attempt %d", attempt+1)); err != nil {
		return nil, err
	}
	if node != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'applying', updated_at = ? WHERE worker_application_id = ?`, now.Format(time.RFC3339Nano), node.WorkerApplicationID); err != nil {
			return nil, err
		}
	}
	return &AgentTask{Kind: "application.command", ID: id, Attempt: attempt + 1, Revision: 1, ApplicationCommand: reality, SubscriptionCommand: subscription, ClientCommand: client, NodeCommand: node, ControllerCommand: controller}, nil
}

func (s *Store) completeApplicationCommand(ctx context.Context, agentID, taskID string, expectedAttempt int64, succeeded bool, taskError string, rawResult json.RawMessage) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applicationID, gatewayID, kind, currentState string
	var inputJSON []byte
	var attempt int64
	if err := tx.QueryRowContext(ctx, `SELECT application_id, gateway_node_id, kind, input_json, state, attempt FROM application_commands WHERE id = ? AND agent_id = ?`, taskID, agentID).Scan(&applicationID, &gatewayID, &kind, &inputJSON, &currentState, &attempt); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: application operation not found")
	} else if err != nil {
		return err
	}
	if currentState == "succeeded" || currentState == "failed" {
		return nil
	}
	if currentState != "running" || expectedAttempt <= 0 || expectedAttempt != attempt {
		return errors.New("center: stale application operation result")
	}
	if kind == subscriptionCommandKind {
		return s.completeSubscriptionCommand(ctx, tx, taskID, agentID, inputJSON, succeeded, taskError, rawResult)
	}
	if kind == clientCommandKind {
		return s.completeThreeXUIClientCommand(ctx, tx, taskID, agentID, inputJSON, succeeded, taskError, rawResult)
	}
	if kind == nodeCommandKind {
		return s.completeThreeXUINodeCommand(ctx, tx, taskID, agentID, inputJSON, succeeded, taskError, rawResult)
	}
	if kind == controllerCommandKind {
		return s.completeThreeXUIControllerCommand(ctx, tx, taskID, agentID, inputJSON, succeeded, taskError, rawResult)
	}
	if kind == realityRenameCommandKind {
		return s.completeRealityRenameCommand(ctx, tx, taskID, agentID, applicationID, inputJSON, succeeded, taskError, rawResult)
	}
	if kind != realityCommandKind {
		return errors.New("center: stored application operation kind is invalid")
	}
	now := s.now().UTC()
	var serviceID string
	var input RealityCommandTask
	var envelope ApplicationTaskResult
	if json.Unmarshal(inputJSON, &input) != nil {
		return errors.New("center: stored application operation is invalid")
	}
	if succeeded {
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.ApplicationCommand == nil {
			succeeded = false
			taskError = "center: Agent returned an invalid REALITY result"
		}
	}
	if succeeded {
		result := *envelope.ApplicationCommand
		if err := validateRealityCommandResult(input, result); err != nil {
			succeeded = false
			taskError = err.Error()
		} else {
			var serviceAddress string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(p.service_address, '') FROM applications a LEFT JOIN agent_network_profiles p ON p.agent_id = a.node_id WHERE a.id = ?`, applicationID).Scan(&serviceAddress); err != nil || result.Listen != serviceAddress {
				succeeded = false
				taskError = "center: REALITY inbound is not bound to the confirmed private service address"
			}
		}
		if succeeded {
			for _, excluded := range input.ExcludedSNI {
				if result.SNIHostname == excluded {
					succeeded = false
					taskError = "center: selected REALITY SNI is already used on this gateway"
					break
				}
			}
		}
		if succeeded {
			result := *envelope.ApplicationCommand
			serviceName := fmt.Sprintf("inbound-%d", result.InboundID)
			err := tx.QueryRowContext(ctx, `SELECT id FROM services WHERE application_id = ? AND name = ?`, applicationID, serviceName).Scan(&serviceID)
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			}
			var siteID string
			if err == nil {
				err = tx.QueryRowContext(ctx, `SELECT site_id FROM applications WHERE id = ?`, applicationID).Scan(&siteID)
			}
			var duplicateDisplayName int
			if err == nil {
				err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE site_id = ? AND id <> ? AND app_protocol = 'vless/tcp/reality' AND status <> 'stopped' AND display_name = ? COLLATE NOCASE`, siteID, serviceID, result.DisplayName).Scan(&duplicateDisplayName)
			}
			if err == nil && duplicateDisplayName != 0 {
				err = errors.New("this Site already has a REALITY node with that display name")
			}
			if err == nil && serviceID == "" {
				serviceID, err = randomToken(18)
				if err == nil {
					_, err = tx.ExecContext(ctx, `INSERT INTO services(id, application_id, site_id, name, display_name, protocol, container_port, host_port, endpoint, source, app_protocol, management, observed_listen, status, created_at, updated_at) VALUES(?, ?, ?, ?, ?, 'tcp', ?, ?, ?, 'observed', 'vless/tcp/reality', 0, ?, 'ready', ?, ?)`, serviceID, applicationID, siteID, serviceName, result.DisplayName, result.Port, result.Port, net.JoinHostPort(result.Listen, fmt.Sprint(result.Port)), result.Listen, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
				}
			} else if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE services SET display_name = ?, protocol = 'tcp', container_port = ?, host_port = ?, endpoint = ?, source = 'observed', app_protocol = 'vless/tcp/reality', observed_listen = ?, status = 'ready', last_error = '', updated_at = ? WHERE id = ?`, result.DisplayName, result.Port, result.Port, net.JoinHostPort(result.Listen, fmt.Sprint(result.Port)), result.Listen, now.Format(time.RFC3339Nano), serviceID)
			}
			if err != nil {
				succeeded = false
				taskError = "center: save REALITY service: " + err.Error()
			}
		}
		if succeeded {
			result := *envelope.ApplicationCommand
			var previousSecret sql.NullString
			if err := tx.QueryRowContext(ctx, `SELECT result_secret_id FROM application_commands WHERE id = ?`, taskID).Scan(&previousSecret); err != nil {
				return err
			}
			if previousSecret.Valid {
				if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, previousSecret.String); err != nil {
					return err
				}
			}
			secretID, err := s.putSecret(ctx, tx, []byte(result.ShareURI), "application-command:"+taskID)
			if err != nil {
				return err
			}
			result.ShareURI = ""
			publicResult, _ := json.Marshal(result)
			if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET result_json = ?, result_secret_id = ?, error = '', updated_at = ? WHERE id = ?`, publicResult, secretID, now.Format(time.RFC3339Nano), taskID); err != nil {
				return err
			}
		}
	}
	if !succeeded {
		if taskError == "" {
			taskError = "application operation failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', lease_expires_at = '', error = ?, updated_at = ? WHERE id = ?`, taskError, now.Format(time.RFC3339Nano), taskID); err != nil {
			return err
		}
	}
	if !succeeded {
		if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, "failed", taskError); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !succeeded {
		return nil
	}
	result := *envelope.ApplicationCommand
	err = s.ensureRealityPublication(ctx, serviceID, gatewayID, input, result.SNIHostname)
	if err == nil {
		finalizeTx, beginErr := s.db.BeginTx(ctx, nil)
		if beginErr != nil {
			return beginErr
		}
		defer finalizeTx.Rollback()
		if _, updateErr := finalizeTx.ExecContext(ctx, `UPDATE application_commands SET state = 'succeeded', lease_expires_at = '', error = '', updated_at = ? WHERE id = ? AND state = 'running'`, s.now().UTC().Format(time.RFC3339Nano), taskID); updateErr != nil {
			return updateErr
		}
		if eventErr := s.recordTaskEvent(ctx, finalizeTx, taskID, agentID, "application.command", 1, "succeeded", "3x-ui REALITY and shared 443 access created"); eventErr != nil {
			return eventErr
		}
		return finalizeTx.Commit()
	}
	failure := "center: create REALITY access entry: " + err.Error()
	cleanupTx, beginErr := s.db.BeginTx(ctx, nil)
	if beginErr != nil {
		return errors.Join(err, beginErr)
	}
	defer cleanupTx.Rollback()
	var secretID sql.NullString
	_ = cleanupTx.QueryRowContext(ctx, `SELECT result_secret_id FROM application_commands WHERE id = ?`, taskID).Scan(&secretID)
	if _, updateErr := cleanupTx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', result_secret_id = NULL, error = ?, updated_at = ? WHERE id = ?`, failure, s.now().UTC().Format(time.RFC3339Nano), taskID); updateErr != nil {
		return errors.Join(err, updateErr)
	}
	if secretID.Valid {
		_, _ = cleanupTx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, secretID.String)
	}
	if eventErr := s.recordTaskEvent(ctx, cleanupTx, taskID, agentID, "application.command", 1, "failed", failure); eventErr != nil {
		return errors.Join(err, eventErr)
	}
	if commitErr := cleanupTx.Commit(); commitErr != nil {
		return errors.Join(err, commitErr)
	}
	return nil
}

func (s *Store) completeRealityRenameCommand(ctx context.Context, tx *sql.Tx, taskID, agentID, applicationID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input RealityCommandTask
	if json.Unmarshal(inputJSON, &input) != nil || input.Action != "rename" || input.InboundID < 1 || !validThreeXUIClientName(input.DisplayName) {
		return errors.New("center: stored REALITY rename operation is invalid")
	}
	var envelope ApplicationTaskResult
	if succeeded {
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.ApplicationCommand == nil {
			succeeded = false
			taskError = "center: Agent returned an invalid REALITY rename result"
		} else if result := envelope.ApplicationCommand; result.Action != "rename" || result.InboundID != input.InboundID || result.DisplayName != input.DisplayName {
			succeeded = false
			taskError = "center: Agent changed the requested REALITY node name"
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	state, event, message := "succeeded", "succeeded", "3x-ui REALITY node renamed"
	resultJSON := []byte(`{}`)
	if succeeded {
		serviceName := fmt.Sprintf("inbound-%d", input.InboundID)
		result, err := tx.ExecContext(ctx, `UPDATE services SET display_name = ?, updated_at = ? WHERE application_id = ? AND name = ? AND app_protocol = 'vless/tcp/reality' AND status <> 'stopped'`, input.DisplayName, now, applicationID, serviceName)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			succeeded = false
			taskError = "center: REALITY service changed while renaming"
		} else {
			resultJSON, _ = json.Marshal(envelope.ApplicationCommand)
		}
	}
	if !succeeded {
		state, event = "failed", "failed"
		if taskError == "" {
			taskError = "application operation failed"
		}
		message = taskError
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'running'`, state, resultJSON, taskError, now, taskID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureRealityPublication(ctx context.Context, serviceID, gatewayID string, input RealityCommandTask, sniHostname string) error {
	var existingID, existingGateway, existingSNI, existingDNS string
	err := s.db.QueryRowContext(ctx, `SELECT id, COALESCE(gateway_node_id, ''), sni_hostname, dns_provider FROM publications WHERE service_id = ? AND kind = 'public_shared_443' AND hostname = ? AND status <> 'stopped'`, serviceID, input.ConnectHostname).Scan(&existingID, &existingGateway, &existingSNI, &existingDNS)
	if err == nil {
		if existingGateway != gatewayID || existingSNI != sniHostname || existingDNS != input.DNSProvider {
			return errors.New("center: existing REALITY access entry does not match the requested gateway, SNI, and DNS")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.CreatePublication(ctx, PublicationInput{ServiceID: serviceID, Kind: publicationShared443, GatewayNodeID: gatewayID, Hostname: input.ConnectHostname, SNIHostname: sniHostname, DNSProvider: input.DNSProvider})
	return err
}

func (s *Store) completeSubscriptionCommand(ctx context.Context, tx *sql.Tx, taskID, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input SubscriptionCommandTask
	if json.Unmarshal(inputJSON, &input) != nil || input.PublicationID == "" {
		return errors.New("center: stored subscription operation is invalid")
	}
	var envelope ApplicationTaskResult
	if succeeded {
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.SubscriptionCommand == nil {
			succeeded = false
			taskError = "center: Agent returned an invalid subscription result"
		} else if envelope.SubscriptionCommand.Domain != input.Domain || envelope.SubscriptionCommand.BaseURI != input.BaseURI {
			succeeded = false
			taskError = "center: Agent changed the requested subscription address"
		}
	}
	if taskError == "" && !succeeded {
		taskError = "application operation failed"
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	state, event, message := "succeeded", "succeeded", "3x-ui public subscription configured"
	var resultJSON []byte
	if succeeded {
		resultJSON, _ = json.Marshal(envelope.SubscriptionCommand)
	} else {
		state, event, message = "failed", "failed", taskError
		resultJSON = []byte(`{}`)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'running'`, state, resultJSON, taskError, now, taskID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if succeeded {
		return nil
	}
	if err := s.StopPublication(context.WithoutCancel(ctx), input.PublicationID); err != nil && !strings.Contains(err.Error(), "already stopped") {
		return errors.Join(errors.New(taskError), err)
	}
	return nil
}
