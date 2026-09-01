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

	"github.com/petauron/vastora/internal/networking"
)

var (
	errApplicationCommandDiscarded   = errors.New("center: unclaimable application command was failed closed")
	errApplicationCommandUnavailable = errors.New("center: application command is permanently unavailable")
)

func (s *Store) claimApplicationCommand(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, kind string
	var inputJSON []byte
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT id, kind, input_json, attempt FROM application_commands
		WHERE agent_id = ? AND state = 'pending'
		ORDER BY CASE WHEN kind = ? AND COALESCE(json_extract(CASE WHEN json_valid(input_json) THEN input_json ELSE '{}' END, '$.migrationId'), '') = '' THEN 1 ELSE 0 END,
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
	case realityCommandKind, realityVerifyCommandKind, realityHardenCommandKind, realityRenameCommandKind:
		var command RealityCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || command.TargetApplicationID == "" {
			return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, nil, errors.New("center: stored REALITY operation is invalid"))
		}
		if kind == realityCommandKind && (command.Action != "create" || !validRegionPrefixedRealityName(command.RegionCode, command.DisplayName) || (command.CreateInitialClient && !validThreeXUIClientName(command.ClientName)) || command.InboundTag != realityCommandInboundTag(id) || command.ConnectHostname == "" || !networking.IsPrivateServiceAddress(command.TargetAddress) || net.ParseIP(command.TargetPublicAddress) == nil || !domainSuffixPattern.MatchString(command.TargetHost) || !domainSuffixPattern.MatchString(command.ServerName) || command.InboundTotalBytes < 0 || command.InboundResetDay < 0 || command.InboundResetDay > maxThreeXUIResetDay || command.ClientTotalBytes < 0 || command.ClientResetDays < 0 || command.ClientResetDays > maxThreeXUIResetDays || command.ClientExpiryTime < 0) {
			return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, nil, errors.New("center: stored REALITY creation operation is invalid"))
		}
		if kind == realityVerifyCommandKind && (command.Action != "verify" || net.ParseIP(command.TargetPublicAddress) == nil || !domainSuffixPattern.MatchString(command.TargetHost) || !domainSuffixPattern.MatchString(command.ServerName)) {
			return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, nil, errors.New("center: stored REALITY verification operation is invalid"))
		}
		if kind == realityHardenCommandKind && (command.Action != "harden" || command.ServiceID == "" || command.InboundID < 1 || command.InboundTag == "" || net.ParseIP(command.TargetAddress) == nil || command.GuardRevision < 1) {
			return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, nil, errors.New("center: stored REALITY hardening operation is invalid"))
		}
		if kind == realityRenameCommandKind && (command.Action != "rename" || command.InboundID < 1) {
			return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, nil, errors.New("center: stored REALITY rename operation is invalid"))
		}
		if (kind == realityCommandKind || kind == realityHardenCommandKind) && command.TargetNodeID > 0 {
			command.TargetAPIToken, err = s.threeXUIAPISecret(ctx, tx, command.TargetApplicationID)
			if err != nil {
				if errors.Is(err, errThreeXUIAPISecretUnavailable) {
					return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, nil, err)
				}
				return nil, err
			}
		}
		reality = &command
	case subscriptionCommandKind:
		var command SubscriptionCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || command.Domain == "" || command.BaseURI == "" || command.PublicationID == "" {
			cause := errors.New("center: stored subscription operation is invalid")
			if err := s.failUnclaimableSubscriptionCommand(ctx, tx, id, agentID, command, cause); err != nil {
				return nil, err
			}
			return nil, errApplicationCommandDiscarded
		}
		subscription = &command
	case clientCommandKind:
		var command ThreeXUIClientCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || !threeXUIClientActions[command.Action] {
			return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, nil, errors.New("center: stored 3x-ui client operation is invalid"))
		}
		if command.Action == "update_inbound" || command.Action == "reset_inbound_plan" {
			if err := s.hydrateThreeXUIInboundPlanTask(ctx, tx, &command); err != nil {
				if !errors.Is(err, errApplicationCommandUnavailable) && !errors.Is(err, errThreeXUIAPISecretUnavailable) {
					return nil, err
				}
				if failErr := s.failUnclaimableThreeXUIInboundPlanCommand(ctx, tx, id, agentID, command, err); failErr != nil {
					return nil, failErr
				}
				return nil, errApplicationCommandDiscarded
			}
		}
		client = &command
	case nodeCommandKind:
		var command ThreeXUINodeCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || command.WorkerApplicationID == "" || (command.Action != "reconcile" && command.Action != "remove") {
			return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, &command, nil, errors.New("center: stored 3x-ui node operation is invalid"))
		}
		if command.Action == "reconcile" {
			command.APIToken, err = s.threeXUIAPISecret(ctx, tx, command.WorkerApplicationID)
			if err != nil {
				if errors.Is(err, errThreeXUIAPISecretUnavailable) {
					return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, &command, nil, err)
				}
				return nil, err
			}
		}
		node = &command
	case controllerCommandKind:
		var command ThreeXUIControllerCommandTask
		if json.Unmarshal(inputJSON, &command) != nil || command.ApplicationID == "" || (command.Action != "backup" && command.Action != "promote" && command.Action != "demote") {
			return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, &command, errors.New("center: stored 3x-ui controller operation is invalid"))
		}
		if command.Action == "promote" {
			command.SourceAPIToken, err = s.threeXUIAPISecret(ctx, tx, command.SourceApplicationID)
		} else {
			command.SourceAPIToken, err = s.threeXUIAPISecret(ctx, tx, command.ApplicationID)
		}
		if err != nil {
			if errors.Is(err, errThreeXUIAPISecretUnavailable) {
				return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, &command, err)
			}
			return nil, err
		}
		controller = &command
	default:
		return s.discardUnclaimableApplicationCommand(ctx, tx, id, agentID, 1, nil, nil, errors.New("center: stored application operation kind is invalid"))
	}
	now := s.now().UTC()
	taskRevision := int64(1)
	if client != nil && client.PlanRevision > 0 {
		taskRevision = client.PlanRevision
	}
	result, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'running', attempt = attempt + 1, lease_expires_at = ?, error = '', updated_at = ? WHERE id = ? AND state = 'pending' AND attempt = ?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, attempt)
	if err != nil {
		return nil, fmt.Errorf("center: claim application operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, errors.New("center: application operation changed while claiming")
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", taskRevision, "claimed", fmt.Sprintf("attempt %d", attempt+1)); err != nil {
		return nil, err
	}
	if node != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'applying', updated_at = ? WHERE worker_application_id = ?`, now.Format(time.RFC3339Nano), node.WorkerApplicationID); err != nil {
			return nil, err
		}
	}
	return &AgentTask{Kind: "application.command", ID: id, Attempt: attempt + 1, Revision: taskRevision, ApplicationCommand: reality, SubscriptionCommand: subscription, ClientCommand: client, NodeCommand: node, ControllerCommand: controller}, nil
}

func (s *Store) failUnclaimableThreeXUIInboundPlanCommand(ctx context.Context, tx *sql.Tx, commandID, agentID string, command ThreeXUIClientCommandTask, cause error) error {
	now := s.now().UTC()
	if command.Action == "reset_inbound_plan" {
		plan, err := readThreeXUIInboundPlan(ctx, tx, command.ServiceID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && plan.Revision == command.PlanRevision && plan.NextResetAt == command.ExpectedNextResetAt && plan.Status == "resetting" {
			if err := s.completeThreeXUIInboundPlanReset(ctx, tx, command, nil, false, applicationCommandFailureMessage(cause), now); err != nil {
				return err
			}
		}
	}
	return s.failUnclaimableApplicationCommand(ctx, tx, commandID, agentID, command.PlanRevision, nil, nil, cause)
}

func (s *Store) failUnclaimableSubscriptionCommand(ctx context.Context, tx *sql.Tx, commandID, agentID string, command SubscriptionCommandTask, cause error) error {
	message := applicationCommandFailureMessage(cause)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(command.PublicationID) != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'failed', last_error = ?, updated_at = ? WHERE id = ? AND status NOT IN ('ready', 'stopped')`, message, now, command.PublicationID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routes SET status = 'failed', last_error = ?, updated_at = ? WHERE publication_id = ? AND status <> 'ready'`, message, now, command.PublicationID); err != nil {
			return err
		}
	}
	return s.failUnclaimableApplicationCommand(ctx, tx, commandID, agentID, 1, nil, nil, cause)
}

func (s *Store) discardUnclaimableApplicationCommand(ctx context.Context, tx *sql.Tx, commandID, agentID string, revision int64, node *ThreeXUINodeCommandTask, controller *ThreeXUIControllerCommandTask, cause error) (*AgentTask, error) {
	if err := s.failUnclaimableApplicationCommand(ctx, tx, commandID, agentID, revision, node, controller, cause); err != nil {
		return nil, err
	}
	return nil, errApplicationCommandDiscarded
}

func applicationCommandFailureMessage(cause error) string {
	message := strings.TrimSpace(cause.Error())
	message = strings.TrimPrefix(message, errApplicationCommandUnavailable.Error()+": ")
	if len(message) > 1024 {
		message = message[:1024]
	}
	return message
}

func (s *Store) failUnclaimableApplicationCommand(ctx context.Context, tx *sql.Tx, commandID, agentID string, revision int64, node *ThreeXUINodeCommandTask, controller *ThreeXUIControllerCommandTask, cause error) error {
	message := applicationCommandFailureMessage(cause)
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	if node != nil && strings.TrimSpace(node.WorkerApplicationID) != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'failed', last_error = ?, updated_at = ? WHERE worker_application_id = ? AND status <> 'stopped'`, message, formattedNow, node.WorkerApplicationID); err != nil {
			return err
		}
		if strings.TrimSpace(node.MigrationID) != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET state = 'failed', last_error = ?, updated_at = ? WHERE id = ? AND state NOT IN ('ready', 'failed')`, message, formattedNow, node.MigrationID); err != nil {
				return err
			}
		}
	}
	if controller != nil {
		if controller.Action == "backup" && strings.TrimSpace(controller.ApplicationID) != "" && controller.BackupRevision > 0 {
			if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_backups SET state = 'failed', last_error = ?, updated_at = ? WHERE application_id = ? AND revision = ? AND state = 'pending'`, message, formattedNow, controller.ApplicationID, controller.BackupRevision); err != nil {
				return err
			}
		}
		if strings.TrimSpace(controller.MigrationID) != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE three_x_ui_migrations SET state = 'failed', last_error = ?, updated_at = ? WHERE id = ? AND state NOT IN ('ready', 'failed')`, message, formattedNow, controller.MigrationID); err != nil {
				return err
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'pending'`, message, now.Format(time.RFC3339Nano), commandID)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: application command changed while failing an unsafe claim")
	}
	if node != nil && strings.TrimSpace(node.MigrationID) != "" {
		if err := s.queueNextThreeXUINodeAfterMigration(ctx, tx, node.MigrationID, now); err != nil {
			return err
		}
	}
	if revision < 1 {
		revision = 1
	}
	return s.recordTaskEvent(ctx, tx, commandID, agentID, "application.command", revision, "failed", message)
}

func (s *Store) completeApplicationCommand(ctx context.Context, agentID, taskID string, expectedAttempt int64, succeeded bool, taskError string, rawResult json.RawMessage, reconciliationRequired bool) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applicationID, gatewayID, kind, currentState, appKey string
	var inputJSON []byte
	var attempt int64
	var currentReconciliationRequired int
	if err := tx.QueryRowContext(ctx, `SELECT command.application_id, command.gateway_node_id, command.kind, command.input_json, command.state, command.reconciliation_required, command.attempt, application.app_key
		FROM application_commands command JOIN applications application ON application.id = command.application_id
		WHERE command.id = ? AND command.agent_id = ?`, taskID, agentID).Scan(&applicationID, &gatewayID, &kind, &inputJSON, &currentState, &currentReconciliationRequired, &attempt, &appKey); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: application operation not found")
	} else if err != nil {
		return err
	}
	if currentState == "succeeded" || currentState == "failed" {
		if reconciliationRequired && (appKey != threeXUIAppKey || currentState != "failed" || currentReconciliationRequired != 1) {
			return errInvalidReconciliationDisposition
		}
		return nil
	}
	if currentState != "running" || expectedAttempt <= 0 || expectedAttempt != attempt {
		return errors.New("center: stale application operation result")
	}
	if reconciliationRequired {
		if succeeded || taskError == "" || appKey != threeXUIAppKey {
			return errInvalidReconciliationDisposition
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = 'failed', reconciliation_required = 1, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND agent_id = ? AND state = 'running' AND attempt = ?`, taskError, now, taskID, agentID, expectedAttempt)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("center: application operation is not active")
		}
		if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, "failed", taskError); err != nil {
			return err
		}
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET reconciliation_required = 0 WHERE id = ?`, taskID); err != nil {
		return err
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
	if kind == realityVerifyCommandKind {
		return s.completeRealityVerifyCommand(ctx, tx, taskID, agentID, inputJSON, succeeded, taskError, rawResult)
	}
	if kind == realityHardenCommandKind {
		return s.completeRealityHardenCommand(ctx, tx, taskID, agentID, applicationID, gatewayID, inputJSON, succeeded, taskError, rawResult)
	}
	if kind == realityRenameCommandKind {
		return s.completeRealityRenameCommand(ctx, tx, taskID, agentID, applicationID, inputJSON, succeeded, taskError, rawResult)
	}
	if kind != realityCommandKind {
		return errors.New("center: stored application operation kind is invalid")
	}
	return s.completeRealityCreateCommand(ctx, tx, taskID, agentID, applicationID, gatewayID, inputJSON, succeeded, taskError, rawResult)
}

func (s *Store) completeRealityVerifyCommand(ctx context.Context, tx *sql.Tx, taskID, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input RealityCommandTask
	var envelope ApplicationTaskResult
	if json.Unmarshal(inputJSON, &input) != nil || input.Action != "verify" {
		return errors.New("center: stored REALITY verification operation is invalid")
	}
	if succeeded {
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.ApplicationCommand == nil {
			succeeded = false
			taskError = "center: Agent returned an invalid REALITY verification result"
		} else {
			result := envelope.ApplicationCommand
			if result.Action != "verify" || result.TargetHost != input.TargetHost || result.ServerName != input.ServerName || net.ParseIP(result.TargetIP) == nil || result.NodeASN <= 0 || result.TargetASN != result.NodeASN || result.CDNProvider != "" || !result.TLS13 || !result.X25519 || !result.HTTP2 || !result.CertificateValid {
				succeeded = false
				taskError = "center: Agent returned an unsafe REALITY target verification"
			}
		}
	}
	if !succeeded && taskError == "" {
		taskError = "REALITY target verification failed"
	}
	state, event, message := "succeeded", "succeeded", "REALITY target verified"
	resultJSON := []byte(`{}`)
	if succeeded {
		resultJSON, _ = json.Marshal(envelope.ApplicationCommand)
	} else {
		state, event, message = "failed", "failed", taskError
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND state = 'running'`, state, resultJSON, taskError, now, taskID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) resumeSucceededRealityPublications(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT command.id, command.application_id, command.gateway_node_id, command.input_json, command.result_json
		FROM application_commands command
		JOIN services service ON service.application_id = command.application_id
		 AND service.name = 'inbound-' || CAST(json_extract(command.result_json, '$.inboundId') AS TEXT)
		JOIN three_x_ui_reality_guards guard ON guard.service_id = service.id AND guard.status = 'ready'
		WHERE command.kind = ? AND command.state = 'succeeded'`, realityCommandKind)
	if err != nil {
		return err
	}
	defer rows.Close()
	type recovery struct {
		commandID, applicationID, gatewayID string
		input                               RealityCommandTask
		result                              RealityCommandResult
	}
	recoveries := []recovery{}
	for rows.Next() {
		var value recovery
		var inputJSON, resultJSON []byte
		if err := rows.Scan(&value.commandID, &value.applicationID, &value.gatewayID, &inputJSON, &resultJSON); err != nil {
			return err
		}
		if json.Unmarshal(inputJSON, &value.input) != nil || json.Unmarshal(resultJSON, &value.result) != nil || value.result.InboundID < 1 {
			continue
		}
		recoveries = append(recoveries, value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, value := range recoveries {
		value := value
		s.startBackground(func() {
			var serviceID string
			err := s.db.QueryRowContext(s.backgroundCtx, `SELECT id FROM services WHERE application_id = ? AND name = ? AND status <> 'stopped'`, value.applicationID, fmt.Sprintf("inbound-%d", value.result.InboundID)).Scan(&serviceID)
			if err == nil {
				err = s.ensureRealityPublication(s.backgroundCtx, serviceID, value.gatewayID, value.input, value.result.ServerName)
			}
			warning := ""
			if err != nil {
				warning = "center: create REALITY access entry: " + err.Error()
			}
			_, _ = s.db.ExecContext(context.WithoutCancel(s.backgroundCtx), `UPDATE application_commands SET error = ?, updated_at = ? WHERE id = ? AND state = 'succeeded'`, warning, s.now().UTC().Format(time.RFC3339Nano), value.commandID)
		})
	}
	return nil
}

func (s *Store) completeRealityRenameCommand(ctx context.Context, tx *sql.Tx, taskID, agentID, applicationID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input RealityCommandTask
	if json.Unmarshal(inputJSON, &input) != nil || input.Action != "rename" || input.InboundID < 1 || !validRegionPrefixedRealityName(input.RegionCode, input.DisplayName) {
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
		result, err := tx.ExecContext(ctx, `UPDATE services SET display_name = ?, region_code = ?, updated_at = ? WHERE application_id = ? AND name = ? AND app_protocol = 'vless/tcp/reality' AND status <> 'stopped'`, input.DisplayName, input.RegionCode, now, applicationID, serviceName)
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
	var guardStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM three_x_ui_reality_guards WHERE service_id = ?`, serviceID).Scan(&guardStatus); err != nil || guardStatus != "ready" {
		return errors.New("center: REALITY service is not protected by a ready fallback guard")
	}
	var existingID, existingGateway, existingSNI, existingDNS, existingStatus string
	err := s.db.QueryRowContext(ctx, `SELECT id, COALESCE(gateway_node_id, ''), sni_hostname, dns_provider, status FROM publications WHERE service_id = ? AND kind = 'public_shared_443' AND hostname = ?`, serviceID, input.ConnectHostname).Scan(&existingID, &existingGateway, &existingSNI, &existingDNS, &existingStatus)
	if err == nil {
		// A stopped publication is an explicit user decision. Startup recovery is
		// allowed to finish a projection that never existed, but must never turn an
		// intentionally removed public entry back on.
		if existingStatus == "stopped" {
			return nil
		}
		if existingGateway != gatewayID || existingSNI != sniHostname || existingDNS != input.DNSProvider {
			return errors.New("center: existing REALITY access entry does not match the requested gateway, SNI, and DNS")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, createErr := s.CreatePublication(ctx, PublicationInput{ServiceID: serviceID, Kind: publicationShared443, GatewayNodeID: gatewayID, Hostname: input.ConnectHostname, SNIHostname: sniHostname, DNSProvider: input.DNSProvider})
	if createErr == nil {
		return nil
	}
	// CreatePublication commits desired state before external reconciliation.
	// If only its post-commit projection failed, the matching durable entry is
	// already the recovery source of truth and must not fail the parent command.
	lookupErr := s.db.QueryRowContext(ctx, `SELECT id, COALESCE(gateway_node_id, ''), sni_hostname, dns_provider, status FROM publications WHERE service_id = ? AND kind = 'public_shared_443' AND hostname = ?`, serviceID, input.ConnectHostname).Scan(&existingID, &existingGateway, &existingSNI, &existingDNS, &existingStatus)
	if lookupErr == nil && (existingStatus == "stopped" || existingGateway == gatewayID && existingSNI == sniHostname && existingDNS == input.DNSProvider) {
		return nil
	}
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return errors.Join(createErr, lookupErr)
	}
	return createErr
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
