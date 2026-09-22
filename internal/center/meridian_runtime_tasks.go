package center

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/secret"
)

type meridianRuntimeProjection struct {
	task               meridianruntime.Task
	agentID            string
	serviceID          string
	materials          []meridian.CredentialMaterial
	readyRouteGrantIDs []string
	blockedRouteGrants map[string]string
}

const (
	meridianRoutePeerUnavailable = "Landing runtime is unavailable; this fixed egress remains blocked."
	meridianRouteAccessBlocked   = "Account access or quota currently blocks this fixed egress."
)

func (s *Store) resetDueMeridianAccounts(ctx context.Context, tx *sql.Tx) error {
	now := s.now().UTC()
	nowText := now.Format(time.RFC3339Nano)
	rows, err := tx.QueryContext(ctx, `SELECT id,reset_days FROM meridian_accounts
		WHERE reset_days>0 AND status='active' AND next_reset_at<>'' AND next_reset_at<=? ORDER BY id`, nowText)
	if err != nil {
		return err
	}
	type dueAccount struct {
		id   string
		days int
	}
	due := []dueAccount{}
	for rows.Next() {
		var account dueAccount
		if err := rows.Scan(&account.id, &account.days); err != nil {
			rows.Close()
			return err
		}
		due = append(due, account)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, account := range due {
		if account.days < 1 || account.days > meridian.MaxResetDays {
			return errors.New("center: stored Meridian reset schedule is invalid")
		}
		if err := s.ensureMeridianSubscriptionSnapshotsForAccountEndpointsInTx(ctx, tx, account.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_usage_watermarks SET baseline_bytes=observed_bytes,observed_at=?
			WHERE credential_id IN (SELECT id FROM meridian_credentials WHERE account_id=?)`, nowText, account.id); err != nil {
			return err
		}
		next := now.AddDate(0, 0, account.days).Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_accounts SET desired_revision=desired_revision+1,last_reset_at=?,next_reset_at=?,last_error='',updated_at=? WHERE id=?`, nowText, next, nowText, account.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=?
			WHERE status<>'retired' AND id IN (SELECT endpoint_id FROM meridian_credentials WHERE account_id=?)`, nowText, account.id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET desired_revision=desired_revision+1,runtime_healthy=0,
			status=CASE WHEN status='revoked' THEN status ELSE 'pending' END,last_error='',updated_at=? WHERE account_id=?`, nowText, account.id); err != nil {
			return err
		}
	}
	expiredRows, err := tx.QueryContext(ctx, `SELECT id FROM meridian_accounts
		WHERE status='active' AND enabled=1 AND expiry_time>0 AND expiry_time<=? ORDER BY id`, now.UnixMilli())
	if err != nil {
		return err
	}
	expired := []string{}
	for expiredRows.Next() {
		var accountID string
		if err := expiredRows.Scan(&accountID); err != nil {
			expiredRows.Close()
			return err
		}
		expired = append(expired, accountID)
	}
	if err := expiredRows.Err(); err != nil {
		expiredRows.Close()
		return err
	}
	if err := expiredRows.Close(); err != nil {
		return err
	}
	for _, accountID := range expired {
		if err := s.ensureMeridianSubscriptionSnapshotsForAccountEndpointsInTx(ctx, tx, accountID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_accounts SET enabled=0,status='expired',desired_revision=desired_revision+1,last_error='',updated_at=? WHERE id=?`, nowText, accountID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=?
			WHERE status<>'retired' AND id IN (SELECT endpoint_id FROM meridian_credentials WHERE account_id=?)`, nowText, accountID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET desired_revision=desired_revision+1,runtime_healthy=0,
			status=CASE WHEN status='revoked' THEN status ELSE 'pending' END,last_error='',updated_at=? WHERE account_id=?`, nowText, accountID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queueNextMeridianRuntime(ctx context.Context, tx *sql.Tx, agentID string) error {
	var endpointID string
	err := tx.QueryRowContext(ctx, `SELECT endpoint.id
		FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id
		WHERE application.node_id=? AND application.app_key=? AND application.status='running'
		AND NOT EXISTS (
		 SELECT 1 FROM application_commands command
		 WHERE command.agent_id=? AND (command.state IN ('pending','running') OR command.reconciliation_required=1)
		 AND command.kind NOT IN ('3xui.controller.manage','pulse.enrollment.create')
		)
		AND endpoint.status='pending' AND endpoint.desired_revision>endpoint.applied_revision
		AND NOT EXISTS (
		 SELECT 1 FROM meridian_deployments deployment
		 WHERE deployment.endpoint_id=endpoint.id AND deployment.desired_revision=endpoint.desired_revision
		 AND deployment.status IN ('pending','applying')
		)
		ORDER BY endpoint.updated_at,endpoint.id LIMIT 1`, agentID, meridianAppKey, agentID).Scan(&endpointID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = s.queueMeridianRuntime(ctx, tx, endpointID, false)
	if err == nil {
		return nil
	}
	// A malformed or temporarily undeployable Meridian projection belongs to
	// this endpoint. Persist that diagnostic and release the Agent claim loop;
	// otherwise one bad route peer or certificate would make every unrelated
	// task on the node return HTTP 500 forever. Recovery remains explicit: a
	// management mutation (or the cutover resume action) creates a new revision.
	message := applicationCommandFailureMessage(err)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, updateErr := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET status='failed',runtime_healthy=0,last_error=?,updated_at=? WHERE id=? AND status='pending'`, message, now, endpointID); updateErr != nil {
		return updateErr
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET status=CASE WHEN status='revoked' THEN status ELSE 'failed' END,runtime_healthy=0,last_error=?,updated_at=? WHERE endpoint_id=? AND status<>'revoked'`, message, now, endpointID); updateErr != nil {
		return updateErr
	}
	if _, updateErr := tx.ExecContext(ctx, `UPDATE meridian_cutover SET last_error=?,updated_at=? WHERE id=1 AND state IN ('project','verify','retire')`, message, now); updateErr != nil {
		return updateErr
	}
	return errApplicationCommandDiscarded
}

// queueMeridianRuntime stores only a secret-free endpoint pointer. The exact
// artifact is rebuilt inside the claim transaction and must retain the digest
// committed here; a concurrent state change therefore fails closed instead of
// delivering a mixed revision to the Agent.
func (s *Store) queueMeridianRuntime(ctx context.Context, tx *sql.Tx, endpointID string, replacePendingState bool) (string, error) {
	projection, err := s.buildMeridianRuntimeTask(ctx, tx, endpointID, "")
	if err != nil {
		return "", err
	}
	command := meridianruntime.Command{EndpointID: endpointID, ReplacePendingState: replacePendingState}
	encoded, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	id, err := randomToken(18)
	if err != nil {
		return "", err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	var siteID, displayName string
	if err := tx.QueryRowContext(ctx, `SELECT application.site_id,application.name FROM applications application WHERE application.id=?`, projection.task.ApplicationID).Scan(&siteID, &displayName); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?, 'pending',?,?)`, id, projection.task.ApplicationID, siteID, displayName, projection.agentID, projection.agentID, meridianruntime.ApplyKind, encoded, now, now); err != nil {
		return "", fmt.Errorf("center: queue Meridian runtime: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_deployments(endpoint_id,desired_revision,desired_sha256,command_id,status,updated_at)
		VALUES(?,?,?,?, 'pending',?)
		ON CONFLICT(endpoint_id) DO UPDATE SET desired_revision=excluded.desired_revision,desired_sha256=excluded.desired_sha256,
		command_id=excluded.command_id,status='pending',last_error='',updated_at=excluded.updated_at`, endpointID, projection.task.Desired.Revision, projection.task.Desired.ConfigSHA256, id, now); err != nil {
		return "", err
	}
	if projection.task.RetireLegacy {
		// Legacy retirement changes only the old installation receipt and
		// temporary container aliases. The currently applied Meridian artifact
		// already passed the public cutover gates, so merely queueing cleanup must
		// not remove this entry or its routes from subscriptions.
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET last_error='',updated_at=? WHERE id=? AND status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision`, now, endpointID)
		if err != nil {
			return "", err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return "", errors.New("center: verified Meridian endpoint changed before legacy retirement was queued")
		}
	} else {
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET status='pending',runtime_healthy=0,last_error='',updated_at=? WHERE id=? AND status<>'retired'`, now, endpointID)
		if err != nil {
			return "", err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return "", errors.New("center: Meridian endpoint changed before its runtime revision was queued")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET status=CASE WHEN status='revoking' THEN status WHEN enabled=1 THEN 'pending' ELSE status END,runtime_healthy=0,last_error='',updated_at=? WHERE endpoint_id=? AND status<>'revoked'`, now, endpointID); err != nil {
			return "", err
		}
		for grantID, message := range projection.blockedRouteGrants {
			updated, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET status='blocked',runtime_healthy=0,last_error=?,updated_at=?
				WHERE id=? AND endpoint_id=? AND enabled=1 AND status<>'revoked'`, message, now, grantID, endpointID)
			if err != nil {
				return "", err
			}
			if changed, _ := updated.RowsAffected(); changed != 1 {
				return "", errors.New("center: Meridian blocked route changed before its runtime revision was queued")
			}
		}
	}
	if err := s.recordTaskEvent(ctx, tx, id, projection.agentID, "application.command", int64(projection.task.Desired.Revision), "queued", "Meridian runtime revision queued"); err != nil {
		return "", err
	}
	return id, nil
}

// discardSupersededMeridianRuntimeCommand closes a queued command whose
// endpoint changed before the Agent could claim it. The latest endpoint stays
// pending and is projected on the next heartbeat instead of being failed by a
// digest that correctly belongs to the older revision.
func (s *Store) discardSupersededMeridianRuntimeCommand(ctx context.Context, tx *sql.Tx, commandID, agentID, endpointID string) (bool, error) {
	var currentRevision, commandRevision int64
	err := tx.QueryRowContext(ctx, `SELECT endpoint.desired_revision,deployment.desired_revision
		FROM meridian_endpoints endpoint JOIN meridian_deployments deployment ON deployment.endpoint_id=endpoint.id
		WHERE endpoint.id=? AND deployment.command_id=?`, endpointID, commandID).Scan(&currentRevision, &commandRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if commandRevision == currentRevision {
		return false, nil
	}
	if commandRevision < 1 || commandRevision > currentRevision {
		return false, errors.New("center: stored Meridian deployment revision is invalid")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	message := "Meridian runtime revision superseded before Agent claim"
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_deployments SET status='failed',last_error=?,updated_at=? WHERE endpoint_id=? AND command_id=? AND desired_revision=?`, message, now, endpointID, commandID, commandRevision); err != nil {
		return true, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=? AND status<>'retired' AND desired_revision=?`, now, endpointID, currentRevision); err != nil {
		return true, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE application_commands SET state='succeeded',result_json='{}',lease_expires_at='',error='',updated_at=? WHERE id=? AND state='pending'`, now, commandID)
	if err != nil {
		return true, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return true, errors.New("center: superseded Meridian command is no longer pending")
	}
	if err := s.recordTaskEvent(ctx, tx, commandID, agentID, "application.command", commandRevision, "succeeded", message); err != nil {
		return true, err
	}
	return true, errApplicationCommandDiscarded
}

func (s *Store) buildMeridianRuntimeTask(ctx context.Context, tx *sql.Tx, endpointID, expectedAgentID string) (meridianRuntimeProjection, error) {
	var projection meridianRuntimeProjection
	var endpoint meridian.RealityEndpoint
	var hysteriaEndpoint meridian.HysteriaEndpoint
	var serverNamesJSON, shortIDsJSON []byte
	var privateKeySecretID, expectedSHA, hy2InboundTag, hy2ServerName, hy2NotAfter, endpointStatus string
	var hy2CertificateSecretID, hy2PrivateKeySecretID sql.NullString
	var vlessEnabled, hy2Enabled, runtimeHealthy, legacyRetired int
	var revision, appliedRevision int64
	err := tx.QueryRowContext(ctx, `SELECT endpoint.application_id,application.node_id,application.image,endpoint.service_id,
		endpoint.inbound_tag,endpoint.listen_port,endpoint.advertise_host,endpoint.advertise_port,endpoint.target,
		endpoint.server_names_json,endpoint.private_key_secret_id,endpoint.public_key,endpoint.short_ids_json,endpoint.fingerprint,
		endpoint.vless_enabled,endpoint.hy2_enabled,endpoint.hy2_inbound_tag,endpoint.hy2_server_name,endpoint.hy2_certificate_secret_id,endpoint.hy2_private_key_secret_id,endpoint.hy2_certificate_not_after,
		endpoint.desired_revision,endpoint.applied_revision,endpoint.runtime_healthy,endpoint.legacy_retired,endpoint.status,COALESCE(deployment.desired_sha256,'')
		FROM meridian_endpoints endpoint
		JOIN applications application ON application.id=endpoint.application_id AND application.app_key=? AND application.status='running'
		LEFT JOIN meridian_deployments deployment ON deployment.endpoint_id=endpoint.id AND deployment.desired_revision=endpoint.desired_revision
		WHERE endpoint.id=? AND endpoint.status<>'retired'`, meridianAppKey, endpointID).Scan(
		&projection.task.ApplicationID, &projection.agentID, &projection.task.ImageReference, &projection.serviceID,
		&endpoint.InboundTag, &endpoint.ListenPort, &endpoint.AdvertiseHost, &endpoint.AdvertisePort, &endpoint.Target,
		&serverNamesJSON, &privateKeySecretID, &endpoint.PublicKey, &shortIDsJSON, &endpoint.Fingerprint,
		&vlessEnabled, &hy2Enabled, &hy2InboundTag, &hy2ServerName, &hy2CertificateSecretID, &hy2PrivateKeySecretID, &hy2NotAfter,
		&revision, &appliedRevision, &runtimeHealthy, &legacyRetired, &endpointStatus, &expectedSHA,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return projection, errors.New("center: Meridian endpoint is not deployable")
	}
	if err != nil {
		return projection, fmt.Errorf("center: read Meridian endpoint: %w", err)
	}
	if expectedAgentID != "" && projection.agentID != expectedAgentID {
		return projection, errors.New("center: Meridian endpoint moved to another Agent")
	}
	if expectedAgentID != "" && expectedSHA == "" {
		return projection, errors.New("center: Meridian deployment digest is unavailable")
	}
	endpoint.ID, endpoint.EntryID = endpointID, projection.task.ApplicationID
	if revision < 1 || json.Unmarshal(serverNamesJSON, &endpoint.ServerNames) != nil || json.Unmarshal(shortIDsJSON, &endpoint.ShortIDs) != nil {
		return projection, errors.New("center: stored Meridian endpoint is invalid")
	}
	privateKey, err := s.meridianSecretInTx(ctx, tx, privateKeySecretID, meridianEndpointSecretContext(endpointID))
	if err != nil {
		return projection, errors.New("center: stored Meridian endpoint key is unavailable")
	}
	endpoint.PrivateKey = string(privateKey)
	if endpoint.Validate() != nil || vlessEnabled == 0 && hy2Enabled == 0 {
		return projection, errors.New("center: stored Meridian endpoint is invalid")
	}
	realityEndpoints := []meridian.RealityEndpoint{}
	if vlessEnabled == 1 {
		realityEndpoints = append(realityEndpoints, endpoint)
	}
	hysteriaEndpoints := []meridian.HysteriaEndpoint{}
	if hy2Enabled == 1 {
		expiresAt, parseErr := time.Parse(time.RFC3339Nano, hy2NotAfter)
		if parseErr != nil || !expiresAt.After(s.now().Add(time.Hour)) {
			return projection, errors.New("center: stored Meridian Hysteria certificate is unavailable or expired")
		}
		if !hy2CertificateSecretID.Valid || !hy2PrivateKeySecretID.Valid {
			return projection, errors.New("center: stored Meridian Hysteria certificate is unavailable")
		}
		certificate, secretErr := s.meridianSecretInTx(ctx, tx, hy2CertificateSecretID.String, meridianHY2CertificateSecretContext(endpointID))
		if secretErr != nil {
			return projection, errors.New("center: stored Meridian Hysteria certificate is unavailable")
		}
		hy2PrivateKey, secretErr := s.meridianSecretInTx(ctx, tx, hy2PrivateKeySecretID.String, meridianHY2PrivateKeySecretContext(endpointID))
		if secretErr != nil {
			return projection, errors.New("center: stored Meridian Hysteria key is unavailable")
		}
		hysteriaEndpoint = meridian.HysteriaEndpoint{ID: endpointID + "-hy2", EntryID: projection.task.ApplicationID, InboundTag: hy2InboundTag, ListenPort: 443, AdvertiseHost: endpoint.AdvertiseHost, AdvertisePort: 443, ServerName: hy2ServerName, CertificatePEM: string(certificate), PrivateKeyPEM: string(hy2PrivateKey)}
		if hysteriaEndpoint.Validate() != nil {
			return projection, errors.New("center: stored Meridian Hysteria endpoint is invalid")
		}
		hysteriaEndpoints = append(hysteriaEndpoints, hysteriaEndpoint)
	}

	materials, credentialByID, err := s.meridianRuntimeMaterials(ctx, tx, endpointID, projection.task.ApplicationID)
	if err != nil {
		return projection, err
	}
	routeInboundTag := ""
	if vlessEnabled == 1 {
		routeInboundTag = endpoint.InboundTag
	}
	routes, err := s.meridianRuntimeGrants(ctx, tx, endpointID, routeInboundTag, credentialByID)
	if err != nil {
		return projection, err
	}
	for index := range materials {
		if routes.disabledCredentialIDs[materials[index].Credential.ID] {
			materials[index].Credential.Enabled = false
		}
	}
	artifact, err := meridian.BuildDesiredArtifact(meridian.XrayPlan{
		Revision: uint64(revision), RealityEndpoints: realityEndpoints, HysteriaEndpoints: hysteriaEndpoints, Credentials: materials, Grants: routes.grants, Peers: routes.peers,
	})
	if err != nil || expectedSHA != "" && artifact.ConfigSHA256 != expectedSHA {
		return projection, errors.New("center: Meridian desired state changed after it was queued")
	}
	projection.task.Desired, projection.materials = artifact, materials
	projection.readyRouteGrantIDs, projection.blockedRouteGrants = routes.readyGrantIDs, routes.blockedGrants
	var cutoverState, subscriptionAuthority, importSHA, importSecretID string
	var backupRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT state,subscription_authority,import_sha256,COALESCE(import_secret_id,''),backup_revision FROM meridian_cutover WHERE id=1`).Scan(&cutoverState, &subscriptionAuthority, &importSHA, &importSecretID, &backupRevision); err != nil {
		return projection, err
	}
	// Retirement replays only an already verified artifact. A route or landing
	// mutation during the retirement phase must first apply as a normal runtime
	// revision while retaining the migration aliases; otherwise the recovery
	// task itself could retire the last known legacy boundary before the new
	// route is healthy.
	retirePhase := cutoverState == "retire" && subscriptionAuthority == "meridian" && backupRevision > 0 && len(importSHA) == 64 && importSecretID != ""
	var unreadyRoutes int
	if retirePhase && legacyRetired == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants
			WHERE endpoint_id=? AND enabled=1 AND status<>'revoked'
			AND (status<>'ready' OR runtime_healthy<>1 OR desired_revision<>applied_revision)`, endpointID).Scan(&unreadyRoutes); err != nil {
			return projection, err
		}
	}
	projection.task.RetireLegacy = retirePhase && legacyRetired == 0 && endpointStatus == "ready" && runtimeHealthy == 1 && appliedRevision == revision && unreadyRoutes == 0
	projection.task.PreserveLegacyAliases = subscriptionAuthority == "meridian" && legacyRetired == 0 &&
		((cutoverState == "project" || cutoverState == "verify") || retirePhase && !projection.task.RetireLegacy)
	if projection.task.Validate() != nil {
		return projection, errors.New("center: Meridian runtime task is invalid")
	}
	return projection, nil
}

func (s *Store) meridianRuntimeMaterials(ctx context.Context, tx *sql.Tx, endpointID, entryID string) ([]meridian.CredentialMaterial, map[string]meridian.CredentialMaterial, error) {
	rows, err := tx.QueryContext(ctx, `SELECT credential.id,credential.account_id,credential.kind,credential.user_name,credential.identity_sha256,
		credential.protocol_secret_id,credential.hy2_auth_secret_id,credential.hy2_identity_sha256,credential.egress_node_id,credential.enabled,
		account.total_bytes,account.expiry_time,account.reset_days,account.enabled,account.status
		FROM meridian_credentials credential JOIN meridian_accounts account ON account.id=credential.account_id
		WHERE credential.endpoint_id=? ORDER BY credential.id`, endpointID)
	if err != nil {
		return nil, nil, err
	}
	type stored struct {
		material    meridian.CredentialMaterial
		secretID    string
		hy2SecretID sql.NullString
		plan        meridian.AccountPlan
		status      string
	}
	storedValues := []stored{}
	accountPlans := map[string]meridian.AccountPlan{}
	accountStatus := map[string]string{}
	for rows.Next() {
		var value stored
		var egress sql.NullString
		var credentialEnabled, accountEnabled int
		if err := rows.Scan(&value.material.Credential.ID, &value.material.Credential.AccountID, &value.material.Credential.Kind,
			&value.material.Credential.User, &value.material.Credential.Identity, &value.secretID, &value.hy2SecretID, &value.material.HysteriaIdentity, &egress, &credentialEnabled,
			&value.plan.TotalBytes, &value.plan.ExpiryTime, &value.plan.ResetDays, &accountEnabled, &value.status); err != nil {
			rows.Close()
			return nil, nil, err
		}
		value.material.Credential.EntryID = entryID
		value.material.Credential.Enabled = credentialEnabled == 1
		if egress.Valid {
			value.material.Credential.EgressID = egress.String
		}
		value.plan.Enabled = accountEnabled == 1 && value.status == "active" && (value.plan.ExpiryTime == 0 || value.plan.ExpiryTime > s.now().UnixMilli())
		accountPlans[value.material.Credential.AccountID] = value.plan
		accountStatus[value.material.Credential.AccountID] = value.status
		storedValues = append(storedValues, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	quotaEnabled := map[string]bool{}
	for accountID, plan := range accountPlans {
		usage, err := meridianAccountUsageInTx(ctx, tx, accountID)
		if err != nil {
			return nil, nil, err
		}
		projection, err := meridian.ProjectQuota(plan, usage)
		status := accountStatus[accountID]
		if err != nil || status != "active" && status != "disabled" && status != "expired" {
			return nil, nil, errors.New("center: stored Meridian quota is invalid")
		}
		quotaEnabled[accountID] = status == "active" && projection.Enabled
	}
	materials := []meridian.CredentialMaterial{}
	byID := map[string]meridian.CredentialMaterial{}
	for _, value := range storedValues {
		protocolID, err := s.meridianSecretInTx(ctx, tx, value.secretID, meridianCredentialSecretContext(value.material.Credential.ID))
		if err != nil {
			return nil, nil, errors.New("center: stored Meridian credential is unavailable")
		}
		value.material.ProtocolID = string(protocolID)
		if value.hy2SecretID.Valid {
			hy2Auth, secretErr := s.meridianSecretInTx(ctx, tx, value.hy2SecretID.String, meridianCredentialHY2SecretContext(value.material.Credential.ID))
			if secretErr != nil {
				return nil, nil, errors.New("center: stored Meridian Hysteria credential is unavailable")
			}
			value.material.HysteriaAuth = string(hy2Auth)
		}
		if value.material.Validate() != nil {
			return nil, nil, errors.New("center: stored Meridian credential is invalid")
		}
		value.material.Credential.Enabled = value.material.Credential.Enabled && quotaEnabled[value.material.Credential.AccountID]
		byID[value.material.Credential.ID] = value.material
		materials = append(materials, value.material)
	}
	return materials, byID, nil
}

type meridianRuntimeRoutes struct {
	grants                []meridian.RouteGrant
	peers                 []meridian.RoutePeer
	readyGrantIDs         []string
	blockedGrants         map[string]string
	disabledCredentialIDs map[string]bool
}

func (s *Store) meridianRuntimeGrants(ctx context.Context, tx *sql.Tx, endpointID, inboundTag string, credentials map[string]meridian.CredentialMaterial) (meridianRuntimeRoutes, error) {
	rows, err := tx.QueryContext(ctx, `SELECT grant_row.id,grant_row.account_id,grant_row.egress_node_id,grant_row.base_credential_id,grant_row.route_credential_id,
		grant_row.mode,grant_row.hide_native,grant_row.enabled,grant_row.desired_revision,grant_row.applied_revision,grant_row.runtime_healthy,grant_row.status,
		CASE WHEN agent.id IS NULL THEN NULL ELSE server.peer_json END
		FROM meridian_route_grants grant_row
		LEFT JOIN landing_server_states server ON server.node_id=grant_row.egress_node_id
		 AND server.status='ready' AND server.desired_revision=server.applied_revision
		LEFT JOIN agents agent ON agent.id=grant_row.egress_node_id AND agent.status='active' AND agent.credential_revoked_at=''
		WHERE grant_row.endpoint_id=? AND grant_row.status<>'revoked' ORDER BY grant_row.id`, endpointID)
	if err != nil {
		return meridianRuntimeRoutes{}, err
	}
	result := meridianRuntimeRoutes{blockedGrants: map[string]string{}, disabledCredentialIDs: map[string]bool{}}
	peersByID := map[string]meridian.RoutePeer{}
	for rows.Next() {
		var grant meridian.RouteGrant
		var baseID, routeID, mode, status string
		var hideNative, enabled, runtimeHealthy int
		var peerJSON []byte
		if err := rows.Scan(&grant.ID, &grant.AccountID, &grant.EgressID, &baseID, &routeID, &mode, &hideNative, &enabled,
			&grant.DesiredRev, &grant.AppliedRev, &runtimeHealthy, &status, &peerJSON); err != nil {
			rows.Close()
			return meridianRuntimeRoutes{}, err
		}
		base, baseOK := credentials[baseID]
		route, routeOK := credentials[routeID]
		if !baseOK || !routeOK || base.Credential.AccountID != grant.AccountID || route.Credential.AccountID != grant.AccountID {
			rows.Close()
			return meridianRuntimeRoutes{}, errors.New("center: stored Meridian route credentials are incomplete")
		}
		grant.EntryID, grant.InboundTag = base.Credential.EntryID, inboundTag
		grant.Base, grant.Route = base.Credential, route.Credential
		grant.Mode = meridian.PublishingMode(mode)
		grant.HideNative, grant.Enabled = hideNative == 1, enabled == 1
		grant.RuntimeGood = runtimeHealthy == 1 && status == "ready"
		if !grant.Enabled {
			continue
		}
		if !grant.Base.Enabled || !grant.Route.Enabled {
			result.blockedGrants[grant.ID] = meridianRouteAccessBlocked
			result.disabledCredentialIDs[grant.Route.ID] = true
			continue
		}
		if inboundTag == "" {
			rows.Close()
			return meridianRuntimeRoutes{}, errors.New("center: Meridian routes require a VLESS REALITY entry")
		}
		if len(peerJSON) == 0 {
			result.blockedGrants[grant.ID] = meridianRoutePeerUnavailable
			result.disabledCredentialIDs[grant.Route.ID] = true
			continue
		}
		var peer landing.PeerIdentity
		if json.Unmarshal(peerJSON, &peer) != nil || peer.ID != grant.EgressID {
			rows.Close()
			return meridianRuntimeRoutes{}, errors.New("center: stored Meridian route peer is invalid")
		}
		projectedPeer := meridian.RoutePeer{EgressID: peer.ID, Address: peer.Address, Port: landing.SOCKSPort}
		if grant.Validate() != nil || !grant.Deployable() || projectedPeer.Validate() != nil {
			rows.Close()
			return meridianRuntimeRoutes{}, errors.New("center: stored Meridian route is invalid")
		}
		result.grants = append(result.grants, grant)
		result.readyGrantIDs = append(result.readyGrantIDs, grant.ID)
		peersByID[projectedPeer.EgressID] = projectedPeer
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return meridianRuntimeRoutes{}, err
	}
	if err := rows.Close(); err != nil {
		return meridianRuntimeRoutes{}, err
	}
	result.peers = make([]meridian.RoutePeer, 0, len(peersByID))
	for _, peer := range peersByID {
		result.peers = append(result.peers, peer)
	}
	slices.SortFunc(result.peers, func(a, b meridian.RoutePeer) int { return strings.Compare(a.EgressID, b.EgressID) })
	return result, nil
}

func meridianAccountUsageInTx(ctx context.Context, tx *sql.Tx, accountID string) ([]meridian.UsageMember, error) {
	rows, err := tx.QueryContext(ctx, `SELECT credential.id,watermark.baseline_bytes,watermark.observed_bytes,credential.enabled
		FROM meridian_credentials credential LEFT JOIN meridian_usage_watermarks watermark ON watermark.credential_id=credential.id
		WHERE credential.account_id=? ORDER BY credential.id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	usage := []meridian.UsageMember{}
	for rows.Next() {
		var value meridian.UsageMember
		var baseline, observed sql.NullInt64
		var active int
		if err := rows.Scan(&value.CredentialID, &baseline, &observed, &active); err != nil {
			return nil, err
		}
		if !baseline.Valid || !observed.Valid {
			return nil, errors.New("center: Meridian usage watermark is missing")
		}
		value.Baseline, value.Observed, value.Active = baseline.Int64, observed.Int64, active == 1
		usage = append(usage, value)
	}
	if err := rows.Err(); err != nil || len(usage) == 0 {
		return nil, errors.Join(errors.New("center: Meridian account has no usage authority"), err)
	}
	return usage, nil
}

func (s *Store) meridianSecretInTx(ctx context.Context, tx *sql.Tx, id, additionalData string) ([]byte, error) {
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id=?`, id).Scan(&sealed); err != nil {
		return nil, err
	}
	return secret.Open(s.key, sealed, []byte(additionalData))
}

func (s *Store) completeMeridianRuntimeCommand(ctx context.Context, commit projectionCommit, tx *sql.Tx, taskID, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var command meridianruntime.Command
	if json.Unmarshal(inputJSON, &command) != nil || command.Validate() != nil {
		return errors.New("center: stored Meridian runtime command is invalid")
	}
	if handled, err := s.completeSupersededMeridianRuntimeCommand(ctx, commit, tx, taskID, agentID, command.EndpointID, succeeded, taskError, rawResult); handled || err != nil {
		return err
	}
	projection, projectionErr := s.buildMeridianRuntimeTask(ctx, tx, command.EndpointID, agentID)
	var envelope ApplicationTaskResult
	if succeeded {
		if projectionErr != nil {
			succeeded, taskError = false, projectionErr.Error()
		} else if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.MeridianRuntime == nil || envelope.MeridianRuntime.Validate(projection.task.Desired) != nil || projection.task.RetireLegacy && !envelope.MeridianRuntime.LegacyRetired {
			succeeded, taskError = false, "center: Agent returned an invalid Meridian runtime receipt"
		}
	}
	if !succeeded && taskError == "" {
		taskError = "Meridian runtime apply failed"
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	state, event, message := "succeeded", "succeeded", "Meridian runtime revision applied"
	resultJSON := []byte(`{}`)
	if succeeded {
		snapshots, err := meridian.ParseXrayUserCounters(projection.materials, envelope.MeridianRuntime.Stats)
		if err != nil {
			succeeded, taskError = false, "center: Agent returned invalid Meridian usage counters"
		} else if err := s.observeMeridianUsageInTx(ctx, tx, snapshots, now); err != nil {
			return err
		} else {
			resultJSON, _ = json.Marshal(envelope.MeridianRuntime.Receipt)
		}
	}
	if succeeded {
		revision := int64(projection.task.Desired.Revision)
		legacyRetired := 0
		if envelope.MeridianRuntime.LegacyRetired {
			legacyRetired = 1
		}
		endpointUpdate, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET applied_revision=?,runtime_healthy=1,legacy_retired=?,status='ready',last_error='',updated_at=? WHERE id=? AND desired_revision=?`, revision, legacyRetired, now, command.EndpointID, revision)
		if err != nil {
			return err
		}
		if changed, _ := endpointUpdate.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian endpoint changed before its runtime receipt was committed")
		}
		serviceUpdate, err := tx.ExecContext(ctx, `UPDATE services SET endpoint=?,status='ready',last_error='',updated_at=?
			WHERE id=? AND application_id=? AND status<>'stopped'`, net.JoinHostPort(dockerruntime.MeridianAlias, "443"), now, projection.serviceID, projection.task.ApplicationID)
		if err != nil {
			return err
		}
		if changed, _ := serviceUpdate.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian service changed before its runtime receipt was committed")
		}
		deploymentUpdate, err := tx.ExecContext(ctx, `UPDATE meridian_deployments SET applied_revision=?,applied_sha256=?,status='ready',last_error='',updated_at=? WHERE endpoint_id=? AND command_id=? AND desired_revision=?`, revision, projection.task.Desired.ConfigSHA256, now, command.EndpointID, taskID, revision)
		if err != nil {
			return err
		}
		if changed, _ := deploymentUpdate.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian deployment changed before its runtime receipt was committed")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,
			runtime_healthy=0,status=CASE WHEN status='revoking' THEN 'revoked' ELSE status END,
			last_error=CASE WHEN status='revoking' THEN '' ELSE last_error END,updated_at=? WHERE endpoint_id=? AND status<>'revoked'`, now, command.EndpointID); err != nil {
			return err
		}
		for _, grantID := range projection.readyRouteGrantIDs {
			updated, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=1,status='ready',last_error='',updated_at=?
				WHERE id=? AND endpoint_id=? AND enabled=1 AND status<>'revoked'`, now, grantID, command.EndpointID)
			if err != nil {
				return err
			}
			if changed, _ := updated.RowsAffected(); changed != 1 {
				return errors.New("center: Meridian ready route changed before its runtime receipt was committed")
			}
		}
		for grantID, message := range projection.blockedRouteGrants {
			updated, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET applied_revision=desired_revision,runtime_healthy=0,status='blocked',last_error=?,updated_at=?
				WHERE id=? AND endpoint_id=? AND enabled=1 AND status<>'revoked'`, message, now, grantID, command.EndpointID)
			if err != nil {
				return err
			}
			if changed, _ := updated.RowsAffected(); changed != 1 {
				return errors.New("center: Meridian blocked route changed before its runtime receipt was committed")
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_accounts SET applied_revision=desired_revision,last_error='',updated_at=?
			WHERE id IN (SELECT account_id FROM meridian_credentials WHERE endpoint_id=?)
			AND NOT EXISTS (
			 SELECT 1 FROM meridian_credentials credential JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id
			 WHERE credential.account_id=meridian_accounts.id AND credential.enabled=1 AND endpoint.status<>'retired'
			 AND (endpoint.status<>'ready' OR endpoint.runtime_healthy<>1 OR endpoint.applied_revision<>endpoint.desired_revision)
			)`, now, command.EndpointID); err != nil {
			return err
		}
		if !projection.task.RetireLegacy {
			parsedNow, err := time.Parse(time.RFC3339Nano, now)
			if err != nil {
				return err
			}
			if err := s.reconcileApplicationPublications(ctx, tx, projection.task.ApplicationID, parsedNow); err != nil {
				return err
			}
		}
	} else {
		state, event, message = "failed", "failed", taskError
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_deployments SET status='failed',last_error=?,updated_at=? WHERE endpoint_id=? AND command_id=?`, taskError, now, command.EndpointID, taskID); err != nil {
			return err
		}
		if projectionErr == nil && projection.task.RetireLegacy {
			// Retirement changes only the old installation receipt. The exact
			// Meridian revision was verified before this command was queued, so a
			// failed receipt cleanup must not take the live subscription offline.
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET last_error=?,updated_at=? WHERE id=? AND status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision`, taskError, now, command.EndpointID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=0,status='failed',last_error=?,updated_at=? WHERE id=?`, taskError, now, command.EndpointID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET runtime_healthy=0,status=CASE WHEN status='revoked' THEN status ELSE 'failed' END,last_error=?,updated_at=? WHERE endpoint_id=?`, taskError, now, command.EndpointID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET last_error=?,updated_at=?
			WHERE id=1 AND state IN ('project','verify','retire')
			AND EXISTS(SELECT 1 FROM meridian_endpoints WHERE id=?)`, taskError, now, command.EndpointID); err != nil {
			return err
		}
	}
	commandUpdate, err := tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json=?,lease_expires_at='',error=?,updated_at=? WHERE id=? AND state='running'`, state, resultJSON, taskError, now, taskID)
	if err != nil {
		return err
	}
	if changed, _ := commandUpdate.RowsAffected(); changed != 1 {
		return errors.New("center: Meridian runtime command changed before its receipt was committed")
	}
	revision := int64(1)
	if projectionErr == nil {
		revision = int64(projection.task.Desired.Revision)
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", revision, event, message); err != nil {
		return err
	}
	if succeeded {
		if err := s.reconcileMeridianCutoverInTx(ctx, tx, now); err != nil {
			return err
		}
	}
	return commit(tx)
}

// completeSupersededMeridianRuntimeCommand accepts the terminal receipt for a
// revision that was already replaced while the Agent was applying it. The old
// command is closed, but the endpoint remains pending so the next claim builds
// the newest complete projection. Treating this ordinary race as an endpoint
// failure would strand the newer desired revision indefinitely.
func (s *Store) completeSupersededMeridianRuntimeCommand(ctx context.Context, commit projectionCommit, tx *sql.Tx, taskID, agentID, endpointID string, succeeded bool, taskError string, rawResult json.RawMessage) (bool, error) {
	var currentRevision, commandRevision int64
	var expectedSHA string
	if err := tx.QueryRowContext(ctx, `SELECT endpoint.desired_revision,deployment.desired_revision,deployment.desired_sha256
		FROM meridian_endpoints endpoint JOIN meridian_deployments deployment ON deployment.endpoint_id=endpoint.id
		WHERE endpoint.id=? AND deployment.command_id=?`, endpointID, taskID).Scan(&currentRevision, &commandRevision, &expectedSHA); err != nil {
		return false, err
	}
	if commandRevision == currentRevision {
		return false, nil
	}
	if commandRevision < 1 || commandRevision > currentRevision || !meridian.ValidIdentity(expectedSHA) {
		return false, errors.New("center: stored Meridian deployment revision is invalid")
	}

	state, deploymentStatus := "succeeded", "ready"
	message := "Meridian runtime revision superseded by newer desired state"
	terminalError := ""
	resultJSON := []byte(`{}`)
	if succeeded {
		var envelope ApplicationTaskResult
		if len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.MeridianRuntime == nil || !meridianSupersededReceiptMatches(commandRevision, expectedSHA, *envelope.MeridianRuntime) {
			state, deploymentStatus = "failed", "failed"
			message = "center: Agent returned an invalid superseded Meridian runtime receipt"
			terminalError = message
		} else {
			resultJSON, _ = json.Marshal(envelope.MeridianRuntime.Receipt)
		}
	} else {
		state, deploymentStatus = "failed", "failed"
		message = applicationCommandFailureMessage(errors.New(taskError))
		if message == "" {
			message = "superseded Meridian runtime apply failed"
		}
		terminalError = message
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_deployments SET status=?,last_error=?,updated_at=? WHERE endpoint_id=? AND command_id=? AND desired_revision=?`, deploymentStatus, terminalError, now, endpointID, taskID, commandRevision); err != nil {
		return true, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=? AND status<>'retired' AND desired_revision=?`, now, endpointID, currentRevision); err != nil {
		return true, err
	}
	commandUpdate, err := tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json=?,lease_expires_at='',error=?,updated_at=? WHERE id=? AND state='running'`, state, resultJSON, terminalError, now, taskID)
	if err != nil {
		return true, err
	}
	if changed, _ := commandUpdate.RowsAffected(); changed != 1 {
		return true, errors.New("center: superseded Meridian command changed before its receipt was committed")
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", commandRevision, state, message); err != nil {
		return true, err
	}
	return true, commit(tx)
}

func meridianSupersededReceiptMatches(revision int64, expectedSHA string, result meridianruntime.Result) bool {
	receipt := result.Receipt
	return revision > 0 && receipt.Revision == uint64(revision) && receipt.RuntimeReady && meridian.ValidIdentity(receipt.ConfigSHA256) &&
		subtle.ConstantTimeCompare([]byte(expectedSHA), []byte(receipt.ConfigSHA256)) == 1 &&
		len(result.Stats) > 0 && len(result.Stats) <= 4<<20 && json.Valid(result.Stats)
}

func (s *Store) observeMeridianUsageInTx(ctx context.Context, tx *sql.Tx, snapshots []meridian.CounterSnapshot, observedAt string) error {
	for _, snapshot := range snapshots {
		var baseline, observed, rawUp, rawDown int64
		err := tx.QueryRowContext(ctx, `SELECT baseline_bytes,observed_bytes,raw_up_bytes,raw_down_bytes FROM meridian_usage_watermarks WHERE credential_id=?`, snapshot.CredentialID).Scan(&baseline, &observed, &rawUp, &rawDown)
		if errors.Is(err, sql.ErrNoRows) {
			observed = saturatingAdd(snapshot.UpBytes, snapshot.DownBytes)
			if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,baseline_bytes,observed_bytes,raw_up_bytes,raw_down_bytes,observed_at) VALUES(?,0,?,?,?,?)`, snapshot.CredentialID, observed, snapshot.UpBytes, snapshot.DownBytes, observedAt); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		upDelta := snapshot.UpBytes
		if snapshot.UpBytes >= rawUp {
			upDelta = snapshot.UpBytes - rawUp
		}
		downDelta := snapshot.DownBytes
		if snapshot.DownBytes >= rawDown {
			downDelta = snapshot.DownBytes - rawDown
		}
		nextObserved := saturatingAdd(observed, saturatingAdd(upDelta, downDelta))
		if nextObserved < baseline {
			return errors.New("center: Meridian usage watermark overflowed its baseline")
		}
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_usage_watermarks SET observed_bytes=?,raw_up_bytes=?,raw_down_bytes=?,observed_at=? WHERE credential_id=?`, nextObserved, snapshot.UpBytes, snapshot.DownBytes, observedAt, snapshot.CredentialID)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian usage watermark changed during observation")
		}
	}
	return nil
}

func (s *Store) recordMeridianRuntimeObservation(ctx context.Context, tx *sql.Tx, agentID string, result meridianruntime.Result, now time.Time) error {
	var endpointID string
	var desiredRevision, appliedRevision int64
	err := tx.QueryRowContext(ctx, `SELECT endpoint.id,endpoint.desired_revision,endpoint.applied_revision FROM meridian_endpoints endpoint
		JOIN applications application ON application.id=endpoint.application_id
		WHERE application.node_id=? AND application.app_key=? AND application.status='running' AND endpoint.status<>'retired'`, agentID, meridianAppKey).Scan(&endpointID, &desiredRevision, &appliedRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: Agent reported a Meridian runtime without an owned endpoint")
	}
	if err != nil {
		return err
	}
	if appliedRevision == 0 || appliedRevision != desiredRevision {
		return nil
	}
	projection, err := s.buildMeridianRuntimeTask(ctx, tx, endpointID, agentID)
	if err != nil || result.Validate(projection.task.Desired) != nil {
		return errors.New("center: Agent reported a Meridian runtime that does not match desired state")
	}
	before, err := s.meridianQuotaStatesInTx(ctx, tx, endpointID)
	if err != nil {
		return err
	}
	snapshots, err := meridian.ParseXrayUserCounters(projection.materials, result.Stats)
	if err != nil {
		return errors.New("center: Agent reported invalid Meridian usage counters")
	}
	nowText := now.UTC().Format(time.RFC3339Nano)
	if err := s.observeMeridianUsageInTx(ctx, tx, snapshots, nowText); err != nil {
		return err
	}
	after, err := s.meridianQuotaStatesInTx(ctx, tx, endpointID)
	if err != nil {
		return err
	}
	changedAccounts := []string{}
	for accountID, enabled := range before {
		if next, ok := after[accountID]; !ok || next != enabled {
			changedAccounts = append(changedAccounts, accountID)
		}
	}
	if len(changedAccounts) == 0 {
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=1,last_error='',updated_at=? WHERE id=? AND status='ready' AND desired_revision=applied_revision`, nowText, endpointID)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian endpoint changed during runtime observation")
		}
		return nil
	}
	return s.markMeridianQuotaBoundaryChanged(ctx, tx, changedAccounts, nowText)
}

func (s *Store) markMeridianQuotaBoundaryChanged(ctx context.Context, tx *sql.Tx, accountIDs []string, nowText string) error {
	endpointIDs := map[string]bool{}
	for _, accountID := range accountIDs {
		rows, err := tx.QueryContext(ctx, `SELECT DISTINCT credential.endpoint_id FROM meridian_credentials credential
			JOIN meridian_endpoints endpoint ON endpoint.id=credential.endpoint_id
			WHERE credential.account_id=? AND endpoint.status<>'retired' ORDER BY credential.endpoint_id`, accountID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var affectedEndpointID string
			if err := rows.Scan(&affectedEndpointID); err != nil {
				rows.Close()
				return err
			}
			endpointIDs[affectedEndpointID] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if len(endpointIDs) == 0 {
		return errors.New("center: Meridian quota boundary has no affected endpoint")
	}
	orderedEndpointIDs := make([]string, 0, len(endpointIDs))
	for affectedEndpointID := range endpointIDs {
		orderedEndpointIDs = append(orderedEndpointIDs, affectedEndpointID)
	}
	slices.Sort(orderedEndpointIDs)
	// An endpoint revision also affects accounts whose quota did not change.
	// Capture every applied subscription before any account or endpoint changes,
	// including subscriptions that have never been downloaded.
	for _, affectedEndpointID := range orderedEndpointIDs {
		if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, affectedEndpointID); err != nil {
			return err
		}
	}
	for _, accountID := range accountIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_accounts SET desired_revision=desired_revision+1,updated_at=? WHERE id=?`, nowText, accountID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET desired_revision=desired_revision+1,runtime_healthy=0,
			status=CASE WHEN status='revoked' THEN status ELSE 'pending' END,last_error='',updated_at=? WHERE account_id=?`, nowText, accountID); err != nil {
			return err
		}
	}
	for _, affectedEndpointID := range orderedEndpointIDs {
		resultRow, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=?`, nowText, affectedEndpointID)
		if err != nil {
			return err
		}
		if changed, _ := resultRow.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian quota boundary references an unavailable endpoint")
		}
	}
	return nil
}

// markMeridianLandingPeerChanged rebuilds only entries that route through a
// landing node whose authenticated private peer identity changed. The
// subscription query independently requires that peer to be live, so an
// unavailable landing disappears without taking native entries offline.
func (s *Store) markMeridianLandingPeerChanged(ctx context.Context, tx *sql.Tx, nodeID, nowText string) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT endpoint_id FROM meridian_route_grants WHERE egress_node_id=? AND enabled=1 AND status<>'revoked' ORDER BY endpoint_id`, nodeID)
	if err != nil {
		return err
	}
	endpointIDs := []string{}
	for rows.Next() {
		var endpointID string
		if err := rows.Scan(&endpointID); err != nil {
			rows.Close()
			return err
		}
		endpointIDs = append(endpointIDs, endpointID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, endpointID := range endpointIDs {
		if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=?
		WHERE egress_node_id=? AND enabled=1 AND status<>'revoked'`, nowText, nodeID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=?
		WHERE status<>'retired' AND id IN (
		 SELECT DISTINCT endpoint_id FROM meridian_route_grants WHERE egress_node_id=? AND enabled=1 AND status<>'revoked'
		)`, nowText, nodeID)
	return err
}

func (s *Store) meridianQuotaStatesInTx(ctx context.Context, tx *sql.Tx, endpointID string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT account.id,account.total_bytes,account.expiry_time,account.reset_days,account.enabled,account.status
		FROM meridian_accounts account JOIN meridian_credentials credential ON credential.account_id=account.id
		WHERE credential.endpoint_id=? ORDER BY account.id`, endpointID)
	if err != nil {
		return nil, err
	}
	type stored struct {
		id, status string
		plan       meridian.AccountPlan
	}
	values := []stored{}
	for rows.Next() {
		var value stored
		var enabled int
		if err := rows.Scan(&value.id, &value.plan.TotalBytes, &value.plan.ExpiryTime, &value.plan.ResetDays, &enabled, &value.status); err != nil {
			rows.Close()
			return nil, err
		}
		value.plan.Enabled = enabled == 1 && value.status == "active" && (value.plan.ExpiryTime == 0 || value.plan.ExpiryTime > s.now().UnixMilli())
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	states := make(map[string]bool, len(values))
	for _, value := range values {
		usage, err := meridianAccountUsageInTx(ctx, tx, value.id)
		if err != nil {
			return nil, err
		}
		projection, err := meridian.ProjectQuota(value.plan, usage)
		if err != nil {
			return nil, errors.New("center: stored Meridian quota is invalid")
		}
		states[value.id] = projection.Enabled
	}
	return states, nil
}

func saturatingAdd(left, right int64) int64 {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func (s *Store) failUnclaimableMeridianRuntimeCommand(ctx context.Context, tx *sql.Tx, commandID, agentID, endpointID string, cause error) (*AgentTask, error) {
	message := applicationCommandFailureMessage(cause)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if strings.TrimSpace(endpointID) != "" {
		var retirement bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM meridian_endpoints endpoint
			JOIN meridian_deployments deployment ON deployment.endpoint_id=endpoint.id AND deployment.command_id=?
			JOIN meridian_cutover cutover ON cutover.id=1
			WHERE endpoint.id=? AND endpoint.status='ready' AND endpoint.runtime_healthy=1
			AND endpoint.desired_revision=endpoint.applied_revision
			AND cutover.state='retire' AND cutover.subscription_authority='meridian'
		)`, commandID, endpointID).Scan(&retirement); err != nil {
			return nil, err
		}
		if retirement {
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET last_error=?,updated_at=? WHERE id=?`, message, now, endpointID); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET last_error=?,updated_at=? WHERE id=1 AND state='retire'`, message, now); err != nil {
				return nil, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET status='failed',runtime_healthy=0,last_error=?,updated_at=? WHERE id=?`, message, now, endpointID); err != nil {
				return nil, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET last_error=?,updated_at=? WHERE id=1 AND state IN ('project','verify')`, message, now); err != nil {
				return nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_deployments SET status='failed',last_error=?,updated_at=? WHERE endpoint_id=? AND command_id=?`, message, now, endpointID, commandID); err != nil {
			return nil, err
		}
	}
	return s.discardUnclaimableApplicationCommand(ctx, tx, commandID, agentID, 1, nil, nil, cause)
}
