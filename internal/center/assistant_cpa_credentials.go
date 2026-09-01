package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const assistantCPARotationPolicyVersion = "rotate-cpa-credential-v1"

type assistantCredentialRotationRequest struct {
	ApplicationID string `json:"applicationId"`
	Target        string `json:"target"`
}

type assistantCredentialRotationPreview struct {
	Request          assistantCredentialRotationRequest `json:"request"`
	Summary          json.RawMessage                    `json:"summary"`
	Targets          json.RawMessage                    `json:"targets"`
	ExpectedRevision string                             `json:"expectedRevision"`
	PolicyVersion    string                             `json:"policyVersion"`
	Risk             string                             `json:"risk"`
	Digest           string                             `json:"digest"`
}

func (s *Store) PreviewAssistantCredentialRotation(ctx context.Context, request assistantCredentialRotationRequest) (assistantCredentialRotationPreview, error) {
	request.ApplicationID = strings.TrimSpace(request.ApplicationID)
	request.Target = strings.TrimSpace(request.Target)
	if request.ApplicationID == "" || request.Target != "management" && request.Target != "client" {
		return assistantCredentialRotationPreview{}, errors.New("center: assistant CPA rotation requires one application and a management or client target")
	}
	var agentID, agentName, appKey, applicationStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT application.node_id, agent.name, application.app_key, application.status FROM applications application JOIN agents agent ON agent.id = application.node_id WHERE application.id = ?`, request.ApplicationID).Scan(&agentID, &agentName, &appKey, &applicationStatus); errors.Is(err, sql.ErrNoRows) {
		return assistantCredentialRotationPreview{}, errors.New("center: assistant CPA application was not found")
	} else if err != nil {
		return assistantCredentialRotationPreview{}, err
	}
	if appKey != cpaAppKey || applicationStatus == "stopped" {
		return assistantCredentialRotationPreview{}, errors.New("center: assistant target is not an active CPA application")
	}
	if _, err := s.currentCPACredentials(ctx, agentID); err != nil {
		return assistantCredentialRotationPreview{}, errors.New("center: current CPA credentials cannot be proven")
	}
	var activeTasks, activeRotations int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE agent_id = ? AND app_key = ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, cpaAppKey).Scan(&activeTasks); err != nil {
		return assistantCredentialRotationPreview{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_credential_rotations WHERE application_id = ? AND state IN ('preparing', 'pending', 'action_required')`, request.ApplicationID).Scan(&activeRotations); err != nil {
		return assistantCredentialRotationPreview{}, err
	}
	if activeTasks != 0 || activeRotations != 0 {
		return assistantCredentialRotationPreview{}, errors.New("center: CPA already has an active change")
	}
	var currentDeploymentID string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM deployments WHERE agent_id = ? AND app_key = ? AND state = 'succeeded' AND operation IN ('install', 'upgrade', 'configure') ORDER BY updated_at DESC, rowid DESC LIMIT 1`, agentID, cpaAppKey).Scan(&currentDeploymentID); err != nil {
		return assistantCredentialRotationPreview{}, fmt.Errorf("center: read current CPA deployment: %w", err)
	}
	keeperID, keeperStatus := "", ""
	if err := s.db.QueryRowContext(ctx, `SELECT id, status FROM applications WHERE node_id = ? AND app_key = 'vastora-official/keeper' AND status <> 'stopped'`, agentID).Scan(&keeperID, &keeperStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return assistantCredentialRotationPreview{}, fmt.Errorf("center: inspect managed Keeper dependency: %w", err)
	}
	revision := assistantDigest(map[string]any{
		"applicationId": request.ApplicationID, "applicationStatus": applicationStatus,
		"agentId": agentID, "deploymentId": currentDeploymentID,
		"keeperApplicationId": keeperID, "keeperStatus": keeperStatus,
		"activeTasks": activeTasks, "activeRotations": activeRotations,
	})
	impact := "Rotates only the CPA client API key. The management key is unchanged."
	if request.Target == "management" {
		impact = "Rotates the CPA management key and then updates the managed Keeper dependency on the same Agent."
	}
	summary := assistantJSON(map[string]any{
		"action": "rotate_cpa_credential", "agentId": agentID, "agentName": agentName,
		"appKey": cpaAppKey, "appName": map[string]string{"en": "CPA", "zh-CN": "CPA"},
		"credentialTarget": request.Target, "impact": impact,
		"dataRetention": "Application data is preserved. The selected old credential becomes invalid after the managed configuration succeeds.",
	})
	targets := []map[string]string{{"kind": "application", "id": request.ApplicationID}, {"kind": "agent", "id": agentID}}
	if request.Target == "management" && keeperID != "" {
		targets = append(targets, map[string]string{"kind": "application", "id": keeperID})
	}
	targetJSON := assistantJSON(targets)
	requestJSON, _ := json.Marshal(request)
	digest := assistantDigest(map[string]any{"request": json.RawMessage(requestJSON), "summary": summary, "targets": targetJSON, "revision": revision, "policy": assistantCPARotationPolicyVersion, "risk": "high"})
	return assistantCredentialRotationPreview{Request: request, Summary: summary, Targets: targetJSON, ExpectedRevision: revision, PolicyVersion: assistantCPARotationPolicyVersion, Risk: "high", Digest: digest}, nil
}

func (s *Store) CreateAssistantCredentialRotationProposal(ctx context.Context, adminID, conversationID, runID string, preview assistantCredentialRotationPreview) (AssistantProposalView, error) {
	current, err := s.PreviewAssistantCredentialRotation(ctx, preview.Request)
	if err != nil {
		return AssistantProposalView{}, err
	}
	if current.Digest != preview.Digest {
		return AssistantProposalView{}, errors.New("center: assistant CPA rotation preview changed before proposal creation")
	}
	id, err := randomToken(18)
	if err != nil {
		return AssistantProposalView{}, err
	}
	requestJSON, _ := json.Marshal(current.Request)
	now, expires := s.now().UTC(), s.now().UTC().Add(assistantProposalTTL)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO change_proposals(id, conversation_id, run_id, admin_id, kind, request_json, summary_json, digest, targets_json, expected_revision, policy_version, risk, status, expires_at, created_at, updated_at)
		VALUES(?, ?, ?, ?, 'rotate_cpa_credential', ?, ?, ?, ?, ?, ?, 'high', 'pending', ?, ?, ?)`, id, conversationID, runID, adminID, requestJSON, current.Summary, current.Digest, current.Targets, current.ExpectedRevision, current.PolicyVersion, expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return AssistantProposalView{}, fmt.Errorf("center: create assistant CPA rotation proposal: %w", err)
	}
	proposal := AssistantProposalView{ID: id, ConversationID: conversationID, RunID: runID, Kind: "rotate_cpa_credential", Summary: current.Summary, Digest: current.Digest, Targets: current.Targets, ExpectedRevision: current.ExpectedRevision, PolicyVersion: current.PolicyVersion, Risk: "high", Status: "pending", ExpiresAt: expires, CreatedAt: now, UpdatedAt: now}
	_ = s.recordAssistantAudit(ctx, adminID, conversationID, runID, "", id, "", "proposal.created", map[string]string{"digest": current.Digest, "risk": current.Risk, "kind": proposal.Kind})
	return proposal, nil
}

func (s *Store) currentAssistantProposalRevision(ctx context.Context, kind string, requestJSON json.RawMessage) (digest, revision, policy string, err error) {
	switch kind {
	case "install_application":
		var request assistantInstallRequest
		if json.Unmarshal(requestJSON, &request) != nil {
			return "", "", "", errors.New("center: assistant install proposal request is invalid")
		}
		preview, previewErr := s.PreviewAssistantInstall(ctx, request)
		if previewErr != nil {
			return "", "", "", previewErr
		}
		return preview.Digest, preview.ExpectedRevision, preview.PolicyVersion, nil
	case "rotate_cpa_credential":
		var request assistantCredentialRotationRequest
		if json.Unmarshal(requestJSON, &request) != nil {
			return "", "", "", errors.New("center: assistant CPA rotation proposal request is invalid")
		}
		preview, previewErr := s.PreviewAssistantCredentialRotation(ctx, request)
		if previewErr != nil {
			return "", "", "", previewErr
		}
		return preview.Digest, preview.ExpectedRevision, preview.PolicyVersion, nil
	default:
		return "", "", "", errors.New("center: unsupported assistant proposal kind")
	}
}
