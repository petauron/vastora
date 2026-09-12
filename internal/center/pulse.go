package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/petauron/vastora/internal/pulse"
)

const pulseAppKey = pulse.ServiceKey
const pulseAgentAppKey = pulse.AgentKey

func nativeApplication(appKey string) bool {
	return appKey == komariAppKey || appKey == pulseAgentAppKey
}

// Called under deploymentCreateMu. A Pulse controller is global, not one per
// Site. Failed first installs may be retried; retained successful installs count.
func (s *Store) validatePulseDeployment(ctx context.Context, request DeploymentRequest) error {
	if request.AppKey != pulseAppKey {
		return nil
	}
	if request.Operation != "install" {
		var pending int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands c JOIN applications a ON a.id = c.application_id WHERE a.app_key = ? AND c.kind = ? AND c.state IN ('pending','running')`, pulseAppKey, pulse.EnrollmentKind).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return errors.New("center: wait for Pulse collector enrollment before changing the monitoring service")
		}
	}
	key := pulseAppKey
	if request.Operation == "uninstall" {
		key = pulseAgentAppKey
	}
	if request.Operation != "install" && request.Operation != "uninstall" {
		return nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments d WHERE d.app_key = ? AND (
		d.state IN ('pending','running') OR d.reconciliation_required = 1 OR
		(d.state = 'succeeded' AND d.operation <> 'uninstall' AND NOT EXISTS (
			SELECT 1 FROM deployments removed WHERE removed.agent_id = d.agent_id AND removed.app_key = d.app_key
			AND removed.operation = 'uninstall' AND removed.state = 'succeeded' AND removed.created_at > d.created_at)))`, key).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 && request.Operation == "install" {
		return errors.New("center: a global Pulse monitoring service already exists")
	}
	if count > 0 {
		return errors.New("center: remove Pulse collectors before uninstalling the monitoring service")
	}
	return nil
}

func (s *Store) pulseAgentConfig(ctx context.Context, nodeID string, options []byte) ([]byte, error) {
	var config pulse.AgentConfig
	if json.Unmarshal(options, &config) != nil {
		return nil, errors.New("center: invalid Pulse collector options")
	}
	var hostname string
	err := s.db.QueryRowContext(ctx, `SELECT a.id, p.hostname FROM applications a
		JOIN services sv ON sv.application_id = a.id AND sv.name = 'dashboard'
		JOIN publications p ON p.service_id = sv.id
		WHERE a.app_key = ? AND a.status = 'running' AND sv.status IN ('ready','publishing')
		AND p.kind IN ('headscale_gateway','lan_gateway') AND p.status = 'ready' AND p.tls_enabled = 1
		ORDER BY CASE p.kind WHEN 'headscale_gateway' THEN 0 ELSE 1 END, p.created_at LIMIT 1`, pulseAppKey).Scan(&config.ServiceApplicationID, &hostname)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: add a private HTTPS access point to the Pulse monitoring service first")
	}
	if err != nil {
		return nil, err
	}
	config.ServiceURL = (&url.URL{Scheme: "https", Host: hostname, Path: "/"}).String()
	if err := s.db.QueryRowContext(ctx, `SELECT a.name, s.name FROM agents a JOIN sites s ON s.id = a.site_id WHERE a.id = ?`, nodeID).Scan(&config.NodeName, &config.NodeGroup); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(config)
}

func (s *Store) queuePulseEnrollment(ctx context.Context, tx *sql.Tx, deployment DeploymentView, configJSON []byte, now time.Time) error {
	var config pulse.AgentConfig
	if json.Unmarshal(configJSON, &config) != nil || config.Validate() != nil {
		return errors.New("center: invalid Pulse enrollment configuration")
	}
	var serviceAgentID string
	if err := tx.QueryRowContext(ctx, `SELECT node_id FROM applications WHERE id = ? AND app_key = ? AND status = 'running'`, config.ServiceApplicationID, pulseAppKey).Scan(&serviceAgentID); err != nil {
		return errors.New("center: Pulse monitoring service is unavailable")
	}
	id, err := randomToken(18)
	if err != nil {
		return err
	}
	id = "application-command-" + id
	input, err := json.Marshal(pulse.EnrollmentTask{ApplicationID: config.ServiceApplicationID, DeploymentID: deployment.ID})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, config.ServiceApplicationID, serviceAgentID, deployment.AgentID, pulse.EnrollmentKind, input, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, id, serviceAgentID, "application.command", 1, "queued", "Prepare Pulse collector enrollment")
}

func (s *Store) completePulseEnrollment(ctx context.Context, tx *sql.Tx, commandID, agentID string, inputJSON []byte, succeeded bool, rawResult json.RawMessage) error {
	var input pulse.EnrollmentTask
	var result struct {
		Enrollment *pulse.EnrollmentResult `json:"pulseEnrollment"`
	}
	if json.Unmarshal(inputJSON, &input) != nil {
		return errors.New("center: invalid Pulse enrollment task")
	}
	if json.Unmarshal(rawResult, &result) != nil || result.Enrollment == nil || result.Enrollment.ID == "" || len(result.Enrollment.Token) < 16 || len(result.Enrollment.Token) > 512 || result.Enrollment.ExpiresAtUnixMS <= s.now().UnixMilli() {
		succeeded = false
	}
	var targetAgentID, applicationID, state string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id, application_id, state FROM deployments WHERE id = ? AND app_key = ? AND operation = 'install'`, input.DeploymentID, pulseAgentAppKey).Scan(&targetAgentID, &applicationID, &state); err != nil {
		return err
	}
	if state != "pending" {
		return errors.New("center: Pulse collector installation is no longer pending")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	commandState, message := "succeeded", ""
	if succeeded {
		// The enrollment secret is never copied to result_json, events, or UI.
		encoded, _ := json.Marshal(map[string]string{"enrollment_token": result.Enrollment.Token})
		secretID, err := s.putSecret(ctx, tx, encoded, "deployment:"+input.DeploymentID)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET secret_id = ?, updated_at = ? WHERE id = ?`, secretID, now, input.DeploymentID); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, input.DeploymentID, targetAgentID, "application.apply", 1, "queued", "Pulse collector is ready to install"); err != nil {
			return err
		}
	} else {
		commandState, message = "failed", "Pulse enrollment could not be prepared. Retry installation."
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET state = 'failed', error = ?, updated_at = ? WHERE id = ?`, message, now, input.DeploymentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'failed', updated_at = ? WHERE id = ?`, now, applicationID); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, input.DeploymentID, targetAgentID, "application.apply", 1, "failed", message); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET state = ?, result_json = '{}', error = ?, lease_expires_at = '', updated_at = ? WHERE id = ?`, commandState, message, now, commandID); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, commandID, agentID, "application.command", 1, commandState, message); err != nil {
		return err
	}
	return tx.Commit()
}
