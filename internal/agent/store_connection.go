package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

func (s *Store) SaveConnection(ctx context.Context, connection Connection) error {
	fingerprint, certificatePEM, err := normalizeCenterTrust(connection.CenterURL, connection.CAFingerprint, connection.CACertificatePEM)
	if err != nil {
		return err
	}
	connection.CAFingerprint = fingerprint
	connection.CACertificatePEM = certificatePEM
	if err := validateCAFingerprint(connection.CenterURL, connection.CAFingerprint); err != nil {
		return err
	}
	sealedCredential, sealedPrivateKey, err := s.sealConnection(connection)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint, ca_certificate_pem) VALUES(1, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`, connection.AgentID, connection.Name, connection.CenterURL, sealedCredential, sealedPrivateKey, normalizeCAFingerprint(connection.CAFingerprint), strings.TrimSpace(connection.CACertificatePEM))
	if err != nil {
		return fmt.Errorf("agent: save Center connection: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("agent: save Center connection: %w", err)
	}
	if changed != 1 {
		return errors.New("agent: already enrolled; clear the Agent data directory before enrolling again")
	}
	return nil
}

// ReplaceConnection changes only the control-plane identity. Application,
// gateway, and recovery state remain intact so an explicitly approved Center
// migration cannot stop or forget locally managed workloads.
func (s *Store) ReplaceConnection(ctx context.Context, connection Connection) error {
	fingerprint, certificatePEM, err := normalizeCenterTrust(connection.CenterURL, connection.CAFingerprint, connection.CACertificatePEM)
	if err != nil {
		return err
	}
	connection.CAFingerprint = fingerprint
	connection.CACertificatePEM = certificatePEM
	sealedCredential, sealedPrivateKey, err := s.sealConnection(connection)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO control_plane_connection(id, agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint, ca_certificate_pem)
		VALUES(1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET agent_id = excluded.agent_id, name = excluded.name, center_url = excluded.center_url, sealed_credential = excluded.sealed_credential, sealed_private_key = excluded.sealed_private_key, ca_fingerprint = excluded.ca_fingerprint, ca_certificate_pem = excluded.ca_certificate_pem`, connection.AgentID, connection.Name, connection.CenterURL, sealedCredential, sealedPrivateKey, normalizeCAFingerprint(connection.CAFingerprint), strings.TrimSpace(connection.CACertificatePEM)); err != nil {
		return fmt.Errorf("agent: replace Center connection: %w", err)
	}
	return nil
}

// HasConnection reports whether enrollment state is present without requiring
// its encrypted contents to be readable. Install recovery must distinguish a
// missing enrollment from a damaged one without silently treating damage as a
// fresh host.
func (s *Store) HasConnection(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM control_plane_connection WHERE id = 1`).Scan(&count); err != nil {
		return false, fmt.Errorf("agent: inspect Center connection: %w", err)
	}
	return count == 1, nil
}

func (s *Store) sealConnection(connection Connection) ([]byte, []byte, error) {
	connection.AgentID = strings.TrimSpace(connection.AgentID)
	connection.Name = strings.TrimSpace(connection.Name)
	connection.CenterURL = strings.TrimSpace(connection.CenterURL)
	if connection.AgentID == "" || connection.Name == "" || connection.CenterURL == "" || connection.Credential == "" {
		return nil, nil, errors.New("agent: incomplete Center connection")
	}
	if _, err := controlplane.PublicKey(connection.PrivateKey); err != nil {
		return nil, nil, errors.New("agent: incomplete Center encryption identity")
	}
	fingerprint, certificatePEM, err := normalizeCenterTrust(connection.CenterURL, connection.CAFingerprint, connection.CACertificatePEM)
	if err != nil {
		return nil, nil, err
	}
	connection.CAFingerprint = fingerprint
	connection.CACertificatePEM = certificatePEM
	sealedCredential, err := secret.Seal(s.key, []byte(connection.Credential), []byte("agent-control-plane:"+connection.AgentID))
	if err != nil {
		return nil, nil, fmt.Errorf("agent: encrypt Center credential: %w", err)
	}
	sealedPrivateKey, err := secret.Seal(s.key, connection.PrivateKey, []byte("agent-control-plane-key:"+connection.AgentID))
	if err != nil {
		return nil, nil, fmt.Errorf("agent: encrypt Center identity: %w", err)
	}
	return sealedCredential, sealedPrivateKey, nil
}

func (s *Store) Connection(ctx context.Context) (Connection, error) {
	var connection Connection
	var sealedCredential, sealedPrivateKey []byte
	err := s.db.QueryRowContext(ctx, `SELECT agent_id, name, center_url, sealed_credential, sealed_private_key, ca_fingerprint, ca_certificate_pem FROM control_plane_connection WHERE id = 1`).Scan(&connection.AgentID, &connection.Name, &connection.CenterURL, &sealedCredential, &sealedPrivateKey, &connection.CAFingerprint, &connection.CACertificatePEM)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, errors.New("agent: not enrolled")
	}
	if err != nil {
		return Connection{}, fmt.Errorf("agent: read Center connection: %w", err)
	}
	credential, err := secret.Open(s.key, sealedCredential, []byte("agent-control-plane:"+connection.AgentID))
	if err != nil {
		return Connection{}, fmt.Errorf("agent: decrypt Center credential: %w", err)
	}
	connection.Credential = string(credential)
	privateKey, err := secret.Open(s.key, sealedPrivateKey, []byte("agent-control-plane-key:"+connection.AgentID))
	if err != nil {
		return Connection{}, fmt.Errorf("agent: decrypt Center identity: %w", err)
	}
	if _, err := controlplane.PublicKey(privateKey); err != nil {
		return Connection{}, errors.New("agent: Center encryption identity is invalid; enroll the Agent again")
	}
	connection.PrivateKey = privateKey
	if connection.CAFingerprint != "" {
		if _, _, err := normalizeCenterTrust(connection.CenterURL, connection.CAFingerprint, connection.CACertificatePEM); err != nil {
			return Connection{}, err
		}
	} else if !loopbackCenterURL(connection.CenterURL) {
		// Schema-3 Agents acquire and persist the verified CA root before their
		// first post-upgrade request. New enrollments always have a pin.
		return connection, nil
	}
	return connection, nil
}
