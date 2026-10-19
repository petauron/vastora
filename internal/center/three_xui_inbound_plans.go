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
	maxThreeXUIResetDay              = 31
	threeXUIInboundPlanResetInterval = time.Minute
	threeXUIInboundPlanResetWakeKey  = "three-x-ui-inbound-plan-reset"
)

type threeXUIInboundPlan struct {
	ServiceID   string
	InboundTag  string
	TotalBytes  int64
	ResetDay    int
	NextResetAt string
	LastResetAt string
	Revision    int64
	Status      string
	RetryAt     string
	Attempt     int
	LastError   string
}

func threeXUIResetBoundary(now time.Time, resetDay int, location *time.Location) string {
	if resetDay <= 0 {
		return ""
	}
	if location == nil {
		location = time.UTC
	}
	local := now.In(location)
	next := monthlyBoundary(local.Year(), local.Month(), resetDay, location)
	if !next.After(local) {
		next = monthlyBoundary(local.Year(), local.Month()+1, resetDay, location)
	}
	return next.UTC().Format(time.RFC3339Nano)
}

func advanceThreeXUIResetBoundary(anchor string, resetDay int, now time.Time, location *time.Location) (string, error) {
	if resetDay <= 0 {
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
	current := now.In(location)
	for !local.After(current) {
		local = monthlyBoundary(local.Year(), local.Month()+1, resetDay, location)
	}
	return local.UTC().Format(time.RFC3339Nano), nil
}

func monthlyBoundary(year int, month time.Month, resetDay int, location *time.Location) time.Time {
	firstOfFollowingMonth := time.Date(year, month+1, 1, 0, 0, 0, 0, location)
	lastDay := firstOfFollowingMonth.AddDate(0, 0, -1).Day()
	if resetDay > lastDay {
		resetDay = lastDay
	}
	return time.Date(year, month, resetDay, 0, 0, 0, 0, location)
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

func nextThreeXUIInboundResetAt(ctx context.Context, tx *sql.Tx, serviceID string, now time.Time, resetDay int) (string, error) {
	if resetDay == 0 {
		return "", nil
	}
	location, err := threeXUIInboundPlanLocation(ctx, tx, serviceID)
	if err != nil {
		return "", err
	}
	return threeXUIResetBoundary(now, resetDay, location), nil
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
		WHERE reset_day > 0 AND next_reset_at <> '' AND julianday(next_reset_at) <= julianday(?)
		AND NOT EXISTS (SELECT 1 FROM meridian_endpoints endpoint WHERE endpoint.service_id=three_x_ui_inbound_plans.service_id)
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
	var imported bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_endpoints WHERE service_id=?)`, serviceID).Scan(&imported); err != nil {
		return err
	}
	if imported {
		return nil
	}
	boundary, boundaryErr := time.Parse(time.RFC3339Nano, plan.NextResetAt)
	retryAt, retryErr := time.Parse(time.RFC3339Nano, plan.RetryAt)
	failedRetryDue := plan.Status == "failed" && retryErr == nil && !retryAt.After(now)
	if plan.ResetDay <= 0 || boundaryErr != nil || boundary.After(now) || (plan.Status != "active" && !failedRetryDue) {
		return nil
	}
	var migrationActive int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM three_x_ui_migrations
		WHERE state IN ('backing_up', 'restoring', 'switching')
	)`).Scan(&migrationActive); err != nil {
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
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE (agent_id = ? OR kind='3xui.protocols.configure') AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, controllerCommandKind).Scan(&active); err != nil {
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
		InboundTotalBytes: plan.TotalBytes, InboundResetDay: plan.ResetDay,
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
	err := tx.QueryRowContext(ctx, `SELECT service_id, inbound_tag, total_bytes, reset_day, next_reset_at, last_reset_at, revision, status, retry_at, attempt, last_error
		FROM three_x_ui_inbound_plans WHERE service_id = ?`, serviceID).Scan(&plan.ServiceID, &plan.InboundTag, &plan.TotalBytes, &plan.ResetDay, &plan.NextResetAt, &plan.LastResetAt, &plan.Revision, &plan.Status, &plan.RetryAt, &plan.Attempt, &plan.LastError)
	return plan, err
}

func threeXUIInboundPlanController(ctx context.Context, tx *sql.Tx, serviceID string) (string, string, int, []ThreeXUIClientInbound, error) {
	var targetApplicationID, serviceName string
	err := tx.QueryRowContext(ctx, `SELECT service.application_id, service.name FROM services service
		JOIN applications target ON target.id = service.application_id
		WHERE service.id = ? AND service.app_protocol = 'vless/tcp/reality' AND service.status IN ('running', 'ready') AND target.status = 'running'`, serviceID).Scan(&targetApplicationID, &serviceName)
	if err != nil {
		return "", "", 0, nil, errors.New("center: active REALITY service is unavailable")
	}
	inboundID, err := strconv.Atoi(strings.TrimPrefix(serviceName, "inbound-"))
	if err != nil || inboundID < 1 {
		return "", "", 0, nil, errors.New("center: REALITY service has an invalid inbound identifier")
	}
	controllerApplicationID, agentID, err := runningGlobalThreeXUIController(ctx, tx)
	if err != nil {
		return "", "", 0, nil, errors.New("center: global 3x-ui subscription controller is unavailable")
	}
	inbounds, err := threeXUIClientInbounds(ctx, tx, controllerApplicationID)
	if err != nil {
		return "", "", 0, nil, err
	}
	if !threeXUIClientInboundMatches(inbounds, serviceID, inboundID) {
		return "", "", 0, nil, errors.New("center: REALITY inbound is unavailable from the global subscription controller")
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
