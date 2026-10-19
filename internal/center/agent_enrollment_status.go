package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/secret"
)

const enrollmentStatusContext = "agent-enrollment-status"

type AgentEnrollmentCommand struct {
	AgentEnrollment
	StatusID string `json:"statusId"`
}

type AgentEnrollmentStatus struct {
	State     string `json:"state"`
	AgentID   string `json:"agentId,omitempty"`
	Connected bool   `json:"connected"`
}

// A separate, authenticated opaque locator exposes neither the one-shot token
// nor its database hash. It is not an Agent/bootstrap credential.
func (s *Store) agentEnrollmentStatusID(token string) (string, error) {
	sealed, err := secret.Seal(s.key, tokenHash(token), []byte(enrollmentStatusContext))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Store) AgentEnrollmentStatus(ctx context.Context, statusID string) (AgentEnrollmentStatus, error) {
	invalid := errors.New("center: enrollment status reference is unavailable")
	if len(statusID) == 0 || len(statusID) > 128 {
		return AgentEnrollmentStatus{}, invalid
	}
	sealed, err := base64.RawURLEncoding.DecodeString(statusID)
	if err != nil {
		return AgentEnrollmentStatus{}, invalid
	}
	hash, err := secret.Open(s.key, sealed, []byte(enrollmentStatusContext))
	if err != nil || len(hash) != sha256.Size {
		return AgentEnrollmentStatus{}, invalid
	}
	var expires string
	var used, agentID, state, revoked, lastSeen, registeredAt sql.NullString
	err = s.db.QueryRowContext(ctx, `SELECT t.expires_at,t.used_at,o.agent_id,a.status,a.credential_revoked_at,a.last_seen_at,o.created_at
		FROM agent_enrollment_tokens t
		LEFT JOIN agent_enrollment_operations o ON o.token_hash=t.token_hash
		LEFT JOIN agents a ON a.id=o.agent_id
		WHERE t.token_hash=?`, hash).Scan(&expires, &used, &agentID, &state, &revoked, &lastSeen, &registeredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentEnrollmentStatus{State: "unavailable"}, nil
	}
	if err != nil {
		return AgentEnrollmentStatus{}, err
	}
	if used.Valid {
		if !agentID.Valid || !state.Valid {
			return AgentEnrollmentStatus{State: "unavailable"}, nil
		}
		seen, err := time.Parse(time.RFC3339Nano, lastSeen.String)
		if err != nil {
			return AgentEnrollmentStatus{}, errors.New("center: invalid enrollment heartbeat time")
		}
		registered, err := time.Parse(time.RFC3339Nano, registeredAt.String)
		if err != nil {
			return AgentEnrollmentStatus{}, errors.New("center: invalid enrollment registration time")
		}
		// Registration initializes last_seen_at too. Require a subsequent live
		// heartbeat, not merely that the one-shot enrollment request succeeded.
		connected := state.String == "active" && revoked.String == "" && seen.After(registered) && seen.After(s.now().Add(-agentConnectedMaxAge))
		status := "enrolled"
		if connected {
			status = "connected"
		}
		return AgentEnrollmentStatus{State: status, AgentID: agentID.String, Connected: connected}, nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return AgentEnrollmentStatus{}, errors.New("center: invalid enrollment expiry time")
	}
	if !s.now().Before(deadline) {
		return AgentEnrollmentStatus{State: "expired"}, nil
	}
	return AgentEnrollmentStatus{State: "pending"}, nil
}

func (s *Server) handleAgentEnrollmentStatus(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if _, err := s.requestAdminID(request); err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	status, err := s.store.AgentEnrollmentStatus(request.Context(), request.PathValue("statusID"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("center: enrollment status is unavailable"))
		return
	}
	writeJSON(writer, http.StatusOK, status)
}
