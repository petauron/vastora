package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/petauron/catalog/catalog"
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
		WHERE gateway_node_id = ? AND desired_status = 'running' AND status = 'ready' AND generation=applied_generation
		AND NOT EXISTS(SELECT 1 FROM gateway_states g WHERE g.gateway_node_id=gateway_components.gateway_node_id AND (g.status<>'ready' OR g.desired_revision<>g.applied_revision))`, now.Format(time.RFC3339Nano), agentID)
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
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_listener_states WHERE node_id = ? AND status='ready' AND desired_revision=applied_revision)`, agentID).Scan(&nodeListenerExists); err != nil {
		return err
	}
	if nodeListenerExists {
		if err := s.queueNodeListenerState(ctx, tx, agentID, now); err != nil {
			return err
		}
	}
	var tunnelExists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cloudflare_tunnels WHERE agent_id = ? AND status='ready' AND desired_revision=applied_revision)`, agentID).Scan(&tunnelExists); err != nil {
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
	// Legacy proxy installations are input to the explicit Meridian cutover.
	// A generic Agent runtime upgrade must not reinstall their removed package
	// or replace their running state before the subscription handoff is verified.
	rows, err := tx.QueryContext(ctx, `SELECT a.id, a.app_key, d.id, d.app_version, d.manifest_json, d.config_json, d.service_address, d.secret_id, d.registry_credential_id
		FROM applications a
		JOIN deployments d ON d.rowid = (
			SELECT previous.rowid FROM deployments previous
			WHERE previous.application_id = a.id
			ORDER BY previous.updated_at DESC, previous.rowid DESC LIMIT 1
		)
		WHERE a.node_id = ? AND a.status = 'running' AND a.runtime_generation < ?
		AND a.app_key <> ?
		AND EXISTS (SELECT 1 FROM application_resources resource WHERE resource.application_id=a.id AND resource.adoption_state='ready' AND resource.package_revision>0)
		AND d.state = 'succeeded' AND d.operation IN ('install', 'upgrade', 'configure')
		AND NOT EXISTS (
			SELECT 1 FROM deployments active WHERE active.application_id = a.id
			AND (active.state IN ('pending', 'running') OR active.reconciliation_required = 1)
		)
		ORDER BY a.id`, agentID, generation, threeXUIAppKey)
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
		manifestJSON := app.manifestJSON
		appVersion := app.appVersion
		if app.registryCredentialID.Valid {
			var manifest catalog.AppManifest
			if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
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
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'configure', 0, 'pending', '', ?, ?, ?, ?)`, deploymentID, agentID, app.appKey, appVersion, manifestJSON, app.configJSON, app.serviceAddress, newSecretID, nullableSQLString(app.registryCredentialID), formattedNow, formattedNow, app.applicationID, generation); err != nil {
			return fmt.Errorf("center: queue application runtime migration: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET package_revision=(SELECT package_revision FROM deployments WHERE id=?),manifest_sha256=(SELECT manifest_sha256 FROM deployments WHERE id=?),authorized_capabilities_json=(SELECT authorized_capabilities_json FROM deployments WHERE id=?) WHERE id=?`, app.deploymentID, app.deploymentID, app.deploymentID, deploymentID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'pending', updated_at = ? WHERE id = ?`, formattedNow, app.applicationID); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, deploymentID, agentID, "application.apply", applicationTaskRevision, "queued", fmt.Sprintf("application runtime generation %d", generation)); err != nil {
			return err
		}
	}
	return nil
}

func currentOfficialApplicationManifest(ctx context.Context, tx *sql.Tx, appID string, now time.Time) (catalog.AppManifest, error) {
	value, _, err := readAcceptedOfficialCatalog(ctx, tx, "stable")
	if err != nil {
		return catalog.AppManifest{}, err
	}
	for _, manifest := range value.Apps {
		if manifest.ID != appID {
			continue
		}
		canonical, err := catalog.CanonicalAppManifest(manifest)
		if err != nil {
			return catalog.AppManifest{}, err
		}
		if err := authorizeOfficialManifest(ctx, tx, "stable", canonical, now); err != nil {
			return catalog.AppManifest{}, err
		}
		return canonical, nil
	}
	return catalog.AppManifest{}, fmt.Errorf("official application %q was not found", appID)
}
