package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/senulun"
)

const senulunAppKey = senulun.ServiceKey
const senulunAgentAppKey = senulun.AgentKey

// Fill a missing historical browser origin from the existing access entry.
// Administrator setup belongs to Senulun, not Center.
func (s *Store) withSenulunPublicOrigin(ctx context.Context, agentID, operation string, raw []byte) ([]byte, error) {
	values, err := decodeJSONObject(raw, "center: Senulun configuration must be a JSON object")
	if err != nil {
		return nil, err
	}
	var publicURL string
	if field, exists := values["public_url"]; exists && json.Unmarshal(field, &publicURL) != nil {
		return nil, errors.New("center: Senulun dashboard HTTPS address is invalid")
	}
	if strings.TrimSpace(publicURL) == "" && operation == "upgrade" {
		var hostname string
		err = s.db.QueryRowContext(ctx, `SELECT p.hostname FROM publications p
			JOIN services service ON service.id = p.service_id
			JOIN applications app ON app.id = service.application_id
			WHERE app.node_id = ? AND app.app_key = ? AND service.name = 'dashboard'
			AND p.status = 'ready' AND p.tls_enabled = 1
			ORDER BY CASE p.kind WHEN 'cloudflare_tunnel' THEN 0 WHEN 'public_direct' THEN 1
				WHEN 'headscale_gateway' THEN 2 WHEN 'lan_gateway' THEN 3 ELSE 4 END, p.updated_at DESC LIMIT 1`, agentID, senulunAppKey).Scan(&hostname)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("center: add a ready Senulun dashboard HTTPS access point before upgrading")
		}
		if err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal((&url.URL{Scheme: "https", Host: hostname}).String())
		values["public_url"] = encoded
	}
	// Superseded bootstrap values remain in historical deployment evidence;
	// current Senulun creates its administrator through its own login page.
	delete(values, "setup_token")
	return json.Marshal(values)
}

// A service can be independently installed on any eligible host. Retained
// lifecycle commands still have to finish before replacing their actual owner.
func (s *Store) validateSenulunDeployment(ctx context.Context, request DeploymentRequest) error {
	if request.AppKey != senulunAppKey {
		return nil
	}
	if request.Operation != "install" {
		var pending int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands c JOIN applications a ON a.id = c.application_id WHERE a.app_key = ? AND a.node_id = ? AND c.kind IN (?,?,?,?) AND c.state IN ('pending','running')`, senulunAppKey, request.AgentID, senulun.EnrollmentKind, senulun.InspectionKind, senulun.RotationKind, senulun.ReportingKind).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return errors.New("center: wait for Senulun collector enrollment before changing the monitoring service")
		}
	}
	return nil
}

func (s *Store) completeSenulunEnrollment(ctx context.Context, commit projectionCommit, tx *sql.Tx, commandID, agentID string, inputJSON []byte, succeeded bool, rawResult json.RawMessage) error {
	var input senulun.EnrollmentTask
	var result struct {
		Enrollment *senulun.EnrollmentResult `json:"pulseEnrollment"`
	}
	if json.Unmarshal(inputJSON, &input) != nil {
		return errors.New("center: invalid Senulun enrollment task")
	}
	if json.Unmarshal(rawResult, &result) != nil || result.Enrollment == nil || result.Enrollment.ID == "" || len(result.Enrollment.Token) < 16 || len(result.Enrollment.Token) > 512 || result.Enrollment.ExpiresAtUnixMS <= s.now().UnixMilli() {
		succeeded = false
	}
	var targetAgentID, applicationID, state string
	if err := tx.QueryRowContext(ctx, `SELECT agent_id, application_id, state FROM deployments WHERE id = ? AND app_key = ? AND operation = 'install'`, input.DeploymentID, senulunAgentAppKey).Scan(&targetAgentID, &applicationID, &state); err != nil {
		return err
	}
	if state != "pending" {
		return errors.New("center: Senulun collector installation is no longer pending")
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
		if err := s.recordTaskEvent(ctx, tx, input.DeploymentID, targetAgentID, "application.apply", 1, "queued", "Senulun collector is ready to install"); err != nil {
			return err
		}
	} else {
		commandState, message = "failed", "Senulun enrollment could not be prepared. Retry installation."
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
	return commit(tx)
}
