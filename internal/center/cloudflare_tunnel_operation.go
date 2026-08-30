package center

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type cloudflareTunnelOperation struct {
	AgentID        string
	AccountID      string
	OperationID    string
	TunnelName     string
	TunnelSecretID string
	TunnelID       string
	Phase          string
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (s *Store) ensureCloudflareTunnel(ctx context.Context, agentID string) error {
	s.cloudflareTunnelMu.Lock()
	defer s.cloudflareTunnelMu.Unlock()

	var existing int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cloudflare_tunnels WHERE agent_id = ?`, agentID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return nil
	}
	client, err := s.cloudflare(ctx)
	if err != nil {
		return err
	}
	operation, err := s.startCloudflareTunnelOperation(ctx, client.accountID, agentID)
	if err != nil {
		return err
	}
	secret, err := s.cloudflareTunnelOperationSecret(ctx, operation)
	if err != nil {
		return s.failCloudflareTunnelOperation(ctx, operation, err)
	}
	var recoveredToken string
	if operation.Phase == "intent" {
		claimed, claimErr := s.claimCloudflareTunnelCreation(ctx, operation)
		if claimErr != nil {
			return s.failCloudflareTunnelOperation(ctx, operation, claimErr)
		}
		if claimed {
			operation.Phase = "creating"
			created, createErr := client.createTunnel(ctx, operation.TunnelName, secret)
			if createErr == nil {
				if created.ID == "" || (created.Name != "" && created.Name != operation.TunnelName) || (created.AccountTag != "" && created.AccountTag != operation.AccountID) {
					cleanupErr := error(nil)
					if created.ID != "" {
						cleanupErr = client.deleteTunnel(context.WithoutCancel(ctx), created.ID)
					}
					failure := errors.Join(errors.New("center: Cloudflare returned a Tunnel that does not match the persisted operation"), cleanupErr)
					if cleanupErr == nil {
						_ = s.resetCloudflareTunnelOperation(ctx, operation, failure)
					}
					return s.failCloudflareTunnelOperation(ctx, operation, failure)
				}
				operation.TunnelID = created.ID
				if err := s.saveCloudflareTunnelOperationID(ctx, operation); err != nil {
					return s.failCloudflareTunnelOperation(ctx, operation, err)
				}
				operation.Phase = "created"
			} else {
				recovered, token, reconcileErr := client.reconcileOwnedTunnel(ctx, operation, secret)
				if reconcileErr != nil {
					failure := errors.Join(createErr, reconcileErr)
					if cloudflareCreateDefinitelyRejected(createErr) {
						_ = s.resetCloudflareTunnelOperation(ctx, operation, failure)
					} else {
						failure = fmt.Errorf("center: Cloudflare Tunnel creation outcome is unresolved; retry this publication to reconcile the persisted operation without creating another Tunnel: %w", failure)
					}
					return s.failCloudflareTunnelOperation(ctx, operation, failure)
				}
				operation.TunnelID = recovered.ID
				recoveredToken = token
				if err := s.saveCloudflareTunnelOperationID(ctx, operation); err != nil {
					return s.failCloudflareTunnelOperation(ctx, operation, err)
				}
				operation.Phase = "created"
			}
		} else {
			operation, err = s.cloudflareTunnelOperation(ctx, agentID)
			if err != nil {
				return err
			}
		}
	}
	if operation.Phase == "creating" {
		recovered, token, reconcileErr := client.reconcileOwnedTunnel(ctx, operation, secret)
		if reconcileErr != nil {
			failure := fmt.Errorf("center: pending Cloudflare Tunnel creation has not converged; retry after Cloudflare propagation or inspect the persisted operation in diagnostics: %w", reconcileErr)
			return s.failCloudflareTunnelOperation(ctx, operation, failure)
		}
		operation.TunnelID = recovered.ID
		recoveredToken = token
		if err := s.saveCloudflareTunnelOperationID(ctx, operation); err != nil {
			return s.failCloudflareTunnelOperation(ctx, operation, err)
		}
		operation.Phase = "created"
	}
	if operation.Phase != "created" || operation.TunnelID == "" {
		return s.failCloudflareTunnelOperation(ctx, operation, errors.New("center: stored Cloudflare Tunnel operation is invalid"))
	}
	token := recoveredToken
	if token == "" {
		token, err = client.tunnelToken(ctx, operation.TunnelID)
		if err != nil {
			return s.failCloudflareTunnelOperation(ctx, operation, fmt.Errorf("center: Tunnel was created and will be resumed after token retrieval succeeds: %w", err))
		}
	}
	if err := s.completeCloudflareTunnelOperation(ctx, operation, token); err != nil {
		return s.failCloudflareTunnelOperation(ctx, operation, fmt.Errorf("center: Tunnel was created and will be resumed after local persistence succeeds: %w", err))
	}
	return nil
}

func (s *Store) startCloudflareTunnelOperation(ctx context.Context, accountID, agentID string) (cloudflareTunnelOperation, error) {
	if operation, exists, err := s.readCloudflareTunnelOperation(ctx, agentID); err != nil {
		return cloudflareTunnelOperation{}, err
	} else if exists {
		if operation.AccountID != accountID {
			return cloudflareTunnelOperation{}, errors.New("center: the pending Cloudflare Tunnel belongs to another account; reconnect that account to resume it")
		}
		return operation, nil
	}
	var nodeName string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, agentID).Scan(&nodeName); err != nil {
		return cloudflareTunnelOperation{}, errors.New("center: Tunnel node not found")
	}
	operationID, err := randomToken(18)
	if err != nil {
		return cloudflareTunnelOperation{}, err
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		return cloudflareTunnelOperation{}, fmt.Errorf("center: generate Cloudflare Tunnel operation secret: %w", err)
	}
	secret := base64.StdEncoding.EncodeToString(secretBytes)
	namePrefix := sanitizeCloudflareName(nodeName)
	if len(namePrefix) > 40 {
		namePrefix = strings.Trim(namePrefix[:40], "-")
	}
	tunnelName := "vastora-" + namePrefix + "-" + sanitizeCloudflareName(operationID)
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return cloudflareTunnelOperation{}, err
	}
	defer tx.Rollback()
	secretID, err := s.putSecret(ctx, tx, []byte(secret), "cloudflare-tunnel-operation:"+agentID)
	if err != nil {
		return cloudflareTunnelOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cloudflare_tunnel_operations(
		agent_id, account_id, operation_id, tunnel_name, tunnel_secret_id, phase, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, 'intent', ?, ?)`, agentID, accountID, operationID, tunnelName, secretID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		_ = tx.Rollback()
		if operation, exists, readErr := s.readCloudflareTunnelOperation(ctx, agentID); readErr == nil && exists {
			if operation.AccountID != accountID {
				return cloudflareTunnelOperation{}, errors.New("center: the pending Cloudflare Tunnel belongs to another account; reconnect that account to resume it")
			}
			return operation, nil
		}
		return cloudflareTunnelOperation{}, fmt.Errorf("center: persist Cloudflare Tunnel creation intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return cloudflareTunnelOperation{}, fmt.Errorf("center: commit Cloudflare Tunnel creation intent: %w", err)
	}
	return cloudflareTunnelOperation{AgentID: agentID, AccountID: accountID, OperationID: operationID, TunnelName: tunnelName, TunnelSecretID: secretID, Phase: "intent", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) readCloudflareTunnelOperation(ctx context.Context, agentID string) (cloudflareTunnelOperation, bool, error) {
	return s.readCloudflareTunnelOperationQuery(ctx, s.db, agentID)
}

type cloudflareTunnelOperationQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *Store) readCloudflareTunnelOperationQuery(ctx context.Context, query cloudflareTunnelOperationQuerier, agentID string) (cloudflareTunnelOperation, bool, error) {
	var operation cloudflareTunnelOperation
	var createdAt, updatedAt string
	err := query.QueryRowContext(ctx, `SELECT agent_id, account_id, operation_id, tunnel_name, tunnel_secret_id,
		tunnel_id, phase, last_error, created_at, updated_at FROM cloudflare_tunnel_operations WHERE agent_id = ?`, agentID).Scan(
		&operation.AgentID, &operation.AccountID, &operation.OperationID, &operation.TunnelName, &operation.TunnelSecretID,
		&operation.TunnelID, &operation.Phase, &operation.LastError, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return cloudflareTunnelOperation{}, false, nil
	}
	if err != nil {
		return cloudflareTunnelOperation{}, false, fmt.Errorf("center: read Cloudflare Tunnel operation: %w", err)
	}
	operation.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	operation.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return operation, true, nil
}

func (s *Store) cloudflareTunnelOperation(ctx context.Context, agentID string) (cloudflareTunnelOperation, error) {
	operation, exists, err := s.readCloudflareTunnelOperation(ctx, agentID)
	if err != nil {
		return cloudflareTunnelOperation{}, err
	}
	if !exists {
		return cloudflareTunnelOperation{}, errors.New("center: Cloudflare Tunnel operation disappeared while applying")
	}
	return operation, nil
}

func (s *Store) cloudflareTunnelOperationSecret(ctx context.Context, operation cloudflareTunnelOperation) (string, error) {
	value, err := s.getSecret(ctx, operation.TunnelSecretID, "cloudflare-tunnel-operation:"+operation.AgentID)
	if err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(string(value))
	if err != nil || len(decoded) != 32 {
		return "", errors.New("center: stored Cloudflare Tunnel operation secret is invalid")
	}
	return string(value), nil
}

func (s *Store) claimCloudflareTunnelCreation(ctx context.Context, operation cloudflareTunnelOperation) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE cloudflare_tunnel_operations SET phase = 'creating', last_error = '', updated_at = ?
		WHERE agent_id = ? AND operation_id = ? AND phase = 'intent'`, s.now().UTC().Format(time.RFC3339Nano), operation.AgentID, operation.OperationID)
	if err != nil {
		return false, fmt.Errorf("center: claim Cloudflare Tunnel creation: %w", err)
	}
	changed, _ := result.RowsAffected()
	return changed == 1, nil
}

func (s *Store) saveCloudflareTunnelOperationID(ctx context.Context, operation cloudflareTunnelOperation) error {
	result, err := s.db.ExecContext(ctx, `UPDATE cloudflare_tunnel_operations SET tunnel_id = ?, phase = 'created', last_error = '', updated_at = ?
		WHERE agent_id = ? AND operation_id = ? AND phase IN ('creating', 'created') AND (tunnel_id = '' OR tunnel_id = ?)`,
		operation.TunnelID, s.now().UTC().Format(time.RFC3339Nano), operation.AgentID, operation.OperationID, operation.TunnelID)
	if err != nil {
		return fmt.Errorf("center: persist created Cloudflare Tunnel identity: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: Cloudflare Tunnel operation changed while saving its identity")
	}
	return nil
}

func (s *Store) resetCloudflareTunnelOperation(ctx context.Context, operation cloudflareTunnelOperation, cause error) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cloudflare_tunnel_operations SET phase = 'intent', tunnel_id = '', last_error = ?, updated_at = ?
		WHERE agent_id = ? AND operation_id = ? AND phase = 'creating'`, truncateCloudflareOperationError(cause), s.now().UTC().Format(time.RFC3339Nano), operation.AgentID, operation.OperationID)
	return err
}

func (s *Store) failCloudflareTunnelOperation(ctx context.Context, operation cloudflareTunnelOperation, cause error) error {
	_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `UPDATE cloudflare_tunnel_operations SET last_error = ?, updated_at = ?
		WHERE agent_id = ? AND operation_id = ?`, truncateCloudflareOperationError(cause), s.now().UTC().Format(time.RFC3339Nano), operation.AgentID, operation.OperationID)
	return cause
}

func truncateCloudflareOperationError(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) > 2048 {
		return message[:2048]
	}
	return message
}

func (s *Store) completeCloudflareTunnelOperation(ctx context.Context, operation cloudflareTunnelOperation, token string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var phase, tunnelID, operationSecretID string
	if err := tx.QueryRowContext(ctx, `SELECT phase, tunnel_id, tunnel_secret_id FROM cloudflare_tunnel_operations
		WHERE agent_id = ? AND operation_id = ?`, operation.AgentID, operation.OperationID).Scan(&phase, &tunnelID, &operationSecretID); err != nil {
		return err
	}
	if phase != "created" || tunnelID != operation.TunnelID || operationSecretID != operation.TunnelSecretID {
		return errors.New("center: Cloudflare Tunnel operation changed before local commit")
	}
	tokenSecretID, err := s.putSecret(ctx, tx, []byte(token), "cloudflare-tunnel:"+operation.AgentID)
	if err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO cloudflare_tunnels(agent_id, tunnel_id, tunnel_name, token_secret_id, desired_json, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, '{}', 'stopped', ?, ?)`, operation.AgentID, operation.TunnelID, operation.TunnelName, tokenSecretID, now, now); err != nil {
		return fmt.Errorf("center: save Cloudflare Tunnel: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cloudflare_tunnel_operations WHERE agent_id = ? AND operation_id = ?`, operation.AgentID, operation.OperationID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, operation.TunnelSecretID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("center: commit Cloudflare Tunnel: %w", err)
	}
	return nil
}

func cloudflareCreateDefinitelyRejected(err error) bool {
	var failure *cloudflareAPIError
	if !errors.As(err, &failure) {
		return false
	}
	return failure.StatusCode >= http.StatusBadRequest && failure.StatusCode < http.StatusInternalServerError && failure.StatusCode != http.StatusRequestTimeout && failure.StatusCode != http.StatusTooManyRequests
}

func (client cloudflareClient) reconcileOwnedTunnel(ctx context.Context, operation cloudflareTunnelOperation, expectedSecret string) (cloudflareTunnelRecord, string, error) {
	candidates, err := client.listTunnelsByName(ctx, operation.TunnelName)
	if err != nil {
		return cloudflareTunnelRecord{}, "", fmt.Errorf("center: reconcile Cloudflare Tunnel list: %w", err)
	}
	type match struct {
		record cloudflareTunnelRecord
		token  string
	}
	matches := []match{}
	verificationErrors := []error{}
	for _, candidate := range candidates {
		if candidate.ID == "" || candidate.Name != operation.TunnelName || candidate.AccountTag != operation.AccountID || (candidate.ConfigSrc != "" && candidate.ConfigSrc != "cloudflare") {
			continue
		}
		token, tokenErr := client.tunnelToken(ctx, candidate.ID)
		if tokenErr != nil {
			verificationErrors = append(verificationErrors, tokenErr)
			continue
		}
		if cloudflareTunnelTokenMatches(token, operation.AccountID, candidate.ID, expectedSecret) {
			matches = append(matches, match{record: candidate, token: token})
		}
	}
	if len(matches) == 1 {
		return matches[0].record, matches[0].token, nil
	}
	if len(matches) > 1 {
		return cloudflareTunnelRecord{}, "", errors.New("center: multiple remote Tunnels match the same persisted ownership secret; no Tunnel was adopted")
	}
	if len(verificationErrors) != 0 {
		return cloudflareTunnelRecord{}, "", errors.Join(verificationErrors...)
	}
	if len(candidates) != 0 {
		return cloudflareTunnelRecord{}, "", errors.New("center: same-name Cloudflare Tunnels exist but none prove ownership of this operation; no Tunnel was adopted or deleted")
	}
	return cloudflareTunnelRecord{}, "", errors.New("center: the persisted Cloudflare Tunnel operation is not visible remotely yet")
}

func cloudflareTunnelTokenMatches(token, accountID, tunnelID, secret string) bool {
	encoded := strings.TrimSpace(token)
	var decoded []byte
	var err error
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		decoded, err = encoding.DecodeString(encoded)
		if err == nil {
			break
		}
	}
	if err != nil {
		return false
	}
	var claims struct {
		AccountID string `json:"a"`
		TunnelID  string `json:"t"`
		Secret    string `json:"s"`
	}
	if json.Unmarshal(decoded, &claims) != nil {
		return false
	}
	return claims.AccountID == accountID && claims.TunnelID == tunnelID && subtle.ConstantTimeCompare([]byte(claims.Secret), []byte(secret)) == 1
}
