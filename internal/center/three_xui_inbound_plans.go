package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	maxThreeXUIResetDays             = 3650
	threeXUIInboundPlanResetInterval = time.Minute
	threeXUIInboundPlanResetWakeKey  = "three-x-ui-inbound-plan-reset"
)

type threeXUIInboundPlan struct {
	ServiceID   string
	InboundTag  string
	TotalBytes  int64
	ResetDays   int
	NextResetAt string
	LastResetAt string
	Revision    int64
	Status      string
	RetryAt     string
	Attempt     int
	LastError   string
}

func threeXUIResetBoundary(now time.Time, resetDays int, location *time.Location) string {
	if resetDays <= 0 {
		return ""
	}
	if location == nil {
		location = time.UTC
	}
	local := now.In(location)
	next := time.Date(local.Year(), local.Month(), local.Day()+resetDays, 0, 0, 0, 0, location)
	return next.UTC().Format(time.RFC3339Nano)
}

func advanceThreeXUIResetBoundary(anchor string, resetDays int, now time.Time, location *time.Location) (string, error) {
	if resetDays <= 0 {
		return "", nil
	}
	if location == nil {
		location = time.UTC
	}
	boundary, err := time.Parse(time.RFC3339Nano, anchor)
	if err != nil {
		return "", errors.New("center: stored REALITY traffic reset boundary is invalid")
	}
	local := boundary.In(location)
	for !local.After(now.In(location)) {
		local = local.AddDate(0, 0, resetDays)
	}
	return local.UTC().Format(time.RFC3339Nano), nil
}

func threeXUIInboundPlanLocation(ctx context.Context, tx *sql.Tx, serviceID string) (*time.Location, error) {
	var timezone string
	err := tx.QueryRowContext(ctx, `SELECT site.timezone FROM services service JOIN sites site ON site.id = service.site_id WHERE service.id = ?`, serviceID).Scan(&timezone)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: REALITY service not found")
	}
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, errors.New("center: REALITY service timezone is invalid")
	}
	return location, nil
}

func nextThreeXUIInboundResetAt(ctx context.Context, tx *sql.Tx, serviceID string, now time.Time, resetDays int) (string, error) {
	if resetDays == 0 {
		return "", nil
	}
	location, err := threeXUIInboundPlanLocation(ctx, tx, serviceID)
	if err != nil {
		return "", err
	}
	return threeXUIResetBoundary(now, resetDays, location), nil
}

func upsertThreeXUIInboundPlan(ctx context.Context, tx *sql.Tx, serviceID, inboundTag string, totalBytes int64, resetDays int, nextResetAt string, revision int64, now time.Time) error {
	if strings.TrimSpace(serviceID) == "" || strings.TrimSpace(inboundTag) == "" || totalBytes < 0 || resetDays < 0 || resetDays > maxThreeXUIResetDays || revision < 1 {
		return errors.New("center: invalid REALITY inbound traffic plan")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(
		service_id, inbound_tag, total_bytes, reset_days, next_reset_at, last_reset_at,
		revision, status, retry_at, attempt, last_error, updated_at
	) VALUES(?, ?, ?, ?, ?, '', ?, 'active', '', 0, '', ?)
	ON CONFLICT(service_id) DO UPDATE SET
		inbound_tag = excluded.inbound_tag,
		total_bytes = excluded.total_bytes,
		reset_days = excluded.reset_days,
		next_reset_at = excluded.next_reset_at,
		revision = excluded.revision,
		status = 'active',
		retry_at = '',
		attempt = 0,
		last_error = '',
		updated_at = excluded.updated_at`, serviceID, inboundTag, totalBytes, resetDays, nextResetAt, revision, now.UTC().Format(time.RFC3339Nano))
	return err
}

func threeXUIInboundResetOperationKey(serviceID, nextResetAt string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(serviceID) + "\x00" + strings.TrimSpace(nextResetAt)))
	return "3xui-inbound-reset-" + hex.EncodeToString(digest[:18])
}

func (s *Store) RunThreeXUIInboundPlanResets(ctx context.Context, interval time.Duration, report func(error)) {
	if interval <= 0 {
		interval = threeXUIInboundPlanResetInterval
	}
	run := func() {
		if err := s.queueDueThreeXUIInboundPlanResets(ctx); err != nil && report != nil && !errors.Is(err, context.Canceled) {
			report(err)
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		wake := s.taskChanges.subscribe(threeXUIInboundPlanResetWakeKey)
		run()
		select {
		case <-ctx.Done():
			s.taskChanges.unsubscribe(threeXUIInboundPlanResetWakeKey, wake)
			return
		case <-ticker.C:
		case <-wake:
		}
		s.taskChanges.unsubscribe(threeXUIInboundPlanResetWakeKey, wake)
	}
}

func (s *Store) queueDueThreeXUIInboundPlanResets(ctx context.Context) error {
	now := s.now().UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT service_id FROM three_x_ui_inbound_plans
		WHERE reset_days > 0 AND next_reset_at <> '' AND julianday(next_reset_at) <= julianday(?)
		AND (status = 'active' OR (status = 'failed' AND retry_at <> '' AND julianday(retry_at) <= julianday(?)))
		ORDER BY next_reset_at, service_id LIMIT 64`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("center: inspect due REALITY traffic plans: %w", err)
	}
	serviceIDs := []string{}
	for rows.Next() {
		var serviceID string
		if err := rows.Scan(&serviceID); err != nil {
			rows.Close()
			return err
		}
		serviceIDs = append(serviceIDs, serviceID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, serviceID := range serviceIDs {
		if err := s.queueThreeXUIInboundPlanReset(ctx, serviceID, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queueThreeXUIInboundPlanReset(ctx context.Context, serviceID string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	plan, err := readThreeXUIInboundPlan(ctx, tx, serviceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	boundary, boundaryErr := time.Parse(time.RFC3339Nano, plan.NextResetAt)
	retryAt, retryErr := time.Parse(time.RFC3339Nano, plan.RetryAt)
	failedRetryDue := plan.Status == "failed" && retryErr == nil && !retryAt.After(now)
	if plan.ResetDays <= 0 || boundaryErr != nil || boundary.After(now) || (plan.Status != "active" && !failedRetryDue) {
		return nil
	}
	var migrationActive int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM three_x_ui_migrations migration JOIN services service ON service.site_id = migration.site_id
		WHERE service.id = ? AND migration.state IN ('backing_up', 'restoring', 'switching')
	)`, serviceID).Scan(&migrationActive); err != nil {
		return err
	}
	if migrationActive != 0 {
		return nil
	}
	controllerApplicationID, agentID, inboundID, inbounds, err := threeXUIInboundPlanController(ctx, tx, serviceID)
	if err != nil {
		return failThreeXUIInboundPlanBeforeDispatch(ctx, tx, plan, err, now)
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, controllerCommandKind).Scan(&active); err != nil {
		return err
	}
	operationKey := threeXUIInboundResetOperationKey(serviceID, plan.NextResetAt)
	commandID := "application-command-" + operationKey
	var existingState string
	existingErr := tx.QueryRowContext(ctx, `SELECT state FROM application_commands WHERE id = ?`, commandID).Scan(&existingState)
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return existingErr
	}
	if active != 0 {
		return nil
	}
	var resetInbound ThreeXUIClientInbound
	for index := range inbounds {
		if inbounds[index].ServiceID == serviceID && inbounds[index].ID == inboundID {
			inbounds[index].PlanStatus = "resetting"
			inbounds[index].PlanError = ""
			resetInbound = inbounds[index]
			break
		}
	}
	if resetInbound.ID == 0 {
		return failThreeXUIInboundPlanBeforeDispatch(ctx, tx, plan, errors.New("REALITY inbound disappeared while scheduling its traffic reset"), now)
	}
	task := ThreeXUIClientCommandTask{
		Action: "reset_inbound_plan", ServiceID: serviceID, InboundID: inboundID,
		InboundTotalBytes: plan.TotalBytes, InboundResetDays: plan.ResetDays,
		ExpectedNextResetAt: plan.NextResetAt, PlanRevision: plan.Revision,
		OperationKey: operationKey, InboundTag: plan.InboundTag, Inbounds: []ThreeXUIClientInbound{resetInbound},
	}
	encoded, _ := json.Marshal(task)
	switch {
	case errors.Is(existingErr, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, commandID, controllerApplicationID, agentID, agentID, clientCommandKind, encoded, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	case existingState == "failed":
		if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET application_id = ?, agent_id = ?, gateway_node_id = ?, input_json = ?, result_json = '{}', result_secret_id = NULL, state = 'pending', lease_expires_at = '', error = '', updated_at = ? WHERE id = ? AND state = 'failed'`, controllerApplicationID, agentID, agentID, encoded, now.Format(time.RFC3339Nano), commandID); err != nil {
			return err
		}
	case existingState == "pending" || existingState == "running":
		return nil
	default:
		return failThreeXUIInboundPlanBeforeDispatch(ctx, tx, plan, errors.New("completed reset operation did not advance its traffic plan"), now)
	}
	planUpdate, err := tx.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET status = 'resetting', retry_at = '', attempt = attempt + 1, last_error = '', updated_at = ?
		WHERE service_id = ? AND revision = ? AND next_reset_at = ? AND status IN ('active', 'failed')`, now.Format(time.RFC3339Nano), serviceID, plan.Revision, plan.NextResetAt)
	if err != nil {
		return err
	}
	if changed, _ := planUpdate.RowsAffected(); changed != 1 {
		return errors.New("center: REALITY inbound traffic plan changed while scheduling its reset")
	}
	if err := s.recordTaskEvent(ctx, tx, commandID, agentID, "application.command", plan.Revision, "queued", "3x-ui REALITY inbound traffic reset queued"); err != nil {
		return err
	}
	return tx.Commit()
}

func readThreeXUIInboundPlan(ctx context.Context, tx *sql.Tx, serviceID string) (threeXUIInboundPlan, error) {
	var plan threeXUIInboundPlan
	err := tx.QueryRowContext(ctx, `SELECT service_id, inbound_tag, total_bytes, reset_days, next_reset_at, last_reset_at, revision, status, retry_at, attempt, last_error
		FROM three_x_ui_inbound_plans WHERE service_id = ?`, serviceID).Scan(&plan.ServiceID, &plan.InboundTag, &plan.TotalBytes, &plan.ResetDays, &plan.NextResetAt, &plan.LastResetAt, &plan.Revision, &plan.Status, &plan.RetryAt, &plan.Attempt, &plan.LastError)
	return plan, err
}

func threeXUIInboundPlanController(ctx context.Context, tx *sql.Tx, serviceID string) (string, string, int, []ThreeXUIClientInbound, error) {
	var targetApplicationID, siteID, serviceName string
	err := tx.QueryRowContext(ctx, `SELECT service.application_id, service.site_id, service.name FROM services service
		JOIN applications target ON target.id = service.application_id
		WHERE service.id = ? AND service.app_protocol = 'vless/tcp/reality' AND service.status IN ('running', 'ready') AND target.status = 'running'`, serviceID).Scan(&targetApplicationID, &siteID, &serviceName)
	if err != nil {
		return "", "", 0, nil, errors.New("center: active REALITY service is unavailable")
	}
	inboundID, err := strconv.Atoi(strings.TrimPrefix(serviceName, "inbound-"))
	if err != nil || inboundID < 1 {
		return "", "", 0, nil, errors.New("center: REALITY service has an invalid inbound identifier")
	}
	var controllerApplicationID, agentID string
	err = tx.QueryRowContext(ctx, `SELECT id, node_id FROM applications WHERE site_id = ? AND app_key = ? AND role = 'master' AND status = 'running'`, siteID, threeXUIAppKey).Scan(&controllerApplicationID, &agentID)
	if err != nil {
		return "", "", 0, nil, errors.New("center: Site 3x-ui controller is unavailable")
	}
	inbounds, err := threeXUIClientInbounds(ctx, tx, controllerApplicationID)
	if err != nil {
		return "", "", 0, nil, err
	}
	if !threeXUIClientInboundMatches(inbounds, serviceID, inboundID) {
		return "", "", 0, nil, errors.New("center: REALITY inbound is unavailable from the Site controller")
	}
	return controllerApplicationID, agentID, inboundID, inbounds, nil
}

func failThreeXUIInboundPlanBeforeDispatch(ctx context.Context, tx *sql.Tx, plan threeXUIInboundPlan, cause error, now time.Time) error {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 1024 {
		message = message[:1024]
	}
	retry := now.Add(5 * time.Minute).Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET status = 'failed', retry_at = ?, last_error = ?, updated_at = ? WHERE service_id = ? AND revision = ? AND next_reset_at = ?`, retry, message, now.Format(time.RFC3339Nano), plan.ServiceID, plan.Revision, plan.NextResetAt)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: REALITY inbound traffic plan changed before dispatch")
	}
	return tx.Commit()
}

func threeXUIInboundResetRetryAt(now time.Time, attempt int) string {
	if attempt < 1 {
		attempt = 1
	}
	shift := attempt - 1
	if shift > 6 {
		shift = 6
	}
	return now.Add(time.Minute * time.Duration(1<<shift)).Format(time.RFC3339Nano)
}

func (s *Store) hydrateThreeXUIInboundPlanTask(ctx context.Context, tx *sql.Tx, command *ThreeXUIClientCommandTask) error {
	if command == nil || (command.Action != "update_inbound" && command.Action != "reset_inbound_plan") || command.ServiceID == "" || command.InboundID < 1 || command.PlanRevision < 1 {
		return fmt.Errorf("%w: center: stored REALITY inbound traffic operation is invalid", errApplicationCommandUnavailable)
	}
	plan, err := readThreeXUIInboundPlan(ctx, tx, command.ServiceID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: center: REALITY inbound traffic plan is unavailable", errApplicationCommandUnavailable)
	}
	if err != nil {
		return err
	}
	if command.InboundTag != "" && plan.InboundTag != command.InboundTag {
		return fmt.Errorf("%w: center: REALITY inbound traffic plan is unavailable", errApplicationCommandUnavailable)
	}
	if command.Action == "update_inbound" {
		if command.PlanRevision != plan.Revision+1 {
			return fmt.Errorf("%w: center: REALITY inbound traffic plan changed before applying", errApplicationCommandUnavailable)
		}
	} else if plan.InboundTag == "" || command.InboundTag != plan.InboundTag || command.PlanRevision != plan.Revision || command.ExpectedNextResetAt != plan.NextResetAt || command.OperationKey != threeXUIInboundResetOperationKey(command.ServiceID, command.ExpectedNextResetAt) {
		return fmt.Errorf("%w: center: stale REALITY inbound traffic reset operation", errApplicationCommandUnavailable)
	}
	var role, targetAddress string
	var configJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT target.id, target.role, COALESCE(profile.service_address, ''), deployment.config_json
		FROM services service JOIN applications target ON target.id = service.application_id
		LEFT JOIN agent_network_profiles profile ON profile.agent_id = target.node_id
		JOIN deployments deployment ON deployment.rowid = (
			SELECT latest.rowid FROM deployments latest WHERE latest.application_id = target.id
			AND latest.operation IN ('install', 'upgrade', 'configure') AND latest.state = 'succeeded'
			ORDER BY latest.created_at DESC, latest.rowid DESC LIMIT 1
		)
		WHERE service.id = ? AND service.app_protocol = 'vless/tcp/reality' AND target.status = 'running'`, command.ServiceID).Scan(&command.TargetApplicationID, &role, &targetAddress, &configJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: center: target REALITY application connection is unavailable", errApplicationCommandUnavailable)
	}
	if err != nil {
		return err
	}
	var config struct {
		PanelPort int `json:"panel_port"`
	}
	if json.Unmarshal(configJSON, &config) != nil || config.PanelPort < 1024 || config.PanelPort > 65535 {
		return fmt.Errorf("%w: center: target REALITY application API configuration is invalid", errApplicationCommandUnavailable)
	}
	command.TargetAddress = targetAddress
	command.TargetPanelPort = config.PanelPort
	if role == threeXUIRoleMaster {
		command.TargetNodeID = 0
		return nil
	}
	if role != threeXUIRoleWorker {
		return fmt.Errorf("%w: center: target REALITY application topology is invalid", errApplicationCommandUnavailable)
	}
	if err := tx.QueryRowContext(ctx, `SELECT remote_node_id FROM three_x_ui_nodes WHERE worker_application_id = ? AND status = 'ready'`, command.TargetApplicationID).Scan(&command.TargetNodeID); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: center: target REALITY worker is not connected to its Site controller", errApplicationCommandUnavailable)
	} else if err != nil {
		return err
	} else if command.TargetNodeID < 1 {
		return fmt.Errorf("%w: center: target REALITY worker is not connected to its Site controller", errApplicationCommandUnavailable)
	}
	command.TargetAPIToken, err = s.threeXUIAPISecret(ctx, tx, command.TargetApplicationID)
	if err != nil {
		return err
	}
	return nil
}

func completeThreeXUIInboundPlanUpdate(ctx context.Context, tx *sql.Tx, input ThreeXUIClientCommandTask, result *ThreeXUIClientCommandResult, now time.Time) error {
	if input.Action != "update_inbound" || input.ServiceID == "" || input.InboundID < 1 || input.PlanRevision < 2 {
		return errors.New("center: stored REALITY inbound traffic plan update is invalid")
	}
	plan, err := readThreeXUIInboundPlan(ctx, tx, input.ServiceID)
	if err != nil {
		return errors.New("center: REALITY inbound traffic plan is unavailable")
	}
	if plan.Revision != input.PlanRevision-1 {
		return errors.New("center: REALITY inbound traffic plan changed while applying")
	}
	inboundTag := ""
	for _, inbound := range result.Inbounds {
		if inbound.ServiceID == input.ServiceID && inbound.ID == input.InboundID {
			inboundTag = strings.TrimSpace(inbound.InboundTag)
			break
		}
	}
	if inboundTag == "" {
		return errors.New("center: Agent did not observe the managed REALITY inbound tag")
	}
	changed, err := tx.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET inbound_tag = ?, total_bytes = ?, reset_days = ?, next_reset_at = ?, revision = ?, status = 'active', retry_at = '', attempt = 0, last_error = '', updated_at = ?
		WHERE service_id = ? AND revision = ?`, inboundTag, input.InboundTotalBytes, input.InboundResetDays, input.ExpectedNextResetAt, input.PlanRevision, now.Format(time.RFC3339Nano), input.ServiceID, plan.Revision)
	if err != nil {
		return err
	}
	if rows, _ := changed.RowsAffected(); rows != 1 {
		return errors.New("center: REALITY inbound traffic plan changed while applying")
	}
	for index := range result.Inbounds {
		if result.Inbounds[index].ServiceID == input.ServiceID && result.Inbounds[index].ID == input.InboundID {
			result.Inbounds[index].ResetDays = input.InboundResetDays
			result.Inbounds[index].NextResetAt = input.ExpectedNextResetAt
			result.Inbounds[index].PlanStatus = "active"
			result.Inbounds[index].PlanError = ""
		}
	}
	return nil
}

func (s *Store) completeThreeXUIInboundPlanReset(ctx context.Context, tx *sql.Tx, input ThreeXUIClientCommandTask, result *ThreeXUIClientCommandResult, succeeded bool, taskError string, now time.Time) error {
	if input.Action != "reset_inbound_plan" || input.ServiceID == "" || input.InboundID < 1 || input.ExpectedNextResetAt == "" || input.PlanRevision < 1 || input.OperationKey != threeXUIInboundResetOperationKey(input.ServiceID, input.ExpectedNextResetAt) {
		return errors.New("center: stored REALITY inbound traffic reset is invalid")
	}
	plan, err := readThreeXUIInboundPlan(ctx, tx, input.ServiceID)
	if err != nil {
		return errors.New("center: REALITY inbound traffic plan is unavailable")
	}
	if plan.Revision != input.PlanRevision || plan.NextResetAt != input.ExpectedNextResetAt || plan.Status != "resetting" {
		return errors.New("center: stale REALITY inbound traffic reset result")
	}
	if !succeeded {
		retryAt := threeXUIInboundResetRetryAt(now, plan.Attempt)
		changed, err := tx.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET status = 'failed', retry_at = ?, last_error = ?, updated_at = ?
			WHERE service_id = ? AND revision = ? AND next_reset_at = ? AND status = 'resetting'`, retryAt, taskError, now.Format(time.RFC3339Nano), input.ServiceID, input.PlanRevision, input.ExpectedNextResetAt)
		if err != nil {
			return err
		}
		if rows, _ := changed.RowsAffected(); rows != 1 {
			return errors.New("center: stale REALITY inbound traffic reset result")
		}
		return nil
	}
	location, err := threeXUIInboundPlanLocation(ctx, tx, input.ServiceID)
	if err != nil {
		return err
	}
	nextResetAt, err := advanceThreeXUIResetBoundary(input.ExpectedNextResetAt, plan.ResetDays, now, location)
	if err != nil {
		return err
	}
	changed, err := tx.ExecContext(ctx, `UPDATE three_x_ui_inbound_plans SET next_reset_at = ?, last_reset_at = ?, status = 'active', retry_at = '', attempt = 0, last_error = '', updated_at = ?
		WHERE service_id = ? AND revision = ? AND next_reset_at = ? AND status = 'resetting'`, nextResetAt, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), input.ServiceID, input.PlanRevision, input.ExpectedNextResetAt)
	if err != nil {
		return err
	}
	if rows, _ := changed.RowsAffected(); rows != 1 {
		return errors.New("center: stale REALITY inbound traffic reset result")
	}
	if result != nil {
		for index := range result.Inbounds {
			if result.Inbounds[index].ServiceID == input.ServiceID && result.Inbounds[index].ID == input.InboundID {
				result.Inbounds[index].NextResetAt = nextResetAt
				result.Inbounds[index].PlanStatus = "active"
				result.Inbounds[index].PlanError = ""
			}
		}
	}
	return nil
}
