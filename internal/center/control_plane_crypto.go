package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/petauron/vastora/internal/controlplane"
)

type AgentTaskEnvelope struct {
	ID       string                `json:"id"`
	Attempt  int64                 `json:"attempt"`
	Envelope controlplane.Envelope `json:"envelope"`
}

func (s *Store) EncryptAgentTask(ctx context.Context, agentID string, task AgentTask) (AgentTaskEnvelope, error) {
	var publicKey []byte
	err := s.db.QueryRowContext(ctx, `SELECT x25519_public_key FROM agents WHERE id = ? AND status = 'active' AND credential_revoked_at = ''`, agentID).Scan(&publicKey)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentTaskEnvelope{}, errors.New("center: Agent encryption identity is unavailable")
	}
	if err != nil {
		return AgentTaskEnvelope{}, fmt.Errorf("center: read Agent encryption identity: %w", err)
	}
	if err := controlplane.ValidatePublicKey(publicKey); err != nil {
		return AgentTaskEnvelope{}, errors.New("center: Agent must enroll again to establish its encryption identity")
	}
	plaintext, err := json.Marshal(task)
	if err != nil {
		return AgentTaskEnvelope{}, fmt.Errorf("center: encode Agent task: %w", err)
	}
	aad := controlplane.TaskAdditionalData(agentID, task.ID, task.Attempt)
	envelope, err := controlplane.Seal(publicKey, plaintext, aad)
	if err != nil {
		return AgentTaskEnvelope{}, err
	}
	return AgentTaskEnvelope{ID: task.ID, Attempt: task.Attempt, Envelope: envelope}, nil
}
