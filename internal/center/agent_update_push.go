package center

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

type AgentUpdateTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	State    string `json:"state"`
	Eligible bool   `json:"eligible"`
}

type AgentUpdatesStatus struct {
	TargetVersion string                   `json:"targetVersion"`
	Available     bool                     `json:"available"`
	PushPaused    bool                     `json:"pushPaused"`
	Rollout       AgentUpdateRolloutStatus `json:"rollout"`
	Targets       []AgentUpdateTarget      `json:"targets"`
}

type AgentUpdatePushInput struct {
	TargetVersion string   `json:"targetVersion"`
	Mode          string   `json:"mode"`
	AgentIDs      []string `json:"agentIds,omitempty"`
}

type AgentUpdatePushResult struct {
	QueuedAgentIDs  []string           `json:"queuedAgentIds"`
	SkippedAgentIDs []string           `json:"skippedAgentIds"`
	Error           string             `json:"error,omitempty"`
	Status          AgentUpdatesStatus `json:"status"`
}

func (s *Server) agentUpdatesStatus(ctx context.Context) (AgentUpdatesStatus, error) {
	status := AgentUpdatesStatus{TargetVersion: strings.TrimPrefix(Version, "v"), Available: s.agentUpdateRolloutAvailable(), Targets: []AgentUpdateTarget{}}
	var err error
	status.Rollout, err = s.store.AgentUpdateRolloutStatus(ctx, Version)
	if err != nil {
		return status, err
	}
	status.PushPaused, err = agentUpdateRolloutPaused(ctx, s.store.db)
	if err != nil {
		return status, err
	}
	candidates, err := s.store.agentUpdateCandidates(ctx, Version)
	if err != nil {
		return status, err
	}
	eligible := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		eligible[candidate.id] = true
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT a.id,a.name,a.version,a.last_seen_at,a.remote_update_supported,a.credential_revoked_at,
 COALESCE(u.state,''), COALESCE(u.target_version,'') FROM agents a
 LEFT JOIN agent_updates u ON u.id=(SELECT id FROM agent_updates WHERE agent_id=a.id ORDER BY created_at DESC,rowid DESC LIMIT 1)
 WHERE a.status='active' ORDER BY a.name,a.id`)
	if err != nil {
		return status, err
	}
	defer rows.Close()
	for rows.Next() {
		var target AgentUpdateTarget
		var lastSeen, revoked, updateState, updateTarget string
		var supported bool
		if err := rows.Scan(&target.ID, &target.Name, &target.Version, &lastSeen, &supported, &revoked, &updateState, &updateTarget); err != nil {
			return status, err
		}
		seen, _ := time.Parse(time.RFC3339Nano, lastSeen)
		target.Eligible = status.Available && eligible[target.ID]
		switch {
		case !s.store.agentUpdateAllowed(target.ID):
			target.State = "paused"
		case revoked != "":
			target.State = "disabled"
		case target.Eligible:
			target.State = "available"
		case strings.TrimPrefix(target.Version, "v") == status.TargetVersion:
			target.State = "current"
		case semver.IsValid("v"+target.Version) && semver.Compare("v"+target.Version, "v"+status.TargetVersion) > 0:
			target.State = "newer"
		case !seen.After(s.store.now().Add(-agentConnectedMaxAge)):
			target.State = "offline"
		case updateTarget == status.TargetVersion && updateState == "pending":
			target.State = "pending"
		case updateState == "running" || updateState == "installing":
			target.State = "updating"
		case updateState == "failed":
			target.State = "failed"
		case !supported:
			target.State = "manual"
		default:
			target.State = "blocked"
		}
		status.Targets = append(status.Targets, target)
	}
	return status, rows.Err()
}

func (s *Server) handleAgentUpdateStatus(writer http.ResponseWriter, request *http.Request) {
	status, err := s.agentUpdatesStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, status)
}

func (s *Server) handlePushAgentUpdates(writer http.ResponseWriter, request *http.Request) {
	var input AgentUpdatePushInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if input.TargetVersion != strings.TrimPrefix(Version, "v") {
		writeError(writer, http.StatusConflict, errors.New("center: Center version changed; refresh Agent update targets before pushing"))
		return
	}
	if (input.Mode != "all" && input.Mode != "selected") || (input.Mode == "all" && len(input.AgentIDs) > 0) || (input.Mode == "selected" && len(input.AgentIDs) == 0) {
		writeError(writer, http.StatusBadRequest, errors.New("center: choose all eligible Agents or an explicit nonempty selection"))
		return
	}
	before, err := s.agentUpdatesStatus(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if !before.Available || before.PushPaused {
		writeError(writer, http.StatusConflict, errors.New("center: Agent update pushes are currently unavailable or paused"))
		return
	}
	requested := make([]string, 0)
	known := make(map[string]bool, len(before.Targets))
	for _, target := range before.Targets {
		known[target.ID] = true
		if input.Mode == "all" && target.Eligible {
			requested = append(requested, target.ID)
		}
	}
	if input.Mode == "selected" {
		selected := make(map[string]bool, len(input.AgentIDs))
		for _, id := range input.AgentIDs {
			if !known[id] || selected[id] {
				writeError(writer, http.StatusBadRequest, errors.New("center: invalid or duplicate selected Agent"))
				return
			}
			selected[id] = true
		}
		requested = input.AgentIDs
	}
	// Freeze this request's targets, including all mode. Nodes becoming eligible
	// later are not silently added by a background rollout or after a restart.
	result := AgentUpdatePushResult{QueuedAgentIDs: []string{}, SkippedAgentIDs: []string{}}
	if len(requested) > 0 {
		result.QueuedAgentIDs, err = s.store.QueueAgentUpdates(request.Context(), Version, requested)
	}
	if result.QueuedAgentIDs == nil {
		result.QueuedAgentIDs = []string{}
	}
	if err != nil {
		result.Error = err.Error()
	}
	queued := make(map[string]bool, len(result.QueuedAgentIDs))
	for _, id := range result.QueuedAgentIDs {
		queued[id] = true
	}
	for _, id := range requested {
		if !queued[id] {
			result.SkippedAgentIDs = append(result.SkippedAgentIDs, id)
		}
	}
	if input.Mode == "all" && err == nil {
		if releaseErr := s.store.releaseMigrationExecutionPause(request.Context()); releaseErr != nil {
			result.Error = releaseErr.Error()
		}
	}
	result.Status, err = s.agentUpdatesStatus(request.Context())
	if err != nil {
		// Queued tasks are durable even if the follow-up read fails. Return their
		// real receipt so the client can refresh without blindly submitting again.
		result.Status = before
		result.Error = err.Error()
	}
	writeJSON(writer, http.StatusAccepted, result)
}
