package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/pulse"
	"github.com/petauron/vastora/internal/secret"
)

// Registration evidence identifies records to inspect through Pulse's supported
// administration interface. It does not prove consumption or identify a monitor
// node by itself. In particular, an installation may have reused local credentials.
type AgentReinstallMonitoring struct {
	ApplicationID        string                            `json:"applicationId"`
	ServiceApplicationID string                            `json:"serviceApplicationId"`
	ServiceAgentID       string                            `json:"serviceAgentId"`
	State                string                            `json:"state"`
	Enrollments          []AgentReinstallMonitorEnrollment `json:"enrollments"`
	Inspection           *AgentReinstallMonitorInspection  `json:"inspection,omitempty"`
}

type AgentReinstallMonitorEnrollment struct {
	EnrollmentID string `json:"enrollmentId"`
	ExecutionID  string `json:"executionId"`
}

const reinstallMonitorEvidenceLimit = 64

func (s *Store) readReinstallMonitoring(ctx context.Context, tx *sql.Tx, plan *AgentReinstallPlan) (string, error) {
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	plan.Monitoring = []AgentReinstallMonitoring{}
	op, err := readAgentReinstallOperation(ctx, tx, plan.AgentID)
	if err != nil {
		return "", err
	}
	for _, app := range plan.Applications {
		if app.AppKey != pulseAgentAppKey || app.Recovery == "keep_stopped" || app.DeploymentID == "" {
			continue
		}
		review, evidence, err := s.reinstallMonitorEvidence(ctx, tx, plan.AgentID, app)
		if err != nil {
			return "", err
		}
		if op != nil {
			review.Inspection, err = readReinstallInspection(ctx, tx, op.ID, app.ApplicationID)
			if err != nil {
				return "", err
			}
			if review.Inspection != nil {
				if _, err := s.validateReinstallInspection(ctx, tx, review.ServiceAgentID, review.Inspection.CommandID); err != nil {
					review.Inspection.State = "stale"
					review.Inspection.NodeID = ""
					review.Inspection.Error = "Reviewed monitoring evidence changed; inspect it again"
				}
			}
		}
		plan.Monitoring = append(plan.Monitoring, review)
		if err = encoder.Encode(evidence); err != nil {
			return "", err
		}
	}
	if len(plan.Monitoring) != 0 {
		plan.Requirements = append(plan.Requirements, "verify_original_monitor_identity_before_restore")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (s *Store) reinstallMonitorEvidence(ctx context.Context, tx *sql.Tx, agentID string, app AgentReinstallApplication) (AgentReinstallMonitoring, string, error) {
	review := AgentReinstallMonitoring{ApplicationID: app.ApplicationID, State: "evidence_missing", Enrollments: []AgentReinstallMonitorEnrollment{}}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT config_json FROM deployments WHERE id=? AND application_id=? AND agent_id=?`, app.DeploymentID, app.ApplicationID, agentID).Scan(&raw); err != nil {
		return review, "", err
	}
	var config pulse.AgentConfig
	if json.Unmarshal(raw, &config) != nil || config.Validate() != nil {
		review.State = "configuration_invalid"
		return review, "", nil
	}
	review.ServiceApplicationID = config.ServiceApplicationID
	err := tx.QueryRowContext(ctx, `SELECT node_id FROM applications WHERE id=? AND app_key=?`, config.ServiceApplicationID, pulseAppKey).Scan(&review.ServiceAgentID)
	if errors.Is(err, sql.ErrNoRows) {
		review.State = "service_missing"
		return review, "", nil
	}
	if err != nil {
		return review, "", err
	}
	// A later upgrade/configure is not a new monitor enrollment. Read registrations
	// across the same collector application's history, including retained failed or
	// unknown attempts. A result is useful only after its exact sealed task matches.
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.agent_id,c.application_id,c.input_json,d.id,
	 COALESCE(e.id,''),COALESCE(e.attempt,0),COALESCE(e.digest,''),COALESCE(e.sealed_task,X''),COALESCE(e.sealed_result,X'')
	 FROM application_commands c JOIN deployments d ON d.id=json_extract(c.input_json,'$.deploymentId')
	 LEFT JOIN task_executions e ON e.task_id=c.id AND e.agent_id=c.agent_id AND e.kind='application.command'
	 WHERE c.kind=? AND c.gateway_node_id=? AND d.agent_id=? AND d.application_id=? AND d.app_key=? AND d.operation='install'
	 ORDER BY c.id,e.id LIMIT ?`, pulse.EnrollmentKind, agentID, agentID, app.ApplicationID, pulseAgentAppKey, reinstallMonitorEvidenceLimit+1)
	if err != nil {
		return review, "", err
	}
	defer rows.Close()
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	count, invalid := 0, false
	for rows.Next() {
		var commandID, serviceAgentID, serviceApplicationID, deploymentID, executionID, taskDigest string
		var attempt int64
		var input, sealedTask, sealedResult []byte
		if err := rows.Scan(&commandID, &serviceAgentID, &serviceApplicationID, &input, &deploymentID, &executionID, &attempt, &taskDigest, &sealedTask, &sealedResult); err != nil {
			return review, "", err
		}
		if err := encoder.Encode([]any{commandID, serviceAgentID, serviceApplicationID, input, deploymentID, executionID, attempt, taskDigest, sealedTask, sealedResult}); err != nil {
			return review, "", err
		}
		count++
		if count > reinstallMonitorEvidenceLimit {
			review.State = "evidence_limit"
			break
		}
		enrollmentID := s.reinstallMonitorEnrollmentID(executionID, commandID, deploymentID, serviceApplicationID, attempt, taskDigest, input, sealedTask, sealedResult)
		if serviceApplicationID != review.ServiceApplicationID || serviceAgentID != review.ServiceAgentID || enrollmentID == "" {
			invalid = true
			continue
		}
		review.Enrollments = append(review.Enrollments, AgentReinstallMonitorEnrollment{EnrollmentID: enrollmentID, ExecutionID: executionID})
	}
	if err := rows.Err(); err != nil {
		return review, "", err
	}
	switch {
	case count > reinstallMonitorEvidenceLimit:
	case invalid:
		review.State = "evidence_invalid"
	case len(review.Enrollments) > 0:
		review.State = "inspection_required"
	}
	if review.State != "inspection_required" {
		// Partial evidence is never permission to select the remaining candidate.
		review.Enrollments = []AgentReinstallMonitorEnrollment{}
	}
	return review, hex.EncodeToString(digest.Sum(nil)), nil
}

func (s *Store) reinstallMonitorEnrollmentID(executionID, commandID, deploymentID, serviceApplicationID string, attempt int64, digest string, input, sealedTask, sealedResult []byte) string {
	raw, err := secret.Open(s.key, sealedTask, []byte("execution-task:"+executionID))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	var task AgentTask
	var expected pulse.EnrollmentTask
	if hex.EncodeToString(sum[:]) != digest || json.Unmarshal(input, &expected) != nil || expected.ApplicationID != serviceApplicationID || expected.DeploymentID != deploymentID ||
		json.Unmarshal(raw, &task) != nil || task.ID != commandID || task.Kind != "application.command" || task.Attempt != attempt || attempt < 1 || task.PulseEnrollment == nil || *task.PulseEnrollment != expected {
		return ""
	}
	raw, err = secret.Open(s.key, sealedResult, []byte("execution-result:"+executionID))
	if err != nil {
		return ""
	}
	var evidence executionResultEvidence
	var result struct {
		Enrollment *pulse.EnrollmentResult `json:"pulseEnrollment"`
	}
	if json.Unmarshal(raw, &evidence) != nil || !evidence.Succeeded || evidence.Unknown || json.Unmarshal(evidence.Result, &result) != nil || result.Enrollment == nil {
		return ""
	}
	enrollment := result.Enrollment
	// Expiry affects use of a token, not inspection of its persisted association.
	if enrollment.ID == "" || len(enrollment.ID) > 128 || len(enrollment.Token) < 16 || len(enrollment.Token) > 512 || enrollment.ExpiresAtUnixMS <= 0 {
		return ""
	}
	return enrollment.ID
}
