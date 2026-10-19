package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"strconv"
	"strings"
	"time"
)

type RealityRemoveCommandInput struct {
	ServiceID string `json:"serviceId"`
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
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM three_x_ui_node_protocols WHERE service_id=? AND hy2_inbound_id>0)`, input.ServiceID).Scan(&task.RemoveHY2); err != nil {
		return ApplicationCommandView{}, err
	}
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
