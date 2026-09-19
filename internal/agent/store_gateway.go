package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
)

type GatewayAppliedState struct {
	Desired      gateway.DesiredState  `json:"desired"`
	Certificates []gateway.Certificate `json:"-"`
	ConfigHash   string                `json:"configHash"`
	AppliedAt    time.Time             `json:"appliedAt"`
}

func (s *Store) GatewayState(ctx context.Context) (GatewayAppliedState, error) {
	var encoded, sealedCertificates []byte
	var state GatewayAppliedState
	var appliedAt string
	err := s.db.QueryRowContext(ctx, `SELECT desired_json, sealed_certificates, config_hash, applied_at FROM gateway_applied_state WHERE id = 1`).Scan(&encoded, &sealedCertificates, &state.ConfigHash, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return GatewayAppliedState{}, errNoAppliedGatewayState
	}
	if err != nil {
		return GatewayAppliedState{}, fmt.Errorf("agent: read gateway state: %w", err)
	}
	if json.Unmarshal(encoded, &state.Desired) != nil || state.Desired.Validate() != nil {
		return GatewayAppliedState{}, errors.New("agent: stored gateway state is invalid")
	}
	certificateJSON, err := secret.Open(s.key, sealedCertificates, []byte("agent-gateway-certificates"))
	if err != nil || json.Unmarshal(certificateJSON, &state.Certificates) != nil || gateway.ValidateCertificates(state.Certificates) != nil {
		return GatewayAppliedState{}, errors.New("agent: stored gateway certificates are invalid")
	}
	state.AppliedAt, err = time.Parse(time.RFC3339Nano, appliedAt)
	if err != nil {
		return GatewayAppliedState{}, errors.New("agent: stored gateway timestamp is invalid")
	}
	return state, nil
}

func (s *Store) RecordGatewayState(ctx context.Context, desired gateway.DesiredState, certificates []gateway.Certificate) (GatewayAppliedState, error) {
	if err := desired.Validate(); err != nil {
		return GatewayAppliedState{}, err
	}
	desired = desired.Sorted()
	if err := gateway.ValidateCertificates(certificates); err != nil {
		return GatewayAppliedState{}, err
	}
	encoded, err := json.Marshal(desired)
	if err != nil {
		return GatewayAppliedState{}, err
	}
	certificateJSON, err := json.Marshal(certificates)
	if err != nil {
		return GatewayAppliedState{}, err
	}
	sealedCertificates, err := secret.Seal(s.key, certificateJSON, []byte("agent-gateway-certificates"))
	if err != nil {
		return GatewayAppliedState{}, fmt.Errorf("agent: encrypt gateway certificates: %w", err)
	}
	configHash, err := gateway.ConfigurationHash(desired, certificates)
	if err != nil {
		return GatewayAppliedState{}, err
	}
	now := s.now().UTC()
	state := GatewayAppliedState{Desired: desired, Certificates: append([]gateway.Certificate(nil), certificates...), ConfigHash: configHash, AppliedAt: now}
	result, err := s.db.ExecContext(ctx, `INSERT INTO gateway_applied_state(id, applied_revision, desired_json, sealed_certificates, config_hash, applied_at)
		VALUES(1, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET applied_revision = excluded.applied_revision, desired_json = excluded.desired_json, sealed_certificates = excluded.sealed_certificates, config_hash = excluded.config_hash, applied_at = excluded.applied_at
		WHERE excluded.applied_revision >= gateway_applied_state.applied_revision`, desired.Revision, encoded, sealedCertificates, state.ConfigHash, now.Format(time.RFC3339Nano))
	if err != nil {
		return GatewayAppliedState{}, fmt.Errorf("agent: record gateway state: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return GatewayAppliedState{}, fmt.Errorf("agent: gateway revision %d is older than the persisted configuration", desired.Revision)
	}
	return state, nil
}

func (s *Store) ClearGatewayState(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM gateway_applied_state WHERE id = 1`); err != nil {
		return fmt.Errorf("agent: clear gateway state: %w", err)
	}
	return nil
}
