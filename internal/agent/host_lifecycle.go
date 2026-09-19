package agent

import (
	"context"

	"errors"

	"net/url"

	"strings"

	"github.com/petauron/vastora/internal/controlplane"
)

// BeginHostDecommission transfers responsibility for a claimed cleanup from
// the short Agent task lease to the persistent host helper.
func (c Client) BeginHostDecommission(ctx context.Context, connection Connection, taskID string, attempt int64, executionID, sessionID string) error {
	if strings.TrimSpace(taskID) == "" || attempt <= 0 || strings.TrimSpace(connection.AgentID) == "" || strings.TrimSpace(connection.Credential) == "" || executionID == "" || sessionID == "" {
		return errors.New("agent: invalid host decommission handoff")
	}
	payload := map[string]any{"taskId": taskID, "attempt": attempt, "executionId": executionID, "sessionId": sessionID}
	var response struct {
		Started bool `json:"started"`
	}
	if err := c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/decommission/start", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &response); err != nil {
		return err
	}
	if !response.Started {
		return errors.New("agent: Center did not acknowledge host cleanup handoff")
	}
	return nil
}

// CompleteHostDecommission reports successful local cleanup through the
// task-bound public callback after the Agent's private network is gone.
func (c Client) CompleteHostDecommission(ctx context.Context, callbackURL, callbackToken, taskID string, attempt int64) error {
	callbackURL, err := normalizeHostDecommissionCallbackURL(callbackURL, taskID)
	if err != nil || attempt <= 0 || strings.TrimSpace(callbackToken) == "" {
		return errors.New("agent: invalid host decommission completion")
	}
	payload := map[string]any{"attempt": attempt}
	var response struct {
		Completed bool `json:"completed"`
	}
	if err := c.post(ctx, callbackURL, payload, callbackToken, "", "", &response); err != nil {
		return err
	}
	if !response.Completed {
		return errors.New("agent: Center did not acknowledge host cleanup completion")
	}
	return nil
}

// AuthorizeHostDecommissionStep consumes one ordered cleanup permission.
func (c Client) AuthorizeHostDecommissionStep(ctx context.Context, callbackURL, callbackToken, taskID string, attempt, sequence int64, phase string) error {
	callbackURL, err := normalizeHostDecommissionCallbackURL(callbackURL, taskID)
	if err != nil || attempt <= 0 || sequence <= 0 || callbackToken == "" {
		return errors.New("agent: invalid cleanup step")
	}
	var response struct {
		Recorded bool `json:"recorded"`
	}
	if err := c.post(ctx, callbackURL, map[string]any{"action": "step", "attempt": attempt, "sequence": sequence, "phase": phase}, callbackToken, "", "", &response); err != nil {
		return err
	}
	if !response.Recorded {
		return errors.New("agent: Center did not authorize cleanup step")
	}
	return nil
}

// ReportHostDecommissionFailure only records evidence; it never retries cleanup.
func (c Client) ReportHostDecommissionFailure(ctx context.Context, callbackURL, callbackToken, taskID string, attempt int64, cleanupErr error) error {
	callbackURL, err := normalizeHostDecommissionCallbackURL(callbackURL, taskID)
	if err != nil || attempt <= 0 || strings.TrimSpace(callbackToken) == "" || cleanupErr == nil {
		return errors.New("agent: invalid host decommission failure")
	}
	var response struct {
		Recorded bool `json:"recorded"`
	}
	if err := c.post(ctx, callbackURL, map[string]any{"attempt": attempt, "error": cleanupErr.Error()}, callbackToken, "", "", &response); err != nil {
		return err
	}
	if !response.Recorded {
		return errors.New("agent: Center did not acknowledge host cleanup failure")
	}
	return nil
}

func normalizeHostDecommissionCallbackURL(raw, taskID string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	callbackURL, err := normalizeCenterURL(raw)
	if err != nil || taskID == "" {
		return "", errors.New("agent: invalid host decommission callback URL")
	}
	parsed, err := url.Parse(callbackURL)
	if err != nil || parsed.Path != "/api/v1/agent-decommission-results/"+taskID {
		return "", errors.New("agent: invalid host decommission callback URL")
	}
	return callbackURL, nil
}

// BeginHostUpdate transfers a claimed update from the Agent lease to the
// persistent systemd helper before the Agent process is restarted.
func (c Client) CheckHostUpdateStep(ctx context.Context, connection Connection, executionID, sessionID, phase string) error {
	input := controlplane.ExecutionTransitionRequest{SessionID: sessionID, Action: "helper-step", Phase: phase}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/executions/"+url.PathEscape(executionID), input, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}

func (c Client) HostUpdateObserved(ctx context.Context, connection Connection, executionID, sessionID string) (bool, error) {
	input := controlplane.ExecutionTransitionRequest{SessionID: sessionID, Action: "helper-observe"}
	var result struct {
		Ready bool `json:"ready"`
	}
	err := c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/executions/"+url.PathEscape(executionID), input, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &result)
	return result.Ready, err
}

func (c Client) BeginHostUpdate(ctx context.Context, connection Connection, taskID string, attempt int64, executionID, sessionID string) error {
	if strings.TrimSpace(taskID) == "" || attempt <= 0 || strings.TrimSpace(connection.AgentID) == "" || strings.TrimSpace(connection.Credential) == "" {
		return errors.New("agent: invalid host update handoff")
	}
	payload := map[string]any{"attempt": attempt, "executionId": executionID, "sessionId": sessionID}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/updates/"+url.PathEscape(taskID)+"/start", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}

// CompleteHostUpdate reports the persistent helper outcome. Center accepts a
// success only after the target Agent version has reconnected by heartbeat.
func (c Client) CompleteHostUpdate(ctx context.Context, connection Connection, taskID string, attempt int64, updateErr error, recoveryRequired bool, executionID, sessionID string) error {
	if strings.TrimSpace(taskID) == "" || attempt <= 0 || strings.TrimSpace(connection.AgentID) == "" || strings.TrimSpace(connection.Credential) == "" {
		return errors.New("agent: invalid host update completion")
	}
	if recoveryRequired && updateErr == nil {
		return errors.New("agent: host update recovery requires an error")
	}
	payload := map[string]any{"executionId": executionID, "sessionId": sessionID, "attempt": attempt, "succeeded": updateErr == nil, "error": safeTaskError(updateErr), "result": ApplicationTaskResult{}, "reconciliationRequired": recoveryRequired}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/"+url.PathEscape(taskID)+"/result", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}
