package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type assistantModelMessage struct {
	Role       string                   `json:"role"`
	Content    string                   `json:"content,omitempty"`
	ToolCallID string                   `json:"tool_call_id,omitempty"`
	ToolCalls  []assistantModelToolCall `json:"tool_calls,omitempty"`
}

type assistantModelToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

type assistantModelResponse struct {
	Choices []struct {
		Message assistantModelMessage `json:"message"`
	} `json:"choices"`
}

type assistantPreviewCache struct {
	installations map[string]assistantInstallPreview
	rotations     map[string]assistantCredentialRotationPreview
}

func assistantTools() []map[string]any {
	tool := func(name, description string, properties map[string]any, required ...string) map[string]any {
		parameters := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
		if len(required) > 0 {
			parameters["required"] = required
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": description, "parameters": parameters}}
	}
	return []map[string]any{
		tool("cluster_overview", "Return sanitized Center, node, application, and task counts.", map[string]any{}),
		tool("list_sites", "List sanitized sites.", map[string]any{}),
		tool("list_nodes", "List sanitized Agents and their health.", map[string]any{}),
		tool("get_node", "Get one sanitized Agent.", map[string]any{"agentId": map[string]any{"type": "string"}}, "agentId"),
		tool("list_catalog_apps", "List verified Catalog applications and configuration schemas; no credentials are returned.", map[string]any{}),
		tool("list_applications", "List installed applications.", map[string]any{}),
		tool("list_actions", "List recent task events.", map[string]any{}),
		tool("explain_failure", "Return a sanitized explanation for one deployment failure.", map[string]any{"deploymentId": map[string]any{"type": "string"}}, "deploymentId"),
		tool("preview_install_application", "Validate and preview one non-secret Catalog application installation. This never creates work.", map[string]any{"agentId": map[string]any{"type": "string"}, "appKey": map[string]any{"type": "string"}, "role": map[string]any{"type": "string"}, "config": map[string]any{"type": "object"}}, "agentId", "appKey", "config"),
		tool("preview_rotate_cpa_credential", "Validate and preview rotation of one CPA management or client credential. This never returns a credential or creates work.", map[string]any{"applicationId": map[string]any{"type": "string"}, "target": map[string]any{"type": "string", "enum": []string{"management", "client"}}}, "applicationId", "target"),
		tool("propose_change", "Create an approval-gated proposal from a preview returned in this run.", map[string]any{"previewDigest": map[string]any{"type": "string"}}, "previewDigest"),
		tool("get_change_status", "Read one proposal status.", map[string]any{"proposalId": map[string]any{"type": "string"}}, "proposalId"),
		tool("cancel_change", "Cancel one still-pending proposal. This cannot cancel or undo execution.", map[string]any{"proposalId": map[string]any{"type": "string"}}, "proposalId"),
	}
}

func (s *Server) startAssistantRun(run AssistantRunView, adminID string) {
	ctx, cancel := context.WithCancel(s.store.backgroundCtx)
	s.assistantRunMu.Lock()
	s.assistantRuns[run.ID] = cancel
	s.assistantRunMu.Unlock()
	s.store.startBackground(func() {
		defer func() {
			s.assistantRunMu.Lock()
			delete(s.assistantRuns, run.ID)
			s.assistantRunMu.Unlock()
		}()
		s.executeAssistantRun(ctx, run, adminID)
	})
}

func (s *Server) executeAssistantRun(ctx context.Context, run AssistantRunView, adminID string) {
	now := s.store.now().UTC().Format(time.RFC3339Nano)
	result, err := s.store.db.ExecContext(ctx, `UPDATE assistant_runs SET status = 'running', last_error = '', updated_at = ? WHERE id = ? AND status = 'queued'`, now, run.ID)
	if err != nil {
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return
	}
	_ = s.store.appendAssistantEvent(ctx, run.ConversationID, run.ID, "run.started", map[string]string{"runId": run.ID})
	provider, err := s.store.assistantProviderCredentials(ctx)
	if err != nil {
		s.failAssistantRun(run, err)
		return
	}
	history, err := s.store.assistantMessages(ctx, run.ConversationID)
	if err != nil {
		s.failAssistantRun(run, err)
		return
	}
	messages := []assistantModelMessage{{Role: "system", Content: "You are the embedded Vastora cluster assistant. Use only the supplied read and proposal tools. Never claim to execute a change. Never ask for, repeat, or expose credentials. For an installation request, inspect the target and Catalog, call preview_install_application, explain the exact impact, then call propose_change with that preview digest. For CPA credential rotation, identify the installed CPA application, call preview_rotate_cpa_credential for exactly one target, explain the impact, then call propose_change. Credential values are generated and applied outside the model and are never returned to you. Every proposal requires the separate trusted approval card. Plain chat text is never approval. Reply in the user's language."}}
	start := 0
	if len(history) > 30 {
		start = len(history) - 30
	}
	for _, message := range history[start:] {
		if message.Role == "user" || message.Role == "assistant" {
			messages = append(messages, assistantModelMessage{Role: message.Role, Content: redactAssistantText(message.Content)})
		}
	}
	previews := assistantPreviewCache{
		installations: make(map[string]assistantInstallPreview),
		rotations:     make(map[string]assistantCredentialRotationPreview),
	}
	for range 8 {
		response, callErr := s.callAssistantModel(ctx, provider, messages)
		if callErr != nil {
			s.failAssistantRun(run, callErr)
			return
		}
		if len(response.ToolCalls) == 0 {
			content := redactAssistantText(strings.TrimSpace(response.Content))
			if content == "" {
				content = "I could not produce a safe answer. Please rephrase the request."
			}
			if err := s.completeAssistantRun(ctx, run, adminID, content); err != nil {
				s.failAssistantRun(run, err)
			}
			return
		}
		messages = append(messages, response)
		for _, call := range response.ToolCalls {
			toolID, _ := randomToken(18)
			arguments := call.Function.Arguments
			if len(arguments) == 0 || len(arguments) > 64<<10 || !json.Valid(arguments) {
				s.failAssistantRun(run, errors.New("center: model returned invalid assistant tool arguments"))
				return
			}
			if assistantArgumentsContainSensitiveData(arguments) {
				s.failAssistantRun(run, errors.New("center: assistant tool arguments contained a forbidden sensitive field"))
				return
			}
			timestamp := s.store.now().UTC().Format(time.RFC3339Nano)
			if _, err := s.store.db.ExecContext(ctx, `INSERT INTO assistant_tool_calls(id, run_id, name, arguments_json, status, created_at, updated_at) VALUES(?, ?, ?, ?, 'running', ?, ?)`, toolID, run.ID, call.Function.Name, arguments, timestamp, timestamp); err != nil {
				s.failAssistantRun(run, err)
				return
			}
			_ = s.store.appendAssistantEvent(ctx, run.ConversationID, run.ID, "tool.started", map[string]string{"toolCallId": toolID, "name": call.Function.Name})
			toolResult, proposal, toolErr := s.executeAssistantTool(ctx, adminID, run, toolID, call.Function.Name, arguments, previews)
			status := "completed"
			if toolErr != nil {
				status, toolResult = "failed", assistantJSON(map[string]string{"error": redactedAssistantError(toolErr)})
			}
			if len(toolResult) > 256<<10 {
				toolResult = assistantJSON(map[string]string{"error": "tool result exceeded the safe limit"})
				status = "failed"
			}
			if _, err := s.store.db.ExecContext(ctx, `UPDATE assistant_tool_calls SET result_json = ?, status = ?, updated_at = ? WHERE id = ? AND status = 'running'`, toolResult, status, s.store.now().UTC().Format(time.RFC3339Nano), toolID); err != nil {
				s.failAssistantRun(run, err)
				return
			}
			_ = s.store.appendAssistantEvent(ctx, run.ConversationID, run.ID, "tool.completed", map[string]string{"toolCallId": toolID, "name": call.Function.Name, "status": status})
			_ = s.store.recordAssistantAudit(ctx, adminID, run.ConversationID, run.ID, toolID, "", "", "tool."+status, map[string]string{"name": call.Function.Name, "argumentsDigest": assistantDigest(json.RawMessage(arguments)), "resultDigest": assistantDigest(json.RawMessage(toolResult))})
			messages = append(messages, assistantModelMessage{Role: "tool", ToolCallID: call.ID, Content: string(toolResult)})
			if proposal != nil {
				content := "A structured change proposal is ready. Review the exact target, version, impact, retention behavior, risk, and expiry in the approval card. Chat text cannot approve it."
				if strings.ContainsAny(history[len(history)-1].Content, "一二三四五六七八九十是否怎么如何安装节点应用") {
					content = "已生成结构化变更提案。请在审批卡中核对目标、版本、影响、数据保留方式、风险和有效期。聊天文字不能完成审批。"
				}
				if err := s.storeAssistantMessage(ctx, run, content); err != nil {
					s.failAssistantRun(run, err)
					return
				}
				_, _ = s.store.db.ExecContext(ctx, `UPDATE assistant_runs SET status = 'approval_required', updated_at = ? WHERE id = ? AND status = 'running'`, s.store.now().UTC().Format(time.RFC3339Nano), run.ID)
				_ = s.store.appendAssistantEvent(ctx, run.ConversationID, run.ID, "proposal.created", proposal)
				_ = s.store.appendAssistantEvent(ctx, run.ConversationID, run.ID, "approval.required", proposal)
				return
			}
		}
	}
	s.failAssistantRun(run, errors.New("center: assistant exceeded the allowed tool rounds"))
}

func (s *Server) callAssistantModel(ctx context.Context, provider assistantProvider, messages []assistantModelMessage) (assistantModelMessage, error) {
	payload, err := json.Marshal(map[string]any{"model": provider.Model, "messages": messages, "tools": assistantTools(), "tool_choice": "auto", "temperature": 0.1})
	if err != nil {
		return assistantModelMessage{}, err
	}
	endpoint, _ := url.JoinPath(provider.APIURL, "chat/completions")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return assistantModelMessage{}, err
	}
	request.Header.Set("Authorization", "Bearer "+provider.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.store.assistantHTTPClient(provider).Do(request)
	if err != nil {
		return assistantModelMessage{}, fmt.Errorf("center: assistant provider request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return assistantModelMessage{}, fmt.Errorf("center: assistant provider returned HTTP %d", response.StatusCode)
	}
	var decoded assistantModelResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&decoded); err != nil || len(decoded.Choices) == 0 {
		return assistantModelMessage{}, errors.New("center: assistant provider returned an invalid response")
	}
	return decoded.Choices[0].Message, nil
}

func (s *Server) executeAssistantTool(ctx context.Context, adminID string, run AssistantRunView, toolID, name string, arguments json.RawMessage, previews assistantPreviewCache) (json.RawMessage, *AssistantProposalView, error) {
	switch name {
	case "cluster_overview":
		agents, err := s.store.ListAgents(ctx)
		if err != nil {
			return nil, nil, err
		}
		applications, err := s.store.ListApplications(ctx)
		if err != nil {
			return nil, nil, err
		}
		actions, _ := s.store.ListActions(ctx, 20)
		return assistantJSON(map[string]any{"agents": len(agents), "connectedAgents": countConnectedAgents(agents), "applications": len(applications), "recentActions": len(actions)}), nil, nil
	case "list_sites":
		values, err := s.store.ListSites(ctx)
		return assistantJSON(map[string]any{"sites": sanitizeAssistantSites(values)}), nil, err
	case "list_nodes":
		values, err := s.store.ListAgents(ctx)
		return assistantJSON(map[string]any{"agents": sanitizeAssistantAgents(values)}), nil, err
	case "get_node":
		var input struct {
			AgentID string `json:"agentId"`
		}
		if json.Unmarshal(arguments, &input) != nil {
			return nil, nil, errors.New("center: invalid get_node arguments")
		}
		values, err := s.store.ListAgents(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, value := range values {
			if value.ID == input.AgentID {
				return assistantJSON(sanitizeAssistantAgents([]AgentView{value})[0]), nil, nil
			}
		}
		return nil, nil, errors.New("center: Agent not found")
	case "list_catalog_apps":
		values, err := s.store.ListApps(ctx)
		return assistantJSON(map[string]any{"apps": values}), nil, err
	case "list_applications":
		values, err := s.store.ListApplications(ctx)
		return assistantJSON(map[string]any{"applications": sanitizeAssistantApplications(values)}), nil, err
	case "list_actions":
		values, err := s.store.ListActions(ctx, 50)
		return assistantJSON(map[string]any{"actions": sanitizeAssistantActions(values)}), nil, err
	case "explain_failure":
		var input struct {
			DeploymentID string `json:"deploymentId"`
		}
		if json.Unmarshal(arguments, &input) != nil {
			return nil, nil, errors.New("center: invalid explain_failure arguments")
		}
		values, err := s.store.ListDeployments(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, value := range values {
			if value.ID == input.DeploymentID {
				return assistantJSON(map[string]any{"deploymentId": value.ID, "state": value.State, "error": redactAssistantText(value.Error), "reconciliationRequired": value.ReconciliationRequired}), nil, nil
			}
		}
		return nil, nil, errors.New("center: deployment not found")
	case "preview_install_application":
		var input assistantInstallRequest
		if json.Unmarshal(arguments, &input) != nil {
			return nil, nil, errors.New("center: invalid preview arguments")
		}
		if len(input.Config) == 0 {
			input.Config = json.RawMessage(`{}`)
		}
		preview, err := s.store.PreviewAssistantInstall(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		previews.installations[preview.Digest] = preview
		return assistantJSON(preview), nil, nil
	case "preview_rotate_cpa_credential":
		var input assistantCredentialRotationRequest
		if json.Unmarshal(arguments, &input) != nil {
			return nil, nil, errors.New("center: invalid CPA credential rotation preview arguments")
		}
		preview, err := s.store.PreviewAssistantCredentialRotation(ctx, input)
		if err != nil {
			return nil, nil, err
		}
		previews.rotations[preview.Digest] = preview
		return assistantJSON(preview), nil, nil
	case "propose_change":
		var input struct {
			PreviewDigest string `json:"previewDigest"`
		}
		if json.Unmarshal(arguments, &input) != nil {
			return nil, nil, errors.New("center: invalid proposal arguments")
		}
		if preview, ok := previews.installations[input.PreviewDigest]; ok {
			proposal, err := s.store.CreateAssistantProposal(ctx, adminID, run.ConversationID, run.ID, preview)
			if err != nil {
				return nil, nil, err
			}
			return assistantJSON(proposal), &proposal, nil
		}
		if preview, ok := previews.rotations[input.PreviewDigest]; ok {
			proposal, err := s.store.CreateAssistantCredentialRotationProposal(ctx, adminID, run.ConversationID, run.ID, preview)
			if err != nil {
				return nil, nil, err
			}
			return assistantJSON(proposal), &proposal, nil
		}
		return nil, nil, errors.New("center: propose_change requires a preview from the current run")
	case "get_change_status":
		var input struct {
			ProposalID string `json:"proposalId"`
		}
		if json.Unmarshal(arguments, &input) != nil {
			return nil, nil, errors.New("center: invalid change status arguments")
		}
		proposal, _, owner, err := s.store.assistantProposalByID(ctx, input.ProposalID)
		if err != nil || owner != adminID {
			return nil, nil, errors.New("center: proposal not found")
		}
		return assistantJSON(proposal), nil, nil
	case "cancel_change":
		var input struct {
			ProposalID string `json:"proposalId"`
		}
		if json.Unmarshal(arguments, &input) != nil {
			return nil, nil, errors.New("center: invalid cancel_change arguments")
		}
		proposal, _, owner, err := s.store.assistantProposalByID(ctx, input.ProposalID)
		if err != nil || owner != adminID || proposal.Status != "pending" {
			return nil, nil, errors.New("center: pending proposal not found")
		}
		result, err := s.store.db.ExecContext(ctx, `UPDATE change_proposals SET status = 'cancelled', updated_at = ? WHERE id = ? AND status = 'pending'`, s.store.now().UTC().Format(time.RFC3339Nano), input.ProposalID)
		if err != nil {
			return nil, nil, errors.New("center: cancel proposal failed")
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return nil, nil, errors.New("center: cancel proposal failed")
		}
		return assistantJSON(map[string]bool{"cancelled": true}), nil, nil
	default:
		return nil, nil, errors.New("center: assistant tool is not allowed")
	}
}

func sanitizeAssistantSites(values []SiteView) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{
			"id": value.ID, "name": value.Name, "code": value.Code, "description": value.Description,
			"timezone": value.Timezone, "gatewayStatus": value.GatewayStatus, "status": value.Status,
		})
	}
	return result
}

func sanitizeAssistantAgents(values []AgentView) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{
			"id": value.ID, "name": value.Name, "version": value.Version, "operatingSystem": value.OperatingSystem,
			"architecture": value.Architecture, "status": value.Status, "connected": value.Connected, "siteId": value.SiteID,
			"roles": value.Roles, "capabilities": value.Capabilities, "gatewayHealthy": value.GatewayHealthy,
		})
	}
	return result
}

func sanitizeAssistantApplications(values []ApplicationView) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{
			"id": value.ID, "name": value.Name, "nodeId": value.NodeID, "siteId": value.SiteID, "appKey": value.AppKey,
			"status": value.Status, "runtime": value.Runtime, "role": value.Role, "nodeSyncStatus": value.NodeSyncStatus,
			"nodeSyncError": redactAssistantText(value.NodeSyncError), "installedVersion": value.InstalledVersion,
			"availableVersion": value.AvailableVersion, "updateAvailable": value.UpdateAvailable,
		})
	}
	return result
}

func sanitizeAssistantActions(values []ActionView) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		result = append(result, map[string]any{
			"id": value.ID, "taskId": value.TaskID, "agentId": value.AgentID, "kind": value.Kind,
			"revision": value.Revision, "event": value.Event, "message": redactAssistantText(value.Message), "createdAt": value.CreatedAt,
		})
	}
	return result
}

func countConnectedAgents(values []AgentView) int {
	count := 0
	for _, value := range values {
		if value.Connected {
			count++
		}
	}
	return count
}

func assistantArgumentsContainSensitiveData(arguments json.RawMessage) bool {
	var value any
	if json.Unmarshal(arguments, &value) != nil {
		return true
	}
	var inspect func(any) bool
	inspect = func(current any) bool {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(key, "_", ""))
				if strings.Contains(normalized, "password") || strings.Contains(normalized, "secret") || strings.Contains(normalized, "token") || strings.Contains(normalized, "apikey") || strings.Contains(normalized, "authorization") {
					return true
				}
				if inspect(child) {
					return true
				}
			}
		case []any:
			for _, child := range typed {
				if inspect(child) {
					return true
				}
			}
		}
		return false
	}
	return inspect(value)
}

func (s *Server) storeAssistantMessage(ctx context.Context, run AssistantRunView, content string) error {
	id, err := randomToken(18)
	if err != nil {
		return err
	}
	now := s.store.now().UTC()
	if _, err := s.store.db.ExecContext(ctx, `INSERT INTO assistant_messages(id, conversation_id, run_id, role, content, created_at) VALUES(?, ?, ?, 'assistant', ?, ?)`, id, run.ConversationID, run.ID, redactAssistantText(content), now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return s.store.appendAssistantEvent(ctx, run.ConversationID, run.ID, "message.delta", map[string]string{"messageId": id, "content": redactAssistantText(content)})
}

func (s *Server) completeAssistantRun(ctx context.Context, run AssistantRunView, adminID, content string) error {
	messageID, err := randomToken(18)
	if err != nil {
		return err
	}
	content = redactAssistantText(content)
	now := s.store.now().UTC()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE assistant_runs SET status = 'completed', last_error = '', updated_at = ? WHERE id = ? AND status = 'running'`, now.Format(time.RFC3339Nano), run.ID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("center: assistant run completion raced with cancellation")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_messages(id, conversation_id, run_id, role, content, created_at) VALUES(?, ?, ?, 'assistant', ?, ?)`, messageID, run.ConversationID, run.ID, content, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := insertAssistantEvent(ctx, tx, run.ConversationID, run.ID, "message.delta", map[string]string{"messageId": messageID, "content": content}, now); err != nil {
		return err
	}
	if err := insertAssistantEvent(ctx, tx, run.ConversationID, run.ID, "run.completed", map[string]string{"runId": run.ID}, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = s.store.recordAssistantAudit(ctx, adminID, run.ConversationID, run.ID, "", "", "", "run.completed", map[string]string{"answerDigest": assistantDigest(content)})
	return nil
}

func (s *Server) failAssistantRun(run AssistantRunView, cause error) {
	message := redactedAssistantError(cause)
	var adminID, status string
	err := s.store.db.QueryRowContext(context.Background(), `SELECT admin_id, status FROM assistant_runs WHERE id = ?`, run.ID).Scan(&adminID, &status)
	if err != nil || status == "cancelled" {
		return
	}
	result, err := s.store.db.ExecContext(context.Background(), `UPDATE assistant_runs SET status = 'failed', last_error = ?, updated_at = ? WHERE id = ? AND status IN ('queued', 'running')`, message, s.store.now().UTC().Format(time.RFC3339Nano), run.ID)
	if err != nil {
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return
	}
	_ = s.store.appendAssistantEvent(context.Background(), run.ConversationID, run.ID, "run.failed", map[string]string{"runId": run.ID, "error": message})
	_ = s.store.recordAssistantAudit(context.Background(), adminID, run.ConversationID, run.ID, "", "", "", "run.failed", map[string]string{"errorDigest": assistantDigest(message)})
}

func (s *Server) cancelAssistantRun(ctx context.Context, adminID, runID string) error {
	var owner, conversationID, status string
	if err := s.store.db.QueryRowContext(ctx, `SELECT admin_id, conversation_id, status FROM assistant_runs WHERE id = ?`, runID).Scan(&owner, &conversationID, &status); err != nil || owner != adminID {
		return errors.New("center: assistant run not found")
	}
	if status == "completed" || status == "failed" || status == "cancelled" || status == "approval_required" {
		return errors.New("center: assistant run is no longer cancellable")
	}
	result, err := s.store.db.ExecContext(ctx, `UPDATE assistant_runs SET status = 'cancelled', updated_at = ? WHERE id = ? AND status IN ('queued', 'running')`, s.store.now().UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("center: assistant run cancellation raced with completion")
	}
	s.assistantRunMu.Lock()
	cancel := s.assistantRuns[runID]
	s.assistantRunMu.Unlock()
	if cancel != nil {
		cancel()
	}
	_ = s.store.appendAssistantEvent(context.Background(), conversationID, runID, "run.cancelled", map[string]string{"runId": runID})
	_ = s.store.recordAssistantAudit(context.Background(), adminID, conversationID, runID, "", "", "", "run.cancelled", map[string]string{})
	return nil
}

func (s *Server) watchAssistantDeployment(proposal AssistantProposalView, execution AssistantExecutionView, adminID string) {
	s.assistantRunMu.Lock()
	if _, exists := s.assistantWatchers[execution.ID]; exists {
		s.assistantRunMu.Unlock()
		return
	}
	s.assistantWatchers[execution.ID] = struct{}{}
	s.assistantRunMu.Unlock()
	s.store.startBackground(func() {
		defer func() {
			s.assistantRunMu.Lock()
			delete(s.assistantWatchers, execution.ID)
			s.assistantRunMu.Unlock()
		}()
		lastState := ""
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			var state, taskError string
			err := s.store.db.QueryRowContext(s.store.backgroundCtx, `SELECT state, error FROM deployments WHERE id = ?`, execution.ID).Scan(&state, &taskError)
			if err != nil {
				return
			}
			if state != lastState {
				event := "execution." + state
				if state == "succeeded" {
					event = "execution.completed"
				} else if state == "failed" {
					event = "execution.failed"
				}
				_ = s.store.appendAssistantEvent(s.store.backgroundCtx, proposal.ConversationID, proposal.RunID, event, map[string]string{"deploymentId": execution.ID, "error": redactAssistantText(taskError)})
				_ = s.store.recordAssistantAudit(s.store.backgroundCtx, adminID, proposal.ConversationID, proposal.RunID, "", proposal.ID, execution.ID, event, map[string]string{"error": redactAssistantText(taskError)})
				lastState = state
			}
			if state == "succeeded" || state == "failed" {
				runState, finalEvent, message := "completed", "run.completed", "The approved application installation completed successfully."
				if state == "failed" {
					runState, finalEvent, message = "failed", "run.failed", "The approved application installation failed: "+redactAssistantText(taskError)
				}
				completed, err := s.finishAssistantExecution(s.store.backgroundCtx, proposal, execution.ID, "deploymentId", runState, finalEvent, message, taskError)
				if err != nil || !completed {
					return
				}
				_ = s.store.recordAssistantAudit(s.store.backgroundCtx, adminID, proposal.ConversationID, proposal.RunID, "", proposal.ID, execution.ID, finalEvent, map[string]string{"answerDigest": assistantDigest(message)})
				return
			}
			select {
			case <-s.store.backgroundCtx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (s *Server) watchAssistantCredentialRotation(proposal AssistantProposalView, execution AssistantExecutionView, adminID string) {
	watchKey := "credential-rotation:" + execution.ID
	s.assistantRunMu.Lock()
	if _, exists := s.assistantWatchers[watchKey]; exists {
		s.assistantRunMu.Unlock()
		return
	}
	s.assistantWatchers[watchKey] = struct{}{}
	s.assistantRunMu.Unlock()
	s.store.startBackground(func() {
		defer func() {
			s.assistantRunMu.Lock()
			delete(s.assistantWatchers, watchKey)
			s.assistantRunMu.Unlock()
		}()
		lastState := ""
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			rotation, err := s.store.refreshCredentialRotation(s.store.backgroundCtx, execution.ID)
			if err != nil {
				return
			}
			if rotation.State != lastState {
				event := "execution." + rotation.State
				if rotation.State == "succeeded" {
					event = "execution.completed"
				} else if rotation.State == "failed" || rotation.State == "action_required" {
					event = "execution.failed"
				}
				payload := map[string]string{"rotationId": execution.ID, "state": rotation.State, "error": redactAssistantText(rotation.LastError)}
				_ = s.store.appendAssistantEvent(s.store.backgroundCtx, proposal.ConversationID, proposal.RunID, event, payload)
				_ = s.store.recordAssistantAudit(s.store.backgroundCtx, adminID, proposal.ConversationID, proposal.RunID, "", proposal.ID, "", event, payload)
				lastState = rotation.State
			}
			if rotation.State == "succeeded" || rotation.State == "failed" || rotation.State == "action_required" {
				runState, finalEvent := "completed", "run.completed"
				message := "The approved CPA credential rotation completed successfully. The secret value was not disclosed to the assistant."
				if rotation.State != "succeeded" {
					runState, finalEvent = "failed", "run.failed"
					message = "The approved CPA credential rotation requires attention: " + redactAssistantText(rotation.LastError)
				}
				completed, finishErr := s.finishAssistantExecution(s.store.backgroundCtx, proposal, execution.ID, "rotationId", runState, finalEvent, message, rotation.LastError)
				if finishErr != nil || !completed {
					return
				}
				_ = s.store.recordAssistantAudit(s.store.backgroundCtx, adminID, proposal.ConversationID, proposal.RunID, "", proposal.ID, "", finalEvent, map[string]string{"answerDigest": assistantDigest(message), "rotationId": execution.ID})
				return
			}
			select {
			case <-s.store.backgroundCtx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (s *Server) finishAssistantExecution(ctx context.Context, proposal AssistantProposalView, executionID, eventField, runState, finalEvent, message, taskError string) (bool, error) {
	messageID, err := randomToken(18)
	if err != nil {
		return false, err
	}
	now := s.store.now().UTC()
	message, taskError = redactAssistantText(message), redactAssistantText(taskError)
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE assistant_runs SET status = ?, last_error = ?, updated_at = ? WHERE id = ? AND status = 'approval_required'`, runState, taskError, now.Format(time.RFC3339Nano), proposal.RunID)
	if err != nil {
		return false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO assistant_messages(id, conversation_id, run_id, role, content, created_at) VALUES(?, ?, ?, 'assistant', ?, ?)`, messageID, proposal.ConversationID, proposal.RunID, message, now.Format(time.RFC3339Nano)); err != nil {
		return false, err
	}
	if err := insertAssistantEvent(ctx, tx, proposal.ConversationID, proposal.RunID, "message.delta", map[string]string{"messageId": messageID, "content": message}, now); err != nil {
		return false, err
	}
	if err := insertAssistantEvent(ctx, tx, proposal.ConversationID, proposal.RunID, finalEvent, map[string]string{"runId": proposal.RunID, eventField: executionID}, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Server) resumeAssistantExecutions() {
	rows, err := s.store.db.QueryContext(s.store.backgroundCtx, `SELECT change_proposals.id, change_proposals.kind, change_proposals.execution_id
		FROM change_proposals
		JOIN assistant_runs ON assistant_runs.id = change_proposals.run_id
		WHERE change_proposals.status = 'applied' AND change_proposals.execution_id <> '' AND assistant_runs.status = 'approval_required'`)
	if err != nil {
		return
	}
	type execution struct{ proposalID, kind, executionID string }
	executions := []execution{}
	for rows.Next() {
		var proposalID, kind, executionID string
		if rows.Scan(&proposalID, &kind, &executionID) != nil {
			continue
		}
		executions = append(executions, execution{proposalID: proposalID, kind: kind, executionID: executionID})
	}
	rows.Close()
	for _, value := range executions {
		proposal, _, adminID, err := s.store.assistantProposalByID(s.store.backgroundCtx, value.proposalID)
		if err == nil {
			executionView := AssistantExecutionView{ID: value.executionID, Kind: value.kind}
			if value.kind == "rotate_cpa_credential" {
				s.watchAssistantCredentialRotation(proposal, executionView, adminID)
			} else {
				s.watchAssistantDeployment(proposal, executionView, adminID)
			}
		}
	}
}
