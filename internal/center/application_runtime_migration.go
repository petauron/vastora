package center

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/secret"
)

type runtimeMigrationApplication struct {
	applicationID        string
	appKey               string
	deploymentID         string
	appVersion           string
	manifestJSON         []byte
	configJSON           []byte
	serviceAddress       string
	secretID             sql.NullString
	registryCredentialID sql.NullString
}

// queueApplicationRuntimeMigration replays stored desired state once after an
// Agent adopts a newer runtime contract. Persistent application data remains
// attached to the same volumes; only the executor-owned runtime is reconciled.
func (s *Store) queueApplicationRuntimeMigration(ctx context.Context, tx *sql.Tx, agentID string, generation int, now time.Time) error {
	if err := s.queueRuntimeApplicationDeployments(ctx, tx, agentID, generation, now); err != nil {
		return err
	}
	var gatewayGeneration int64
	result, err := tx.ExecContext(ctx, `UPDATE gateway_components SET generation = generation + 1, status = 'pending', attempt = 0, lease_expires_at = '', last_error = '', updated_at = ?
		WHERE gateway_node_id = ? AND desired_status = 'running'`, now.Format(time.RFC3339Nano), agentID)
	if err != nil {
		return fmt.Errorf("center: queue gateway runtime migration: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 0 {
		if err := tx.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, agentID).Scan(&gatewayGeneration); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, gatewayComponentTaskID(agentID, gatewayGeneration), agentID, "gateway.component.apply", gatewayGeneration, "queued", fmt.Sprintf("application runtime generation %d", generation)); err != nil {
			return err
		}
		if err := s.queueGatewayState(ctx, tx, agentID, now); err != nil {
			return err
		}
	}
	var nodeListenerExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_listener_states WHERE node_id = ?)`, agentID).Scan(&nodeListenerExists); err != nil {
		return err
	}
	if nodeListenerExists {
		if err := s.queueNodeListenerState(ctx, tx, agentID, now); err != nil {
			return err
		}
	}
	var tunnelExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cloudflare_tunnels WHERE agent_id = ?)`, agentID).Scan(&tunnelExists); err != nil {
		return err
	}
	if tunnelExists {
		if err := s.queueTunnelState(ctx, tx, agentID, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queueRuntimeApplicationDeployments(ctx context.Context, tx *sql.Tx, agentID string, generation int, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.app_key, d.id, d.app_version, d.manifest_json, d.config_json, d.service_address, d.secret_id, d.registry_credential_id
		FROM applications a
		JOIN deployments d ON d.rowid = (
			SELECT previous.rowid FROM deployments previous
			WHERE previous.application_id = a.id AND previous.state = 'succeeded' AND previous.operation IN ('install', 'upgrade', 'configure')
			ORDER BY previous.updated_at DESC, previous.rowid DESC LIMIT 1
		)
		WHERE a.node_id = ? AND a.status IN ('running', 'pending', 'failed') AND a.runtime_generation < ?
		AND NOT EXISTS (
			SELECT 1 FROM deployments active WHERE active.application_id = a.id
			AND (active.state IN ('pending', 'running') OR active.reconciliation_required = 1)
		)
		ORDER BY a.id`, agentID, generation)
	if err != nil {
		return fmt.Errorf("center: inspect applications for runtime migration: %w", err)
	}
	applications := []runtimeMigrationApplication{}
	for rows.Next() {
		var app runtimeMigrationApplication
		if err := rows.Scan(&app.applicationID, &app.appKey, &app.deploymentID, &app.appVersion, &app.manifestJSON, &app.configJSON, &app.serviceAddress, &app.secretID, &app.registryCredentialID); err != nil {
			rows.Close()
			return err
		}
		applications = append(applications, app)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, app := range applications {
		if app.appKey == komariAppKey {
			manifest, err := s.currentCatalogManifest(ctx, tx, app.appKey)
			if err != nil {
				return err
			}
			app.manifestJSON, err = json.Marshal(manifest)
			if err != nil {
				return err
			}
			app.appVersion = manifest.Version
		}
		if app.registryCredentialID.Valid {
			var manifest catalog.AppManifest
			if err := json.Unmarshal(app.manifestJSON, &manifest); err != nil {
				return errors.New("center: stored application manifest is invalid during runtime migration")
			}
			if err := validateRegistryCredentialBinding(ctx, tx, app.registryCredentialID.String, manifest); err != nil {
				return err
			}
		}
		deploymentID, err := randomToken(18)
		if err != nil {
			return err
		}
		var newSecretID any
		if app.secretID.Valid {
			var sealed []byte
			if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, app.secretID.String).Scan(&sealed); err != nil {
				return fmt.Errorf("center: read application secret for runtime migration: %w", err)
			}
			plaintext, err := secret.Open(s.key, sealed, []byte("deployment:"+app.deploymentID))
			if err != nil {
				return fmt.Errorf("center: decrypt application secret for runtime migration: %w", err)
			}
			newSecretID, err = s.putSecret(ctx, tx, plaintext, "deployment:"+deploymentID)
			if err != nil {
				return err
			}
		}
		formattedNow := now.Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, service_address, secret_id, registry_credential_id, operation, delete_data, state, error, created_at, updated_at, application_id, runtime_generation)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'configure', 0, 'pending', '', ?, ?, ?, ?)`, deploymentID, agentID, app.appKey, app.appVersion, app.manifestJSON, app.configJSON, app.serviceAddress, newSecretID, nullableSQLString(app.registryCredentialID), formattedNow, formattedNow, app.applicationID, generation); err != nil {
			return fmt.Errorf("center: queue application runtime migration: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'pending', runtime = CASE WHEN app_key = ? THEN 'host' ELSE runtime END, updated_at = ? WHERE id = ?`, komariAppKey, formattedNow, app.applicationID); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, deploymentID, agentID, "application.apply", applicationTaskRevision, "queued", fmt.Sprintf("application runtime generation %d", generation)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) currentCatalogManifest(ctx context.Context, tx *sql.Tx, appKey string) (catalog.AppManifest, error) {
	sourceID, appID, ok := strings.Cut(appKey, "/")
	if !ok || sourceID == "" || appID == "" {
		return catalog.AppManifest{}, errors.New("center: invalid application key during runtime migration")
	}
	var envelopeJSON, publicKey []byte
	if err := tx.QueryRowContext(ctx, `SELECT cache.envelope, source.public_key FROM catalog_cache cache JOIN catalog_sources source ON source.id = cache.source_id WHERE source.id = ? AND source.enabled = 1`, sourceID).Scan(&envelopeJSON, &publicKey); err != nil {
		return catalog.AppManifest{}, fmt.Errorf("center: read current catalog for runtime migration: %w", err)
	}
	envelope, err := catalog.ParseEnvelope(envelopeJSON)
	if err != nil {
		return catalog.AppManifest{}, err
	}
	verified, _, err := catalog.Verify(envelope, ed25519.PublicKey(publicKey))
	if err != nil {
		return catalog.AppManifest{}, fmt.Errorf("center: verify current catalog for runtime migration: %w", err)
	}
	for _, manifest := range verified.Apps {
		if manifest.ID == appID {
			return manifest, nil
		}
	}
	return catalog.AppManifest{}, errors.New("center: application is unavailable in the current catalog during runtime migration")
}
