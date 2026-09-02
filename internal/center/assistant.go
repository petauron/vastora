package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/petauron/vastora/internal/catalog"
)

const (
	assistantPolicyVersion = "install-application-v1"
	assistantProposalTTL   = 15 * time.Minute
)

var assistantCredentialPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:sk|ghp|github_pat|glpat|xox[baprs])[-_][A-Za-z0-9_-]{12,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)https?://[^/\s:@]+:[^@\s/]+@`),
}

var assistantSensitiveAssignmentPattern = regexp.MustCompile(`(?i)(?:api[ _-]?key|access[ _-]?token|refresh[ _-]?token|password|passwd|secret|authorization|bearer|密钥|密码|令牌)\s*(?::|=|：|是|为)\s*[^\s,;，；]{4,}`)

type AssistantConversationView struct {
	ID        string                  `json:"id"`
	Title     string                  `json:"title"`
	Messages  []AssistantMessageView  `json:"messages"`
	Runs      []AssistantRunView      `json:"runs"`
	Proposals []AssistantProposalView `json:"proposals"`
	CreatedAt time.Time               `json:"createdAt"`
	UpdatedAt time.Time               `json:"updatedAt"`
}

type AssistantMessageView struct {
	ID        string    `json:"id"`
	RunID     string    `json:"runId,omitempty"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

type AssistantRunView struct {
	ID             string    `json:"id"`
	ConversationID string    `json:"conversationId"`
	Status         string    `json:"status"`
	LastError      string    `json:"lastError,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type AssistantProposalView struct {
	ID               string          `json:"id"`
	ConversationID   string          `json:"conversationId"`
	RunID            string          `json:"runId"`
	Kind             string          `json:"kind"`
	Summary          json.RawMessage `json:"summary"`
	Digest           string          `json:"digest"`
	Targets          json.RawMessage `json:"targets"`
	ExpectedRevision string          `json:"expectedRevision"`
	PolicyVersion    string          `json:"policyVersion"`
	Risk             string          `json:"risk"`
	Status           string          `json:"status"`
	ExpiresAt        time.Time       `json:"expiresAt"`
	DeploymentID     string          `json:"deploymentId,omitempty"`
	ExecutionID      string          `json:"executionId,omitempty"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

type AssistantExecutionView struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
}

type AssistantEventView struct {
	ID        int64           `json:"id"`
	RunID     string          `json:"runId,omitempty"`
	Event     string          `json:"event"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"createdAt"`
}

type assistantInstallRequest struct {
	AgentID string          `json:"agentId"`
	AppKey  string          `json:"appKey"`
	Role    string          `json:"role,omitempty"`
	Config  json.RawMessage `json:"config"`
}

type assistantInstallPreview struct {
	Request          assistantInstallRequest `json:"request"`
	Summary          json.RawMessage         `json:"summary"`
	Targets          json.RawMessage         `json:"targets"`
	ExpectedRevision string                  `json:"expectedRevision"`
	PolicyVersion    string                  `json:"policyVersion"`
	Risk             string                  `json:"risk"`
	Digest           string                  `json:"digest"`
}

func (s *Store) CreateAssistantConversation(ctx context.Context, adminID, title string) (AssistantConversationView, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New conversation"
	}
	if len(title) > 160 {
		return AssistantConversationView{}, errors.New("center: assistant conversation title is too long")
	}
	id, err := randomToken(18)
	if err != nil {
		return AssistantConversationView{}, err
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `INSERT INTO assistant_conversations(id, admin_id, title, created_at, updated_at) VALUES(?, ?, ?, ?, ?)`, id, adminID, title, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return AssistantConversationView{}, fmt.Errorf("center: create assistant conversation: %w", err)
	}
	_ = s.recordAssistantAudit(ctx, adminID, id, "", "", "", "", "conversation.created", map[string]string{})
	return AssistantConversationView{ID: id, Title: title, Messages: []AssistantMessageView{}, Runs: []AssistantRunView{}, Proposals: []AssistantProposalView{}, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) ListAssistantConversations(ctx context.Context, adminID string) ([]AssistantConversationView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, title, created_at, updated_at FROM assistant_conversations WHERE admin_id = ? ORDER BY updated_at DESC, id DESC`, adminID)
	if err != nil {
		return nil, fmt.Errorf("center: list assistant conversations: %w", err)
	}
	defer rows.Close()
	values := []AssistantConversationView{}
	for rows.Next() {
		var value AssistantConversationView
		var createdAt, updatedAt string
		if err := rows.Scan(&value.ID, &value.Title, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) AssistantConversation(ctx context.Context, adminID, conversationID string) (AssistantConversationView, error) {
	var value AssistantConversationView
	var createdAt, updatedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT id, title, created_at, updated_at FROM assistant_conversations WHERE id = ? AND admin_id = ?`, conversationID, adminID).Scan(&value.ID, &value.Title, &createdAt, &updatedAt); errors.Is(err, sql.ErrNoRows) {
		return AssistantConversationView{}, errors.New("center: assistant conversation not found")
	} else if err != nil {
		return AssistantConversationView{}, err
	}
	value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	value.Messages, _ = s.assistantMessages(ctx, conversationID)
	value.Runs, _ = s.assistantRunsForConversation(ctx, conversationID)
	value.Proposals, _ = s.assistantProposals(ctx, conversationID)
	return value, nil
}

func (s *Store) QueueAssistantMessage(ctx context.Context, adminID, conversationID, content string) (AssistantRunView, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 8000 {
		return AssistantRunView{}, errors.New("center: assistant message must be 1 to 8000 characters")
	}
	if assistantTextContainsPotentialCredential(content) {
		return AssistantRunView{}, errors.New("center: assistant message appears to contain a credential; remove passwords, keys, tokens, and other secrets before sending")
	}
	content = redactAssistantText(content)
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT admin_id FROM assistant_conversations WHERE id = ?`, conversationID).Scan(&owner); errors.Is(err, sql.ErrNoRows) || owner != adminID {
		return AssistantRunView{}, errors.New("center: assistant conversation not found")
	} else if err != nil {
		return AssistantRunView{}, err
	}
	messageID, err := randomToken(18)
	if err != nil {
		return AssistantRunView{}, err
	}
	runID, err := randomToken(18)
	if err != nil {
		return AssistantRunView{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantRunView{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_runs(id, conversation_id, admin_id, status, created_at, updated_at) VALUES(?, ?, ?, 'queued', ?, ?)`, runID, conversationID, adminID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return AssistantRunView{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_messages(id, conversation_id, run_id, role, content, created_at) VALUES(?, ?, ?, 'user', ?, ?)`, messageID, conversationID, runID, content, now.Format(time.RFC3339Nano)); err != nil {
		return AssistantRunView{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assistant_conversations SET updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), conversationID); err != nil {
		return AssistantRunView{}, err
	}
	if err := insertAssistantEvent(ctx, tx, conversationID, runID, "run.queued", map[string]string{"runId": runID}, now); err != nil {
		return AssistantRunView{}, err
	}
	if err := tx.Commit(); err != nil {
		return AssistantRunView{}, err
	}
	_ = s.recordAssistantAudit(ctx, adminID, conversationID, runID, "", "", "", "message.created", map[string]string{"messageId": messageID})
	return AssistantRunView{ID: runID, ConversationID: conversationID, Status: "queued", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) assistantMessages(ctx context.Context, conversationID string) ([]AssistantMessageView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(run_id, ''), role, content, created_at FROM assistant_messages WHERE conversation_id = ? ORDER BY created_at, id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []AssistantMessageView{}
	for rows.Next() {
		var value AssistantMessageView
		var createdAt string
		if err := rows.Scan(&value.ID, &value.RunID, &value.Role, &value.Content, &createdAt); err != nil {
			return nil, err
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) assistantRunsForConversation(ctx context.Context, conversationID string) ([]AssistantRunView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, status, last_error, created_at, updated_at FROM assistant_runs WHERE conversation_id = ? ORDER BY created_at, id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []AssistantRunView{}
	for rows.Next() {
		var value AssistantRunView
		var createdAt, updatedAt string
		value.ConversationID = conversationID
		if err := rows.Scan(&value.ID, &value.Status, &value.LastError, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) AssistantEvents(ctx context.Context, adminID, conversationID string, after int64) ([]AssistantEventView, error) {
	var owner string
	if err := s.db.QueryRowContext(ctx, `SELECT admin_id FROM assistant_conversations WHERE id = ?`, conversationID).Scan(&owner); err != nil || owner != adminID {
		return nil, errors.New("center: assistant conversation not found")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, COALESCE(run_id, ''), event, data_json, created_at FROM assistant_events WHERE conversation_id = ? AND id > ? ORDER BY id LIMIT 200`, conversationID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []AssistantEventView{}
	for rows.Next() {
		var value AssistantEventView
		var createdAt string
		if err := rows.Scan(&value.ID, &value.RunID, &value.Event, &value.Data, &createdAt); err != nil {
			return nil, err
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) PreviewAssistantInstall(ctx context.Context, request assistantInstallRequest) (assistantInstallPreview, error) {
	request.AgentID, request.AppKey, request.Role = strings.TrimSpace(request.AgentID), strings.TrimSpace(request.AppKey), strings.TrimSpace(request.Role)
	if request.AgentID == "" || request.AppKey == "" {
		return assistantInstallPreview{}, errors.New("center: assistant install preview requires one Agent and one application")
	}
	var agentName, agentStatus, agentLastSeen string
	if err := s.db.QueryRowContext(ctx, `SELECT name, status, last_seen_at FROM agents WHERE id = ?`, request.AgentID).Scan(&agentName, &agentStatus, &agentLastSeen); errors.Is(err, sql.ErrNoRows) {
		return assistantInstallPreview{}, errors.New("center: assistant target Agent was not found")
	} else if err != nil {
		return assistantInstallPreview{}, err
	}
	if agentStatus != "active" {
		return assistantInstallPreview{}, errors.New("center: assistant target Agent is not active")
	}
	var serviceAddress string
	if err := s.db.QueryRowContext(ctx, `SELECT service_address FROM agent_network_profiles WHERE agent_id = ?`, request.AgentID).Scan(&serviceAddress); err != nil || strings.TrimSpace(serviceAddress) == "" {
		return assistantInstallPreview{}, errors.New("center: confirm the target Agent private service address before installing an application")
	}
	apps, err := s.ListApps(ctx)
	if err != nil {
		return assistantInstallPreview{}, err
	}
	var app catalog.AppManifest
	found := false
	for _, candidate := range apps {
		if candidate.Key == request.AppKey {
			app, found = candidate.App, true
			break
		}
	}
	if !found {
		return assistantInstallPreview{}, errors.New("center: assistant application was not found in a verified Catalog")
	}
	for _, field := range app.Config {
		if field.Secret {
			return assistantInstallPreview{}, errors.New("center: assistant installation cannot collect secret application fields; use the trusted application form")
		}
	}
	if request.AppKey == threeXUIAppKey {
		if err := s.validateThreeXUIInstallRole(ctx, request.AgentID, request.Role); err != nil {
			return assistantInstallPreview{}, err
		}
	} else if request.Role != "" {
		return assistantInstallPreview{}, errors.New("center: application role is valid only for 3x-ui")
	}
	if _, _, err := normalizeDeploymentConfig(app, request.Config); err != nil {
		return assistantInstallPreview{}, err
	}
	var activeTasks int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE agent_id = ? AND app_key = ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, request.AgentID, request.AppKey).Scan(&activeTasks); err != nil {
		return assistantInstallPreview{}, err
	}
	active, err := s.activeDeployment(ctx, request.AgentID, request.AppKey)
	if err != nil {
		return assistantInstallPreview{}, err
	}
	if activeTasks != 0 || active.Installed {
		return assistantInstallPreview{}, errors.New("center: assistant installation target already has this application or an active task")
	}
	manifestJSON, _ := json.Marshal(app)
	revision := assistantDigest(map[string]any{"agentId": request.AgentID, "agentStatus": agentStatus, "agentLastSeen": agentLastSeen, "serviceAddress": serviceAddress, "manifest": json.RawMessage(manifestJSON), "activeTasks": activeTasks, "installed": active.Installed})
	risk := "low"
	if app.HostAccess || request.AppKey == threeXUIAppKey {
		risk = "medium"
	}
	summary := assistantJSON(map[string]any{"action": "install", "agentId": request.AgentID, "agentName": agentName, "appKey": request.AppKey, "appName": app.Name, "version": app.Version, "role": request.Role, "impact": "Installs one verified Catalog application on one Agent.", "dataRetention": "Application data remains until a separate uninstall explicitly deletes it."})
	targets := assistantJSON([]map[string]string{{"kind": "agent", "id": request.AgentID}, {"kind": "application", "id": request.AppKey}})
	requestJSON, _ := json.Marshal(request)
	digest := assistantDigest(map[string]any{"request": json.RawMessage(requestJSON), "summary": summary, "targets": targets, "revision": revision, "policy": assistantPolicyVersion, "risk": risk})
	return assistantInstallPreview{Request: request, Summary: summary, Targets: targets, ExpectedRevision: revision, PolicyVersion: assistantPolicyVersion, Risk: risk, Digest: digest}, nil
}

func (s *Store) CreateAssistantProposal(ctx context.Context, adminID, conversationID, runID string, preview assistantInstallPreview) (AssistantProposalView, error) {
	current, err := s.PreviewAssistantInstall(ctx, preview.Request)
	if err != nil {
		return AssistantProposalView{}, err
	}
	if current.Digest != preview.Digest {
		return AssistantProposalView{}, errors.New("center: assistant preview changed before proposal creation")
	}
	id, err := randomToken(18)
	if err != nil {
		return AssistantProposalView{}, err
	}
	requestJSON, _ := json.Marshal(current.Request)
	now, expires := s.now().UTC(), s.now().UTC().Add(assistantProposalTTL)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO change_proposals(id, conversation_id, run_id, admin_id, kind, request_json, summary_json, digest, targets_json, expected_revision, policy_version, risk, status, expires_at, created_at, updated_at)
		VALUES(?, ?, ?, ?, 'install_application', ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?)`, id, conversationID, runID, adminID, requestJSON, current.Summary, current.Digest, current.Targets, current.ExpectedRevision, current.PolicyVersion, current.Risk, expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return AssistantProposalView{}, fmt.Errorf("center: create assistant proposal: %w", err)
	}
	proposal := AssistantProposalView{ID: id, ConversationID: conversationID, RunID: runID, Kind: "install_application", Summary: current.Summary, Digest: current.Digest, Targets: current.Targets, ExpectedRevision: current.ExpectedRevision, PolicyVersion: current.PolicyVersion, Risk: current.Risk, Status: "pending", ExpiresAt: expires, CreatedAt: now, UpdatedAt: now}
	_ = s.recordAssistantAudit(ctx, adminID, conversationID, runID, "", id, "", "proposal.created", map[string]string{"digest": current.Digest, "risk": current.Risk})
	return proposal, nil
}

func (s *Store) DecideAssistantProposal(ctx context.Context, adminID, proposalID, decision, digest string) (AssistantProposalView, error) {
	if decision != "approved" && decision != "rejected" {
		return AssistantProposalView{}, errors.New("center: invalid assistant proposal decision")
	}
	s.assistantProposalMu.Lock()
	defer s.assistantProposalMu.Unlock()
	proposal, request, owner, err := s.assistantProposalByID(ctx, proposalID)
	if err != nil || owner != adminID {
		return AssistantProposalView{}, errors.New("center: assistant proposal not found")
	}
	if proposal.Status != "pending" || proposal.Digest != digest {
		return AssistantProposalView{}, errors.New("center: assistant proposal is stale or already decided")
	}
	if !proposal.ExpiresAt.After(s.now()) {
		_, _ = s.db.ExecContext(ctx, `UPDATE change_proposals SET status = 'expired', updated_at = ? WHERE id = ? AND status = 'pending'`, s.now().UTC().Format(time.RFC3339Nano), proposalID)
		return AssistantProposalView{}, errors.New("center: assistant proposal has expired")
	}
	currentDigest, currentRevision, currentPolicy, err := s.currentAssistantProposalRevision(ctx, proposal.Kind, request)
	if err != nil || currentDigest != proposal.Digest || currentRevision != proposal.ExpectedRevision || currentPolicy != proposal.PolicyVersion {
		return AssistantProposalView{}, errors.New("center: assistant proposal target or policy changed; create a new proposal")
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AssistantProposalView{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE change_proposals SET status = ?, updated_at = ? WHERE id = ? AND status = 'pending' AND digest = ?`, decision, now.Format(time.RFC3339Nano), proposalID, digest)
	if err != nil {
		return AssistantProposalView{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return AssistantProposalView{}, errors.New("center: assistant proposal decision raced with another request")
	}
	approvalID, err := randomToken(18)
	if err != nil {
		return AssistantProposalView{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO change_approvals(id, proposal_id, admin_id, decision, digest, created_at) VALUES(?, ?, ?, ?, ?, ?)`, approvalID, proposalID, adminID, decision, digest, now.Format(time.RFC3339Nano)); err != nil {
		return AssistantProposalView{}, err
	}
	if err := insertAssistantEvent(ctx, tx, proposal.ConversationID, proposal.RunID, "proposal."+decision, map[string]string{"proposalId": proposalID}, now); err != nil {
		return AssistantProposalView{}, err
	}
	if err := tx.Commit(); err != nil {
		return AssistantProposalView{}, err
	}
	_ = s.recordAssistantAudit(ctx, adminID, proposal.ConversationID, proposal.RunID, "", proposalID, "", "proposal."+decision, map[string]string{"digest": digest})
	proposal.Status, proposal.UpdatedAt = decision, now
	return proposal, nil
}

func (s *Store) ApplyAssistantProposal(ctx context.Context, adminID, proposalID, digest string) (AssistantExecutionView, error) {
	s.assistantProposalMu.Lock()
	defer s.assistantProposalMu.Unlock()
	proposal, request, owner, err := s.assistantProposalByID(ctx, proposalID)
	if err != nil || owner != adminID {
		return AssistantExecutionView{}, errors.New("center: assistant proposal not found")
	}
	if proposal.Status == "applied" && proposal.ExecutionID != "" {
		if proposal.Kind == "install_application" {
			deployment, replayErr := s.CreateDeployment(ctx, DeploymentRequest{ChangeProposalID: proposalID})
			return AssistantExecutionView{ID: deployment.ID, Kind: proposal.Kind, State: deployment.State}, replayErr
		}
		rotation, replayErr := s.refreshCredentialRotation(ctx, proposal.ExecutionID)
		return AssistantExecutionView{ID: rotation.ID, Kind: proposal.Kind, State: rotation.State}, replayErr
	}
	if proposal.Status != "approved" || proposal.Digest != digest || !proposal.ExpiresAt.After(s.now()) {
		return AssistantExecutionView{}, errors.New("center: assistant proposal is not an approved current grant")
	}
	var approvedDigest, approver string
	if err := s.db.QueryRowContext(ctx, `SELECT digest, admin_id FROM change_approvals WHERE proposal_id = ? AND decision = 'approved'`, proposalID).Scan(&approvedDigest, &approver); err != nil || approvedDigest != digest || approver != adminID {
		return AssistantExecutionView{}, errors.New("center: assistant approval grant is invalid")
	}
	currentDigest, currentRevision, currentPolicy, err := s.currentAssistantProposalRevision(ctx, proposal.Kind, request)
	if err != nil || currentDigest != digest || currentRevision != proposal.ExpectedRevision || currentPolicy != proposal.PolicyVersion {
		return AssistantExecutionView{}, errors.New("center: assistant proposal became stale before execution")
	}
	execution := AssistantExecutionView{Kind: proposal.Kind}
	linkedDeploymentID := ""
	switch proposal.Kind {
	case "install_application":
		var install assistantInstallRequest
		if json.Unmarshal(request, &install) != nil {
			return AssistantExecutionView{}, errors.New("center: assistant install proposal request is invalid")
		}
		deployment, createErr := s.CreateDeployment(ctx, DeploymentRequest{AgentID: install.AgentID, AppKey: install.AppKey, Role: install.Role, Config: install.Config, Operation: "install", ChangeProposalID: proposalID, SecretOperationOwner: "assistant:" + adminID, SecretOperationKey: proposalID})
		if createErr != nil {
			return AssistantExecutionView{}, createErr
		}
		execution.ID, execution.State, linkedDeploymentID = deployment.ID, deployment.State, deployment.ID
	case "rotate_cpa_credential":
		var rotationRequest assistantCredentialRotationRequest
		if json.Unmarshal(request, &rotationRequest) != nil {
			return AssistantExecutionView{}, errors.New("center: assistant CPA rotation proposal request is invalid")
		}
		rotation, rotateErr := s.RotateApplicationCredentialsFromApprovedProposal(ctx, rotationRequest.ApplicationID, adminID, proposalID, rotationRequest.Target)
		if rotateErr != nil {
			return AssistantExecutionView{}, rotateErr
		}
		execution.ID, execution.State = rotation.ID, rotation.State
	default:
		return AssistantExecutionView{}, errors.New("center: unsupported assistant proposal kind")
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(ctx, `UPDATE change_proposals SET status = 'applied', deployment_id = ?, execution_id = ?, updated_at = ? WHERE id = ? AND status IN ('approved', 'applied')`, nullableString(linkedDeploymentID), execution.ID, now.Format(time.RFC3339Nano), proposalID); err != nil {
		return AssistantExecutionView{}, fmt.Errorf("center: link assistant execution: %w", err)
	}
	_ = s.appendAssistantEvent(ctx, proposal.ConversationID, proposal.RunID, "execution.queued", map[string]string{"proposalId": proposalID, "executionId": execution.ID, "kind": execution.Kind})
	_ = s.recordAssistantAudit(ctx, adminID, proposal.ConversationID, proposal.RunID, "", proposalID, linkedDeploymentID, "proposal.applied", map[string]string{"digest": digest, "executionId": execution.ID, "kind": execution.Kind})
	return execution, nil
}

func (s *Store) assistantProposalByID(ctx context.Context, proposalID string) (AssistantProposalView, json.RawMessage, string, error) {
	var proposal AssistantProposalView
	var requestJSON json.RawMessage
	var owner, expiresAt, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT conversation_id, run_id, admin_id, kind, request_json, summary_json, digest, targets_json, expected_revision, policy_version, risk, status, expires_at, COALESCE(deployment_id, ''), execution_id, created_at, updated_at FROM change_proposals WHERE id = ?`, proposalID).Scan(&proposal.ConversationID, &proposal.RunID, &owner, &proposal.Kind, &requestJSON, &proposal.Summary, &proposal.Digest, &proposal.Targets, &proposal.ExpectedRevision, &proposal.PolicyVersion, &proposal.Risk, &proposal.Status, &expiresAt, &proposal.DeploymentID, &proposal.ExecutionID, &createdAt, &updatedAt)
	if err != nil {
		return proposal, requestJSON, owner, err
	}
	proposal.ID = proposalID
	proposal.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expiresAt)
	proposal.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	proposal.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	if !json.Valid(requestJSON) {
		return proposal, requestJSON, owner, errors.New("center: assistant proposal request is invalid")
	}
	return proposal, requestJSON, owner, nil
}

func (s *Store) assistantProposals(ctx context.Context, conversationID string) ([]AssistantProposalView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM change_proposals WHERE conversation_id = ? ORDER BY created_at, id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	values := make([]AssistantProposalView, 0, len(ids))
	for _, id := range ids {
		proposal, _, _, err := s.assistantProposalByID(ctx, id)
		if err != nil {
			return nil, err
		}
		values = append(values, proposal)
	}
	return values, nil
}

func (s *Store) appendAssistantEvent(ctx context.Context, conversationID, runID, event string, data any) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertAssistantEvent(ctx, tx, conversationID, runID, event, data, s.now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func insertAssistantEvent(ctx context.Context, tx *sql.Tx, conversationID, runID, event string, data any, now time.Time) error {
	encoded, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO assistant_events(conversation_id, run_id, event, data_json, created_at) VALUES(?, ?, ?, ?, ?)`, conversationID, nullableString(runID), event, encoded, now.Format(time.RFC3339Nano))
	return err
}

func (s *Store) recordAssistantAudit(ctx context.Context, adminID, conversationID, runID, toolCallID, proposalID, deploymentID, kind string, payload any) error {
	id, err := randomToken(18)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO assistant_audit_events(id, admin_id, conversation_id, run_id, tool_call_id, proposal_id, deployment_id, kind, payload_json, created_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, adminID, nullableString(conversationID), nullableString(runID), nullableString(toolCallID), nullableString(proposalID), nullableString(deploymentID), kind, encoded, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

func assistantDigest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func redactAssistantText(value string) string {
	for _, pattern := range assistantCredentialPatterns {
		value = pattern.ReplaceAllString(value, "[redacted credential]")
	}
	value = assistantSensitiveAssignmentPattern.ReplaceAllString(value, "[redacted sensitive input]")
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		lower := strings.ToLower(line)
		for _, marker := range []string{"api_key", "apikey", "password", "bearer ", "authorization:", "secret="} {
			if strings.Contains(lower, marker) {
				lines[index] = "[redacted sensitive input]"
				break
			}
		}
	}
	return redactAssistantOpaqueCredentials(strings.Join(lines, "\n"))
}

func assistantTextContainsPotentialCredential(value string) bool {
	for _, pattern := range assistantCredentialPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	if assistantSensitiveAssignmentPattern.MatchString(value) {
		return true
	}
	found := false
	forEachAssistantOpaqueCandidate(value, func(string, int, int) bool {
		found = true
		return false
	})
	return found
}

func redactAssistantOpaqueCredentials(value string) string {
	var replacements [][2]int
	forEachAssistantOpaqueCandidate(value, func(_ string, start, end int) bool {
		replacements = append(replacements, [2]int{start, end})
		return true
	})
	for index := len(replacements) - 1; index >= 0; index-- {
		replacement := replacements[index]
		value = value[:replacement[0]] + "[redacted credential]" + value[replacement[1]:]
	}
	return value
}

func forEachAssistantOpaqueCandidate(value string, visit func(string, int, int) bool) {
	for start := 0; start < len(value); {
		if !assistantOpaqueCharacter(value[start]) {
			start++
			continue
		}
		end := start + 1
		for end < len(value) && assistantOpaqueCharacter(value[end]) {
			end++
		}
		candidate := value[start:end]
		adjacentToDomain := start > 0 && value[start-1] == '.' || end < len(value) && value[end] == '.'
		if !adjacentToDomain && assistantOpaqueCredential(candidate) && !visit(candidate, start, end) {
			return
		}
		start = end
	}
}

func assistantOpaqueCharacter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("_+/=-", rune(value))
}

func assistantOpaqueCredential(value string) bool {
	if len(value) < 32 || assistantHexValue(value) || assistantUUIDValue(value) {
		return false
	}
	var categories [4]bool
	counts := map[rune]int{}
	for _, character := range value {
		switch {
		case unicode.IsLower(character):
			categories[0] = true
		case unicode.IsUpper(character):
			categories[1] = true
		case unicode.IsDigit(character):
			categories[2] = true
		default:
			categories[3] = true
		}
		counts[character]++
	}
	categoryCount := 0
	for _, present := range categories {
		if present {
			categoryCount++
		}
	}
	if categoryCount < 2 {
		return false
	}
	entropy := 0.0
	for _, count := range counts {
		probability := float64(count) / float64(len(value))
		entropy -= probability * math.Log2(probability)
	}
	return entropy >= 4.3
}

func assistantHexValue(value string) bool {
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}

func assistantUUIDValue(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdefABCDEF", character) {
			return false
		}
	}
	return true
}
