package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type RealityRemoveCommandInput struct {
	ServiceID string `json:"serviceId"`
}

func (s *Server) handleRemoveRealityCommand(writer http.ResponseWriter, request *http.Request) {
	var input RealityRemoveCommandInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	value, err := s.store.CreateRealityRemoveCommand(request.Context(), input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, value)
}

func (s *Store) CreateRealityRemoveCommand(ctx context.Context, input RealityRemoveCommandInput) (ApplicationCommandView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	applicationID, agentID, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil {
		return ApplicationCommandView{}, errors.New("center: subscription controller is unavailable")
	}
	input.ServiceID = strings.TrimSpace(input.ServiceID)
	var siteID, name, displayName, tag string
	err = tx.QueryRowContext(ctx, `SELECT s.site_id,s.name,s.display_name,p.inbound_tag FROM services s JOIN three_x_ui_inbound_plans p ON p.service_id=s.id
		WHERE s.id=? AND s.application_id=? AND s.source='observed' AND s.app_protocol='vless/tcp/reality'`, input.ServiceID, applicationID).Scan(&siteID, &name, &displayName, &tag)
	if err != nil || !observedEndpointNamePattern.MatchString(name) || !validThreeXUIInboundTag(tag) {
		return ApplicationCommandView{}, errors.New("center: select the subscription controller's managed local VLESS node")
	}
	inboundID, err := strconv.Atoi(strings.TrimPrefix(name, "inbound-"))
	if err != nil || inboundID < 1 {
		return ApplicationCommandView{}, errors.New("center: local VLESS inbound identifier is invalid")
	}
	// Resume the same immutable command after a page reload or a lost response.
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM application_commands WHERE application_id=? AND kind=? AND json_extract(input_json,'$.serviceId')=?
		AND (state IN ('pending','running') OR reconciliation_required=1) ORDER BY created_at DESC LIMIT 1`, applicationID, realityRemoveCommandKind, input.ServiceID).Scan(&existing)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return ApplicationCommandView{}, err
		}
		return s.ApplicationCommand(ctx, existing)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, err
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1)) +
		(SELECT COUNT(*) FROM deployments WHERE agent_id=? AND app_key='vastora-official/3x-ui' AND (state IN ('pending','running') OR reconciliation_required=1)) +
		(SELECT COUNT(*) FROM three_x_ui_migrations WHERE state IN ('backing_up','restoring','switching')) +
		(SELECT COUNT(*) FROM services WHERE application_id=? AND id<>? AND app_protocol='vless/tcp/reality' AND status<>'stopped')`, agentID, agentID, applicationID, input.ServiceID).Scan(&busy); err != nil {
		return ApplicationCommandView{}, err
	}
	if busy > 0 {
		return ApplicationCommandView{}, errors.New("center: finish the current controller operation before removing its local node")
	}
	// Restore any saved landing route before deleting its inbound. This shares
	// the normal revisioned restoration and authorized-source retirement path.
	var revision uint64
	var restored bool
	err = tx.QueryRowContext(ctx, `SELECT desired_revision,status='stopped' AND desired_revision=applied_revision FROM landing_proxy_states WHERE node_id=?`, agentID).Scan(&revision, &restored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, err
	}
	if revision > 0 && !restored {
		if err := s.configureLandingProxy(ctx, tx, applicationID, LandingProxyInput{Revision: revision}); err != nil {
			return ApplicationCommandView{}, err
		}
	}
	task := RealityCommandTask{Action: "remove", ServiceID: input.ServiceID, TargetApplicationID: applicationID, InboundID: inboundID, InboundTag: tag, DisplayName: displayName}
	encoded, err := json.Marshal(task)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'pending',?,?)`, id, applicationID, siteID, displayName, agentID, agentID, realityRemoveCommandKind, encoded, now, now); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "queued", "local VLESS node removal queued"); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}

func (s *Store) realityRemovalReady(ctx context.Context, tx *sql.Tx, agentID string, input RealityCommandTask) (bool, error) {
	if input.Action != "remove" || input.TargetNodeID != 0 || input.InboundID < 1 || input.ServiceID == "" || !validThreeXUIInboundTag(input.InboundTag) {
		return false, errors.New("center: stored local VLESS removal is invalid")
	}
	applicationID, controllerAgentID, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil || applicationID != input.TargetApplicationID || controllerAgentID != agentID {
		return false, errors.New("center: subscription controller changed; refresh before removing its node")
	}
	var matches int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services s JOIN three_x_ui_inbound_plans p ON p.service_id=s.id WHERE s.id=? AND s.application_id=? AND s.source='observed' AND s.name=? AND s.app_protocol='vless/tcp/reality' AND p.inbound_tag=?`, input.ServiceID, applicationID, fmt.Sprintf("inbound-%d", input.InboundID), input.InboundTag).Scan(&matches); err != nil {
		return false, err
	}
	if matches != 1 {
		return false, errors.New("center: local VLESS node identity changed")
	}
	var status string
	var restored bool
	err = tx.QueryRowContext(ctx, `SELECT status,status='stopped' AND desired_revision=applied_revision AND json_extract(desired_json,'$.proxy') IS NULL FROM landing_proxy_states WHERE node_id=?`, agentID).Scan(&status, &restored)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if status == "failed" || status == "ready" {
		return false, errors.New("center: restore the node's own exit before retrying removal")
	}
	return restored, nil
}

func (s *Store) completeRealityRemoveCommand(ctx context.Context, tx *sql.Tx, taskID, agentID, applicationID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var input RealityCommandTask
	if json.Unmarshal(inputJSON, &input) != nil || input.Action != "remove" || input.TargetApplicationID != applicationID || input.TargetNodeID != 0 || input.ServiceID == "" || input.InboundID < 1 || !validThreeXUIInboundTag(input.InboundTag) {
		return errors.New("center: stored local VLESS removal is invalid")
	}
	var envelope ApplicationTaskResult
	if succeeded && (json.Unmarshal(rawResult, &envelope) != nil || envelope.ApplicationCommand == nil || envelope.ApplicationCommand.Action != "remove" || envelope.ApplicationCommand.InboundID != input.InboundID || envelope.ApplicationCommand.InboundTag != input.InboundTag) {
		succeeded, taskError = false, "center: Agent did not confirm the requested local VLESS removal"
	}
	now := s.now().UTC()
	state, message := "succeeded", "local VLESS node removed; subscription controller retained"
	resultJSON := []byte(`{}`)
	if succeeded {
		ready, err := s.realityRemovalReady(ctx, tx, agentID, input)
		if err != nil {
			return err
		}
		if !ready {
			return errors.New("center: local exit restoration is not confirmed")
		}
		// Keep the stopped service identity as a retry/audit checkpoint. Only
		// this service's public entries and node-local listener route are retired.
		var cleanups []publicationCleanup
		if err := s.stopServicePublications(ctx, tx, input.ServiceID, now, &cleanups); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET action_required=0 WHERE service_id=?`, input.ServiceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status='stopped',last_error='',updated_at=? WHERE id=?`, now.Format(time.RFC3339Nano), input.ServiceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM three_x_ui_reality_guards WHERE service_id=?`, input.ServiceID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM three_x_ui_inbound_plans WHERE service_id=?`, input.ServiceID); err != nil {
			return err
		}
		if err := s.queueNodeListenerState(ctx, tx, agentID, now); err != nil {
			return err
		}
		// External DNS cleanup is persisted via cleanup_pending and retried by
		// the normal cleanup worker, not coupled to acknowledgement delivery.
		resultJSON, _ = json.Marshal(envelope.ApplicationCommand)
		taskError = ""
	} else {
		state = "failed"
		if taskError == "" {
			taskError = "local VLESS node removal failed"
		}
		message = taskError
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json=?,lease_expires_at='',error=?,updated_at=? WHERE id=? AND state='running'`, state, resultJSON, taskError, now.Format(time.RFC3339Nano), taskID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, state, message); err != nil {
		return err
	}
	return tx.Commit()
}
