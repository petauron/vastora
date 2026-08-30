package center

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

type initialSetupOperation struct {
	InputHash string
	Phase     string
	SiteID    string
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) initialSetupOperation(ctx context.Context) (initialSetupOperation, bool, error) {
	var operation initialSetupOperation
	var createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT input_hash, phase, site_id, last_error, created_at, updated_at
		FROM initial_setup_operations WHERE id = 1`).Scan(&operation.InputHash, &operation.Phase, &operation.SiteID, &operation.LastError, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return initialSetupOperation{}, false, nil
	}
	if err != nil {
		return initialSetupOperation{}, false, fmt.Errorf("center: read initial setup operation: %w", err)
	}
	operation.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	operation.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return operation, true, nil
}

func (s *Store) startInitialSetupOperation(ctx context.Context, inputHash, firstPhase string) (initialSetupOperation, error) {
	if len(inputHash) != sha256.Size*2 || !validInitialSetupPhase(firstPhase) || firstPhase == "completed" {
		return initialSetupOperation{}, errors.New("center: initial setup operation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return initialSetupOperation{}, fmt.Errorf("center: begin initial setup operation: %w", err)
	}
	defer tx.Rollback()
	var operation initialSetupOperation
	var createdAt, updatedAt string
	err = tx.QueryRowContext(ctx, `SELECT input_hash, phase, site_id, last_error, created_at, updated_at
		FROM initial_setup_operations WHERE id = 1`).Scan(&operation.InputHash, &operation.Phase, &operation.SiteID, &operation.LastError, &createdAt, &updatedAt)
	if err == nil {
		if operation.InputHash != inputHash {
			return initialSetupOperation{}, errors.New("center: another initial setup operation is pending; retry the original setup choices")
		}
		operation.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		operation.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		return operation, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return initialSetupOperation{}, fmt.Errorf("center: inspect initial setup operation: %w", err)
	}
	var administrators, sites int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&administrators); err != nil {
		return initialSetupOperation{}, err
	}
	if administrators == 0 {
		return initialSetupOperation{}, errors.New("center: create the administrator before completing setup")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites`).Scan(&sites); err != nil {
		return initialSetupOperation{}, err
	}
	if sites != 0 {
		return initialSetupOperation{}, errors.New("center: initial setup is already complete")
	}
	siteID, err := randomToken(18)
	if err != nil {
		return initialSetupOperation{}, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO initial_setup_operations(id, input_hash, phase, site_id, created_at, updated_at)
		VALUES(1, ?, ?, ?, ?, ?)`, inputHash, firstPhase, siteID, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return initialSetupOperation{}, fmt.Errorf("center: persist initial setup operation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return initialSetupOperation{}, fmt.Errorf("center: commit initial setup operation: %w", err)
	}
	return initialSetupOperation{InputHash: inputHash, Phase: firstPhase, SiteID: siteID, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) advanceInitialSetupOperation(ctx context.Context, inputHash, currentPhase, nextPhase string) error {
	if !validInitialSetupPhase(currentPhase) || !validInitialSetupPhase(nextPhase) || currentPhase == "completed" {
		return errors.New("center: initial setup phase transition is invalid")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE initial_setup_operations SET phase = ?, last_error = '', updated_at = ?
		WHERE id = 1 AND input_hash = ? AND phase = ?`, nextPhase, s.now().UTC().Format(time.RFC3339Nano), inputHash, currentPhase)
	if err != nil {
		return fmt.Errorf("center: advance initial setup operation: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: initial setup operation changed while applying")
	}
	return nil
}

func (s *Store) recordInitialSetupError(ctx context.Context, inputHash string, cause error) {
	message := strings.TrimSpace(cause.Error())
	if len(message) > 2048 {
		message = message[:2048]
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE initial_setup_operations SET last_error = ?, updated_at = ? WHERE id = 1 AND input_hash = ?`,
		message, s.now().UTC().Format(time.RFC3339Nano), inputHash)
}

func validInitialSetupPhase(phase string) bool {
	switch phase {
	case "headscale", "fixed_endpoint", "remote_access", "commit", "completed":
		return true
	default:
		return false
	}
}

func firstInitialSetupPhase(input InitialSetupInput) string {
	if input.Headscale != nil {
		return "headscale"
	}
	if input.TailscaleFixedEndpoint != nil {
		return "fixed_endpoint"
	}
	if input.CenterRemoteAccess != nil {
		return "remote_access"
	}
	return "commit"
}

func nextInitialSetupPhase(input InitialSetupInput, current string) string {
	switch current {
	case "headscale":
		if input.TailscaleFixedEndpoint != nil {
			return "fixed_endpoint"
		}
		if input.CenterRemoteAccess != nil {
			return "remote_access"
		}
	case "fixed_endpoint":
		if input.CenterRemoteAccess != nil {
			return "remote_access"
		}
	}
	return "commit"
}

func (s *Server) preflightInitialSetup(ctx context.Context, input InitialSetupInput) (validatedInitialSetup, InitialSetupInput, string, error) {
	validated, err := validateInitialSetupInput(input)
	if err != nil {
		return validatedInitialSetup{}, InitialSetupInput{}, "", err
	}
	input.Site = validated.Site
	input.Network = CenterNetworkInput{AgentConnectionMode: validated.Network.AgentConnectionMode, AgentConnectURL: validated.Network.AgentConnectURL}
	if validated.Network.AgentConnectionMode != "headscale" {
		if input.Headscale != nil || input.TailscaleFixedEndpoint != nil {
			return validatedInitialSetup{}, InitialSetupInput{}, "", errors.New("center: Headscale options require secure private networking")
		}
		if input.CenterRemoteAccess != nil && input.CenterRemoteAccess.Enabled {
			return validatedInitialSetup{}, InitialSetupInput{}, "", errors.New("center: the remote fallback is available only with secure private networking")
		}
	}
	requestedHeadscaleMode := ""
	if input.Headscale != nil {
		headscale := *input.Headscale
		headscale.Mode = strings.TrimSpace(headscale.Mode)
		headscale.APIKey = strings.TrimSpace(headscale.APIKey)
		switch headscale.Mode {
		case "builtin":
			if s.infrastructure == nil {
				return validatedInitialSetup{}, InitialSetupInput{}, "", errors.New("center: this installation does not include the Headscale deployment helper")
			}
			if headscale.APIKey != "" {
				return validatedInitialSetup{}, InitialSetupInput{}, "", errors.New("center: built-in Headscale creates its API key automatically")
			}
			headscale.URL, err = normalizeHeadscaleEndpoint(headscale.URL)
		case "external":
			headscale.URL, err = s.store.authorizedHeadscaleEndpoint(headscale.URL)
			if err == nil && len(headscale.APIKey) < 20 {
				err = errors.New("center: Headscale API key is required")
			}
		default:
			err = errors.New("center: Headscale mode must be builtin or external")
		}
		if err != nil {
			return validatedInitialSetup{}, InitialSetupInput{}, "", err
		}
		requestedHeadscaleMode = headscale.Mode
		input.Headscale = &headscale
	} else if validated.Network.AgentConnectionMode == "headscale" {
		integration, integrationErr := s.store.Integration(ctx, "headscale")
		if integrationErr != nil {
			return validatedInitialSetup{}, InitialSetupInput{}, "", integrationErr
		}
		if integration.Status != "configured" {
			return validatedInitialSetup{}, InitialSetupInput{}, "", errors.New("center: configure Headscale before selecting it as the Agent connection mode")
		}
		requestedHeadscaleMode = integration.Mode
	}
	if input.TailscaleFixedEndpoint != nil {
		if requestedHeadscaleMode != "builtin" {
			return validatedInitialSetup{}, InitialSetupInput{}, "", errors.New("center: a managed fixed endpoint requires built-in Headscale")
		}
		fixedInput, _, validateErr := s.store.validateTailscaleFixedEndpointInput(ctx, *input.TailscaleFixedEndpoint)
		if validateErr != nil {
			return validatedInitialSetup{}, InitialSetupInput{}, "", validateErr
		}
		input.TailscaleFixedEndpoint = &fixedInput
	}
	if input.CenterRemoteAccess != nil {
		remote := *input.CenterRemoteAccess
		if remote.Enabled {
			if s.infrastructure == nil {
				return validatedInitialSetup{}, InitialSetupInput{}, "", errors.New("center: this installation does not include the remote access deployment helper")
			}
			cloudflare, cloudflareErr := s.store.Integration(ctx, "cloudflare")
			if cloudflareErr != nil {
				return validatedInitialSetup{}, InitialSetupInput{}, "", cloudflareErr
			}
			if cloudflare.Status != "configured" || cloudflare.Mode != "oauth" || !cloudflare.AccessManagement {
				return validatedInitialSetup{}, InitialSetupInput{}, "", errors.New("center: Cloudflare OAuth with Access permissions is required for remote access")
			}
			remote, _, err = normalizeCenterRemoteAccess(remote, validated.Network.AgentConnectURL, cloudflare.Endpoint)
			if err != nil {
				return validatedInitialSetup{}, InitialSetupInput{}, "", err
			}
		} else {
			remote = CenterRemoteAccessInput{}
		}
		input.CenterRemoteAccess = &remote
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return validatedInitialSetup{}, InitialSetupInput{}, "", err
	}
	digest := sha256.Sum256(encoded)
	return validated, input, hex.EncodeToString(digest[:]), nil
}

func (s *Store) configuredHeadscaleMatchesSetup(ctx context.Context, input HeadscaleInput) (bool, error) {
	var mode, endpoint, secretID, status string
	err := s.db.QueryRowContext(ctx, `SELECT mode, endpoint, COALESCE(secret_id, ''), status FROM network_integrations WHERE kind = 'headscale'`).Scan(&mode, &endpoint, &secretID, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != "configured" || mode != input.Mode || endpoint != input.URL {
		return false, errors.New("center: existing Headscale configuration does not match the pending initial setup")
	}
	if input.Mode == "external" {
		if secretID == "" {
			return false, errors.New("center: existing Headscale configuration has no credential")
		}
		stored, err := s.getSecret(ctx, secretID, "integration:headscale")
		if err != nil {
			return false, err
		}
		if subtle.ConstantTimeCompare(stored, []byte(input.APIKey)) != 1 {
			return false, errors.New("center: existing Headscale credential does not match the pending initial setup")
		}
	}
	return true, nil
}

func (s *Server) CompleteInitialSetup(ctx context.Context, input InitialSetupInput) (InitialSetupResult, error) {
	s.store.initialSetupMu.Lock()
	defer s.store.initialSetupMu.Unlock()
	validated, normalized, inputHash, err := s.preflightInitialSetup(ctx, input)
	if err != nil {
		return InitialSetupResult{}, err
	}
	operation, err := s.store.startInitialSetupOperation(ctx, inputHash, firstInitialSetupPhase(normalized))
	if err != nil {
		return InitialSetupResult{}, err
	}
	fail := func(cause error) (InitialSetupResult, error) {
		s.store.recordInitialSetupError(context.WithoutCancel(ctx), inputHash, cause)
		return InitialSetupResult{}, cause
	}
	for {
		switch operation.Phase {
		case "headscale":
			if normalized.Headscale == nil {
				return fail(errors.New("center: pending initial setup is missing its Headscale configuration"))
			}
			matches, matchErr := s.store.configuredHeadscaleMatchesSetup(ctx, *normalized.Headscale)
			if matchErr != nil {
				return fail(matchErr)
			}
			if !matches {
				if _, err := s.configureHeadscaleOperation(ctx, *normalized.Headscale, validated.Network.AgentConnectURL, "setup-"+inputHash); err != nil {
					return fail(err)
				}
			} else if err := s.finalizeSetupHeadscale(ctx, normalized.Headscale.Mode, "setup-"+inputHash); err != nil {
				return fail(err)
			}
			next := nextInitialSetupPhase(normalized, operation.Phase)
			if err := s.store.advanceInitialSetupOperation(ctx, inputHash, operation.Phase, next); err != nil {
				return fail(err)
			}
			operation.Phase = next
		case "fixed_endpoint":
			if normalized.TailscaleFixedEndpoint == nil {
				return fail(errors.New("center: pending initial setup is missing its fixed-endpoint configuration"))
			}
			if _, err := s.store.ConfigureTailscaleFixedEndpoint(ctx, *normalized.TailscaleFixedEndpoint); err != nil {
				return fail(err)
			}
			next := nextInitialSetupPhase(normalized, operation.Phase)
			if err := s.store.advanceInitialSetupOperation(ctx, inputHash, operation.Phase, next); err != nil {
				return fail(err)
			}
			operation.Phase = next
		case "remote_access":
			if normalized.CenterRemoteAccess == nil {
				return fail(errors.New("center: pending initial setup is missing its remote-access configuration"))
			}
			if _, err := s.ConfigureCenterRemoteAccess(ctx, *normalized.CenterRemoteAccess, validated.Network.AgentConnectURL); err != nil {
				return fail(err)
			}
			next := nextInitialSetupPhase(normalized, operation.Phase)
			if err := s.store.advanceInitialSetupOperation(ctx, inputHash, operation.Phase, next); err != nil {
				return fail(err)
			}
			operation.Phase = next
		case "commit", "completed":
			result, err := s.store.completeInitialSetup(ctx, validated, inputHash, operation.SiteID)
			if err != nil {
				return fail(err)
			}
			return result, nil
		default:
			return fail(errors.New("center: stored initial setup phase is invalid"))
		}
	}
}

func (s *Server) finalizeSetupHeadscale(ctx context.Context, mode, operationID string) error {
	if mode == "builtin" {
		committer, ok := s.infrastructure.(deployapi.HeadscaleInstallCommitter)
		if !ok {
			return errors.New("center: deployment helper does not support recoverable Headscale installation")
		}
		if err := committer.CommitHeadscaleInstall(ctx, deployapi.HeadscaleInstallCommitRequest{OperationID: operationID}); err != nil {
			return err
		}
		if err := s.store.reconcileHeadscaleDNS(ctx); err != nil {
			return err
		}
		if err := s.store.queueAllGatewayStates(ctx); err != nil {
			return err
		}
		return s.store.markBuiltinHeadscaleRuntime(ctx)
	}
	_, err := s.store.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, tailscaleFixedEndpointSetting)
	if err != nil {
		return fmt.Errorf("center: disable managed Tailscale fixed endpoint: %w", err)
	}
	return nil
}
