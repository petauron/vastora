package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/platform"
)

const meridianCutoverImportContext = "meridian-cutover-import:v1"

type meridianImportedEndpoint struct {
	model         glivela.RealityEndpoint
	totalBytes    int64
	usedBytes     int64
	resetDay      int
	nextResetAt   string
	lastResetAt   string
	targetIP      string
	hy2           *glivela.HysteriaEndpoint
	vlessEnabled  bool
	hy2Enabled    bool
	hy2NotAfter   string
	entryName     string
	serviceID     string
	applicationID string
	nodeID        string
	sourcePeer    *landing.PeerIdentity
}

func (s *Store) StartMeridianCutover(ctx context.Context) (MeridianCutoverView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MeridianCutoverView{}, err
	}
	defer tx.Rollback()
	var state, authority, controllerApplicationID string
	if err := tx.QueryRowContext(ctx, `SELECT state,subscription_authority,COALESCE(legacy_controller_application_id,'') FROM meridian_cutover WHERE id=1`).Scan(&state, &authority, &controllerApplicationID); err != nil {
		return MeridianCutoverView{}, err
	}
	if authority == "meridian" && state != "publish" && state != "project" && state != "verify" && state != "retire" {
		return MeridianCutoverView{}, errors.New("center: Glivela is already the subscription authority")
	}
	if controllerApplicationID == "" {
		return MeridianCutoverView{}, errors.New("center: the legacy subscription controller is unavailable")
	}
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	if _, err := disposeSupersededLandingExecutionFailures(ctx, tx, "", formattedNow); err != nil {
		return MeridianCutoverView{}, err
	}
	switch state {
	case "inspect", "failed":
		var imported int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM meridian_endpoints)+(SELECT COUNT(*) FROM meridian_accounts)+(SELECT COUNT(*) FROM meridian_credentials)`).Scan(&imported); err != nil {
			return MeridianCutoverView{}, err
		}
		if imported != 0 {
			return MeridianCutoverView{}, errors.New("center: Glivela import already exists; resume its current phase instead of starting another cutover")
		}
		if err := ensureMeridianCutoverIdle(ctx, tx, controllerApplicationID); err != nil {
			return MeridianCutoverView{}, err
		}
		var agentID, appKey, role, appStatus string
		if err := tx.QueryRowContext(ctx, `SELECT node_id,app_key,role,status FROM applications WHERE id=?`, controllerApplicationID).Scan(&agentID, &appKey, &role, &appStatus); err != nil || appKey != threeXUIAppKey || role != threeXUIRoleMaster || appStatus != "running" {
			return MeridianCutoverView{}, errors.New("center: the legacy subscription controller is not ready for export")
		}
		revision, err := s.queueThreeXUIControllerBackup(ctx, tx, controllerApplicationID, agentID, "", now)
		if err != nil {
			return MeridianCutoverView{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET state='backup',backup_revision=?,import_secret_id=NULL,import_sha256='',expected_accounts=0,expected_credentials=0,expected_endpoints=0,expected_routes=0,last_error='',updated_at=? WHERE id=1`, revision, formattedNow); err != nil {
			return MeridianCutoverView{}, err
		}
	case "publish", "project", "verify", "retire":
		if err := ensureMeridianCutoverIdle(ctx, tx, controllerApplicationID); err != nil {
			return MeridianCutoverView{}, err
		}
		if state == "verify" {
			if _, err := s.importReadyMeridianLegacyRoutesInTx(ctx, tx, now); err != nil {
				return MeridianCutoverView{}, err
			}
		}
		if state == "publish" {
			if err := s.restoreMeridianSubscriptionOrigin(ctx, tx, controllerApplicationID, now); err != nil {
				return MeridianCutoverView{}, err
			}
			if err := s.retryFailedMeridianSubscriptionPublication(ctx, tx, now); err != nil {
				return MeridianCutoverView{}, err
			}
		} else if state == "project" {
			if err := s.retryFailedMeridianCutoverDeployments(ctx, tx, now); err != nil {
				return MeridianCutoverView{}, err
			}
		} else if state == "retire" {
			if err := s.queueMissingMeridianRetirements(ctx, tx, now); err != nil {
				return MeridianCutoverView{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET status='pending',runtime_healthy=0,last_error='',updated_at=? WHERE status='failed' AND desired_revision>applied_revision`, formattedNow); err != nil {
			return MeridianCutoverView{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET status='pending',runtime_healthy=0,last_error='',updated_at=? WHERE status='failed' AND enabled=1 AND desired_revision>applied_revision`, formattedNow); err != nil {
			return MeridianCutoverView{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET last_error='',updated_at=? WHERE id=1`, formattedNow); err != nil {
			return MeridianCutoverView{}, err
		}
		if err := s.reconcileMeridianCutoverInTx(ctx, tx, formattedNow); err != nil {
			return MeridianCutoverView{}, err
		}
	default:
		return MeridianCutoverView{}, fmt.Errorf("center: Glivela cutover cannot start from %s", state)
	}
	if err := tx.Commit(); err != nil {
		return MeridianCutoverView{}, err
	}
	return s.MeridianCutover(ctx)
}

func ensureMeridianCutoverIdle(ctx context.Context, tx *sql.Tx, controllerApplicationID string) error {
	var activeCommands, activeDeployments, activeMigrations, unresolvedExecutions, pendingLanding int
	if err := tx.QueryRowContext(ctx, `WITH cutover_entries(id) AS (
		SELECT node_id FROM applications WHERE id=?
		UNION
		SELECT application.node_id FROM three_x_ui_nodes topology
		JOIN applications application ON application.id=topology.worker_application_id
		WHERE topology.master_application_id=? AND topology.status<>'stopped'
		UNION
		SELECT application.node_id FROM meridian_endpoints endpoint
		JOIN applications application ON application.id=endpoint.application_id
	), cutover_agents(id) AS (
		SELECT id FROM cutover_entries
		UNION
		SELECT grant_row.landing_node_id FROM landing_client_grants grant_row
		JOIN applications application ON application.id=grant_row.application_id
		WHERE application.node_id IN (SELECT id FROM cutover_entries) AND grant_row.status<>'revoked'
		UNION
		SELECT landing_node_id FROM landing_proxy_states WHERE node_id IN (SELECT id FROM cutover_entries)
		UNION
		SELECT egress_node_id FROM meridian_route_grants WHERE status<>'revoked'
	)
	SELECT
		(SELECT COUNT(*) FROM application_commands WHERE agent_id IN (SELECT id FROM cutover_agents) AND (state IN ('pending','running') OR reconciliation_required=1)),
		(SELECT COUNT(*) FROM deployments WHERE agent_id IN (SELECT id FROM cutover_agents) AND (state IN ('pending','running') OR reconciliation_required=1)),
		(SELECT COUNT(*) FROM three_x_ui_migrations WHERE state IN ('backing_up','restoring','switching')),
		(SELECT COUNT(*) FROM task_executions WHERE agent_id IN (SELECT id FROM cutover_agents) AND disposition='' AND state<>'succeeded'),
		(SELECT COUNT(*) FROM landing_server_states WHERE node_id IN (SELECT id FROM cutover_agents) AND status IN ('pending','applying') AND desired_revision>applied_revision)
		 + (SELECT COUNT(*) FROM landing_proxy_states WHERE node_id IN (SELECT id FROM cutover_entries) AND status IN ('pending','applying') AND desired_revision>applied_revision)`, controllerApplicationID, controllerApplicationID).Scan(&activeCommands, &activeDeployments, &activeMigrations, &unresolvedExecutions, &pendingLanding); err != nil {
		return err
	}
	if unresolvedExecutions != 0 {
		return errors.New("center: explicitly recover unresolved node executions before starting the Glivela cutover")
	}
	if activeCommands != 0 || activeDeployments != 0 || activeMigrations != 0 || pendingLanding != 0 {
		return errors.New("center: finish or explicitly recover active operations before starting the Glivela cutover")
	}
	return nil
}

// The legacy Agent export carries the Tailscale peer ID from its applied
// journal. Only the exact Center grant maps that identity back to a Vastora
// Agent and supplies the previously authorized entry identity. A current
// capability observation must never silently authorize a replacement source.
func resolveLegacyMeridianGrant(ctx context.Context, tx *sql.Tx, endpoint *meridianImportedEndpoint, legacy meridianruntime.LegacyRoute) (string, error) {
	record, err := readLandingGrant(ctx, tx, legacy.ID)
	if err != nil || record.ApplicationID != endpoint.applicationID || record.ServiceID != endpoint.serviceID ||
		record.ParentID != legacy.ParentIdentityHash || record.Grant.InboundTag != legacy.InboundTag || record.Grant.BaseUser != legacy.BaseUser ||
		record.Grant.FixedUser != legacy.FixedUser || record.Grant.FixedIdentity != landing.Identity(legacy.FixedUUID) ||
		record.Grant.Peer.ID != legacy.EgressNodeID || record.Revision != legacy.Revision || record.Grant.Enabled != legacy.Enabled ||
		record.Grant.HideBase != legacy.HideNative || !glivela.ValidIdentifier(record.LandingNodeID) ||
		!sameLegacyInboundTag(endpoint.model.InboundTag, legacy.InboundTag) {
		return "", errors.New("center: legacy route no longer matches its authorized Center grant")
	}
	if record.Status == "revoked" || record.Status == "revoking" {
		return "", errors.New("center: a revoked legacy route cannot grant Glivela authority")
	}
	if legacy.Enabled {
		if (meridianruntime.Peer{EgressID: endpoint.nodeID, Identity: record.Source}).Validate() != nil {
			return "", errors.New("center: enabled legacy route has no authorized entry identity")
		}
		if endpoint.sourcePeer != nil && *endpoint.sourcePeer != record.Source {
			return "", errors.New("center: legacy routes disagree about the authorized entry identity")
		}
		source := record.Source
		endpoint.sourcePeer = &source
	}
	return record.LandingNodeID, nil
}

func (s *Store) resolveLegacyMeridianEndpoint(ctx context.Context, tx *sql.Tx, controllerApplicationID string, legacy meridianruntime.LegacyEndpoint) (meridianImportedEndpoint, error) {
	applicationID := controllerApplicationID
	if legacy.RemoteNodeID > 0 {
		if err := tx.QueryRowContext(ctx, `SELECT worker_application_id FROM three_x_ui_nodes WHERE master_application_id=? AND remote_node_id=? AND status<>'stopped'`, controllerApplicationID, legacy.RemoteNodeID).Scan(&applicationID); err != nil {
			return meridianImportedEndpoint{}, errors.New("center: legacy remote endpoint no longer maps to an application")
		}
	}
	var nodeID, serviceAddress string
	if err := tx.QueryRowContext(ctx, `SELECT application.node_id,profile.service_address FROM applications application JOIN agent_network_profiles profile ON profile.agent_id=application.node_id WHERE application.id=? AND application.app_key=? AND application.status='running'`, applicationID, threeXUIAppKey).Scan(&nodeID, &serviceAddress); err != nil {
		return meridianImportedEndpoint{}, errors.New("center: legacy endpoint application is unavailable")
	}
	type serviceCandidate struct{ id, name, displayName, inboundTag string }
	rows, err := tx.QueryContext(ctx, `SELECT service.id,service.name,service.display_name,COALESCE(plan.inbound_tag,'') FROM services service LEFT JOIN three_x_ui_inbound_plans plan ON plan.service_id=service.id WHERE service.application_id=? AND service.app_protocol='vless/tcp/reality' AND service.status<>'stopped' ORDER BY service.id`, applicationID)
	if err != nil {
		return meridianImportedEndpoint{}, err
	}
	candidates := []serviceCandidate{}
	for rows.Next() {
		var candidate serviceCandidate
		if err := rows.Scan(&candidate.id, &candidate.name, &candidate.displayName, &candidate.inboundTag); err != nil {
			rows.Close()
			return meridianImportedEndpoint{}, err
		}
		if sameLegacyInboundTag(candidate.inboundTag, legacy.Tag) || candidate.name == "inbound-"+strconv.Itoa(legacy.InboundID) {
			candidates = append(candidates, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return meridianImportedEndpoint{}, err
	}
	rows.Close()
	if len(candidates) != 1 {
		return meridianImportedEndpoint{}, errors.New("center: legacy REALITY service mapping is ambiguous")
	}
	plan, err := readThreeXUIInboundPlan(ctx, tx, candidates[0].id)
	if err != nil || plan.Status != "active" || !sameLegacyInboundTag(plan.InboundTag, legacy.Tag) ||
		plan.TotalBytes != legacy.TotalBytes || legacy.TotalBytes < 0 || legacy.UsedBytes < 0 ||
		plan.ResetDay < 0 || plan.ResetDay > 31 || plan.ResetDay == 0 && plan.NextResetAt != "" ||
		plan.ResetDay > 0 && plan.NextResetAt == "" {
		return meridianImportedEndpoint{}, errors.New("center: legacy entry traffic plan is not converged")
	}
	if plan.NextResetAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, plan.NextResetAt); err != nil {
			return meridianImportedEndpoint{}, errors.New("center: legacy entry traffic reset boundary is invalid")
		}
	}
	if plan.LastResetAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, plan.LastResetAt); err != nil {
			return meridianImportedEndpoint{}, errors.New("center: legacy entry last traffic reset is invalid")
		}
	}
	var advertiseHost string
	if err := tx.QueryRowContext(ctx, `SELECT hostname FROM publications WHERE service_id=? AND kind='public_shared_443' AND status='ready' ORDER BY updated_at DESC LIMIT 1`, candidates[0].id).Scan(&advertiseHost); err != nil || strings.TrimSpace(advertiseHost) == "" {
		return meridianImportedEndpoint{}, errors.New("center: legacy endpoint has no verified public hostname")
	}
	endpointID, err := randomToken(18)
	if err != nil {
		return meridianImportedEndpoint{}, err
	}
	displayName := strings.TrimSpace(candidates[0].displayName)
	if displayName == "" {
		displayName = strings.TrimSpace(legacy.DisplayName)
	}
	model := glivela.RealityEndpoint{ID: endpointID, EntryID: applicationID, InboundTag: legacy.Tag, ListenAddress: serviceAddress, ListenPort: glivela.RealityBackendPort, AdvertiseHost: advertiseHost, AdvertisePort: legacy.Port, Target: legacy.Target, ServerNames: slices.Clone(legacy.ServerNames), PrivateKey: legacy.PrivateKey, PublicKey: legacy.PublicKey, ShortIDs: slices.Clone(legacy.ShortIDs), Fingerprint: legacy.Fingerprint}
	if model.Validate() != nil || displayName == "" {
		return meridianImportedEndpoint{}, errors.New("center: legacy REALITY endpoint cannot be represented by Glivela")
	}
	targetHost, _, err := net.SplitHostPort(model.Target)
	if err != nil || len(model.ServerNames) != 1 {
		return meridianImportedEndpoint{}, errors.New("center: legacy REALITY endpoint target is invalid")
	}
	var guardedHost, targetIP, guardedServerName, guardStatus string
	if err := tx.QueryRowContext(ctx, `SELECT target_host,target_ip,server_name,status FROM three_x_ui_reality_guards WHERE service_id=?`, candidates[0].id).Scan(&guardedHost, &targetIP, &guardedServerName, &guardStatus); err != nil {
		return meridianImportedEndpoint{}, errors.New("center: legacy REALITY endpoint has no verified target guard")
	}
	guardedHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(guardedHost), "."))
	guardedServerName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(guardedServerName), "."))
	guardedTargetMatches := guardedHost == strings.ToLower(strings.TrimSuffix(targetHost, "."))
	if pinnedIP := net.ParseIP(targetHost); pinnedIP != nil {
		// The legacy hardener pins the verified hostname to its public IP in
		// Xray's target. Keep that exact pin instead of requiring the runtime
		// target to equal the guard's original DNS hostname.
		guardedTargetMatches = pinnedIP.Equal(net.ParseIP(targetIP))
	}
	if guardStatus != "ready" || !guardedTargetMatches || guardedServerName != strings.ToLower(strings.TrimSuffix(model.ServerNames[0], ".")) || !isPublicPublicationVerificationIP(net.ParseIP(targetIP)) {
		return meridianImportedEndpoint{}, errors.New("center: legacy REALITY endpoint target guard is not ready")
	}
	resolved := meridianImportedEndpoint{model: model, totalBytes: plan.TotalBytes, usedBytes: legacy.UsedBytes, resetDay: plan.ResetDay, nextResetAt: plan.NextResetAt, lastResetAt: plan.LastResetAt, targetIP: strings.TrimSpace(targetIP), vlessEnabled: legacy.VLESSEnabled, hy2Enabled: legacy.HY2Enabled, entryName: displayName, serviceID: candidates[0].id, applicationID: applicationID, nodeID: nodeID}
	if legacy.HY2Configured {
		hy2 := glivela.HysteriaEndpoint{ID: endpointID + "-hy2", EntryID: applicationID, InboundTag: legacy.HY2Tag, ListenPort: 443, AdvertiseHost: advertiseHost, AdvertisePort: 443, ServerName: legacy.HY2ServerName, CertificatePEM: legacy.HY2Certificate, PrivateKeyPEM: legacy.HY2PrivateKey}
		if hy2.Validate() != nil {
			return meridianImportedEndpoint{}, errors.New("center: legacy Hysteria endpoint cannot be represented by Glivela")
		}
		resolved.hy2 = &hy2
		notAfter, expiryErr := glivela.HysteriaCertificateNotAfter(hy2)
		if expiryErr != nil || !notAfter.After(s.now().Add(time.Hour)) {
			return meridianImportedEndpoint{}, errors.New("center: legacy Hysteria certificate expiry is invalid")
		}
		resolved.hy2NotAfter = notAfter.Format(time.RFC3339Nano)
	}
	if !resolved.vlessEnabled && !resolved.hy2Enabled {
		return meridianImportedEndpoint{}, errors.New("center: legacy endpoint has no enabled protocol")
	}
	return resolved, nil
}

func sameLegacyInboundTag(left, right string) bool {
	normalize := func(value string) string {
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "n") {
			if index := strings.IndexByte(value, '-'); index > 1 {
				if _, err := strconv.Atoi(value[1:index]); err == nil {
					value = value[index+1:]
				}
			}
		}
		return value
	}
	return normalize(left) != "" && normalize(left) == normalize(right)
}

func (s *Store) queueMeridianCutoverDeployments(ctx context.Context, tx *sql.Tx, controllerApplicationID string, now time.Time) error {
	manifest, err := currentOfficialApplicationManifest(ctx, tx, "meridian", now)
	if err != nil {
		return fmt.Errorf("center: authorize Glivela cutover package: %w", err)
	}
	if len(manifest.Images) != 1 || strings.TrimSpace(manifest.Images[0].Reference) == "" {
		return errors.New("center: Glivela catalog package has no audited Xray image")
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	type deploymentTarget struct{ nodeID string }
	applications := map[string]deploymentTarget{}
	rows, err := tx.QueryContext(ctx, `SELECT endpoint.application_id,application.node_id
		FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id
		WHERE endpoint.status<>'retired' ORDER BY endpoint.application_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var applicationID, nodeID string
		if err := rows.Scan(&applicationID, &nodeID); err != nil {
			rows.Close()
			return err
		}
		applications[applicationID] = deploymentTarget{nodeID: nodeID}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, exists := applications[controllerApplicationID]; !exists {
		var controllerNodeID string
		if err := tx.QueryRowContext(ctx, `SELECT node_id FROM applications WHERE id=? AND app_key=? AND status='running'`, controllerApplicationID, threeXUIAppKey).Scan(&controllerNodeID); err != nil {
			return errors.New("center: legacy subscription host is unavailable for Glivela package deployment")
		}
		applications[controllerApplicationID] = deploymentTarget{nodeID: controllerNodeID}
	}
	applicationIDs := make([]string, 0, len(applications))
	for applicationID := range applications {
		applicationIDs = append(applicationIDs, applicationID)
	}
	slices.Sort(applicationIDs)
	stamp := now.Format(time.RFC3339Nano)
	for _, applicationID := range applicationIDs {
		target := applications[applicationID]
		var conflicts int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications WHERE node_id=? AND app_key=? AND id<>?`, target.nodeID, meridianAppKey, applicationID).Scan(&conflicts); err != nil || conflicts != 0 {
			return errors.New("center: target node already has a different Glivela application")
		}
		var serviceAddress string
		if err := tx.QueryRowContext(ctx, `SELECT service_address FROM agent_network_profiles WHERE agent_id=?`, target.nodeID).Scan(&serviceAddress); err != nil || net.ParseIP(serviceAddress) == nil {
			return errors.New("center: Glivela target node has no confirmed private service address")
		}
		deploymentID, err := randomToken(18)
		if err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE applications SET name='Meridian',app_key=?,image=?,status='pending',role='',updated_at=? WHERE id=? AND app_key=? AND status='running'`, meridianAppKey, manifest.Images[0].Reference, stamp, applicationID, threeXUIAppKey)
		if err != nil {
			return err
		}
		if affected, err := updated.RowsAffected(); err != nil || affected != 1 {
			return errors.New("center: legacy application changed before Glivela deployment was queued")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,service_address,operation,delete_data,state,error,created_at,updated_at,application_id,runtime_generation,pre_dispatch_application_status) VALUES(?,?,?,?,?,?,?,'install',0,'pending','',?,?,?,?,'failed')`, deploymentID, target.nodeID, meridianAppKey, manifest.Version, manifestJSON, []byte("{}"), serviceAddress, stamp, stamp, applicationID, platform.ApplicationRuntimeGeneration); err != nil {
			return fmt.Errorf("center: queue Glivela cutover deployment: %w", err)
		}
		if err := s.recordTaskEvent(ctx, tx, deploymentID, target.nodeID, "application.apply", applicationTaskRevision, "queued", "install "+meridianAppKey+" from verified cutover"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) retryFailedMeridianCutoverDeployments(ctx context.Context, tx *sql.Tx, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `WITH required_applications(application_id) AS (
		SELECT application_id FROM meridian_endpoints
		UNION
		SELECT legacy_controller_application_id FROM meridian_cutover WHERE id=1 AND legacy_controller_application_id IS NOT NULL
	)
		SELECT required.application_id,application.node_id,
		deployment.app_version,deployment.manifest_json,deployment.service_address
		FROM required_applications required
		JOIN applications application ON application.id=required.application_id
		JOIN deployments deployment ON deployment.id=(
			SELECT candidate.id FROM deployments candidate
			WHERE candidate.application_id=required.application_id AND candidate.app_key=? AND candidate.state='failed'
			ORDER BY candidate.created_at DESC,candidate.rowid DESC LIMIT 1
		)
		WHERE application.app_key=? AND application.status='failed'
		ORDER BY required.application_id`, meridianAppKey, meridianAppKey)
	if err != nil {
		return err
	}
	type retry struct {
		applicationID  string
		agentID        string
		version        string
		manifest       []byte
		serviceAddress string
	}
	retries := []retry{}
	for rows.Next() {
		var value retry
		if err := rows.Scan(&value.applicationID, &value.agentID, &value.version, &value.manifest, &value.serviceAddress); err != nil {
			rows.Close()
			return err
		}
		var manifest catalog.AppManifest
		if json.Unmarshal(value.manifest, &manifest) != nil || catalog.ValidateApp(manifest) != nil || manifest.ID != "meridian" || manifest.Version != value.version || net.ParseIP(value.serviceAddress) == nil {
			rows.Close()
			return errors.New("center: failed Glivela deployment cannot be safely retried")
		}
		retries = append(retries, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	stamp := now.Format(time.RFC3339Nano)
	for _, value := range retries {
		deploymentID, err := randomToken(18)
		if err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE applications SET status='pending',updated_at=? WHERE id=? AND app_key=? AND status='failed'`, stamp, value.applicationID, meridianAppKey)
		if err != nil {
			return err
		}
		if affected, err := updated.RowsAffected(); err != nil || affected != 1 {
			return errors.New("center: failed Glivela application changed before retry")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,service_address,operation,delete_data,state,error,created_at,updated_at,application_id,runtime_generation,pre_dispatch_application_status) VALUES(?,?,?,?,?,?,?,'install',0,'pending','',?,?,?,?,'failed')`, deploymentID, value.agentID, meridianAppKey, value.version, value.manifest, []byte("{}"), value.serviceAddress, stamp, stamp, value.applicationID, platform.ApplicationRuntimeGeneration); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, deploymentID, value.agentID, "application.apply", applicationTaskRevision, "queued", "retry verified Glivela cutover package"); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) reconcileMeridianCutoverInTx(ctx context.Context, tx *sql.Tx, stamp string) error {
	var state, authority string
	var expectedAccounts, expectedCredentials, expectedEndpoints, expectedRoutes int
	if err := tx.QueryRowContext(ctx, `SELECT state,subscription_authority,expected_accounts,expected_credentials,expected_endpoints,expected_routes FROM meridian_cutover WHERE id=1`).Scan(&state, &authority, &expectedAccounts, &expectedCredentials, &expectedEndpoints, &expectedRoutes); err != nil {
		return err
	}
	if state != "publish" && state != "project" && state != "verify" && state != "retire" {
		return nil
	}
	var accounts, credentials, endpoints, ready, published, retired, routes, readyRoutes int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM meridian_accounts),
		(SELECT COUNT(*) FROM meridian_credentials),
		(SELECT COUNT(*) FROM meridian_endpoints),
		(SELECT COUNT(*) FROM meridian_endpoints WHERE status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision),
		(SELECT COUNT(*) FROM meridian_endpoints endpoint JOIN services service ON service.id=endpoint.service_id
		 WHERE endpoint.status='ready' AND endpoint.runtime_healthy=1 AND endpoint.desired_revision=endpoint.applied_revision
		 AND service.status='ready' AND (endpoint.vless_enabled=0 OR EXISTS(
			SELECT 1 FROM publications publication WHERE publication.service_id=endpoint.service_id
			AND publication.kind='public_shared_443' AND publication.status='ready'
			AND publication.desired_revision=publication.applied_revision
			AND publication.hostname=endpoint.advertise_host
			AND EXISTS(SELECT 1 FROM json_each(endpoint.server_names_json) WHERE value=publication.sni_hostname)
		 ))),
		(SELECT COUNT(*) FROM meridian_endpoints WHERE status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision AND legacy_retired=1),
		(SELECT COUNT(*) FROM meridian_route_grants WHERE enabled=1 AND status<>'revoked'),
		(SELECT COUNT(*) FROM meridian_route_grants WHERE enabled=1 AND status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision AND health_expires_unix_ms>?)`, s.now().UTC().UnixMilli()).Scan(&accounts, &credentials, &endpoints, &ready, &published, &retired, &routes, &readyRoutes); err != nil {
		return err
	}
	if accounts != expectedAccounts || credentials != expectedCredentials || endpoints != expectedEndpoints || routes != expectedRoutes || expectedEndpoints == 0 {
		return errors.New("center: Glivela cutover inventory changed after import")
	}
	if state == "publish" {
		if authority != "meridian" {
			return errors.New("center: Center subscription authority was not committed before publication")
		}
		publicationReady, err := meridianSubscriptionPublicationReady(ctx, tx)
		if err != nil {
			return err
		}
		if !publicationReady {
			return nil
		}
		var controllerApplicationID string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(legacy_controller_application_id,'') FROM meridian_cutover WHERE id=1`).Scan(&controllerApplicationID); err != nil || controllerApplicationID == "" {
			return errors.New("center: legacy subscription controller identity is unavailable")
		}
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return err
		}
		if err := s.queueMeridianCutoverDeployments(ctx, tx, controllerApplicationID, parsed); err != nil {
			return err
		}
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET state='project',last_error='',updated_at=? WHERE id=1 AND state='publish' AND subscription_authority='meridian'`, stamp)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Glivela publication phase changed before deployment")
		}
		return nil
	}
	if state == "project" {
		var failed, running, required int
		if err := tx.QueryRowContext(ctx, `WITH required_applications(application_id) AS (
			SELECT application_id FROM meridian_endpoints
			UNION
			SELECT legacy_controller_application_id FROM meridian_cutover WHERE id=1 AND legacy_controller_application_id IS NOT NULL
		)
			SELECT
			(SELECT COUNT(*) FROM required_applications),
			(SELECT COUNT(*) FROM applications WHERE id IN (SELECT application_id FROM required_applications) AND app_key=? AND status='failed'),
			(SELECT COUNT(*) FROM applications WHERE id IN (SELECT application_id FROM required_applications) AND app_key=? AND status='running')`, meridianAppKey, meridianAppKey).Scan(&required, &failed, &running); err != nil {
			return err
		}
		if failed != 0 {
			_, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET last_error='A Meridian package deployment failed; retry the explicit cutover after reviewing the node error.',updated_at=? WHERE id=1`, stamp)
			return err
		}
		if required == 0 || running != required {
			return nil
		}
		updated, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET state='verify',last_error='',updated_at=? WHERE id=1 AND state='project' AND subscription_authority='meridian'`, stamp)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Glivela deployment phase changed before verification")
		}
		state = "verify"
	}
	if state == "verify" && ready == expectedEndpoints && published == expectedEndpoints && readyRoutes == expectedRoutes {
		unimported, err := meridianUnimportedReadyLegacyRoutes(ctx, tx)
		if err != nil {
			return err
		}
		if unimported != 0 {
			// The imported native subscription must not retire a Center-
			// authorized fixed client that has no Meridian route identity.
			return nil
		}
		if authority != "meridian" {
			return errors.New("center: Center subscription authority was lost during runtime verification")
		}
		publicationReady, err := meridianSubscriptionPublicationReady(ctx, tx)
		if err != nil {
			return err
		}
		if !publicationReady {
			return nil
		}
		result, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET state='retire',last_error='',updated_at=? WHERE id=1 AND state='verify' AND subscription_authority='meridian'`, stamp)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("center: Glivela verification phase changed before retirement")
		}
		return nil
	}
	if state == "retire" && authority == "meridian" {
		// Retirement is one-way cleanup, but completion is still allowed only
		// while the Center-owned publication and every imported entry and route
		// remain at their verified revision. If health changes, ordinary runtime
		// reconciliation restores the affected entry before more legacy state is
		// retired.
		publicationReady, err := meridianSubscriptionPublicationReady(ctx, tx)
		if err != nil {
			return err
		}
		if !publicationReady || ready != expectedEndpoints || published != expectedEndpoints || readyRoutes != expectedRoutes {
			return nil
		}
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return err
		}
		if err := s.queueMissingMeridianRetirements(ctx, tx, parsed); err != nil {
			return err
		}
		controllerRetired, err := meridianControllerLegacyRetired(ctx, tx)
		if err != nil {
			return err
		}
		if retired == expectedEndpoints && controllerRetired {
			updated, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET state='complete',last_error='',updated_at=? WHERE id=1 AND state='retire' AND subscription_authority='meridian'`, stamp)
			if err != nil {
				return err
			}
			if changed, _ := updated.RowsAffected(); changed != 1 {
				return errors.New("center: Glivela retirement phase changed before completion")
			}
		}
	}
	return nil
}

// The legacy landing reconciler once rewrote Center's subscription origin to
// its former :2097 port after import. Repair only that exact, known drift on
// explicit cutover resume; any other origin mismatch remains fail-closed.
func (s *Store) restoreMeridianSubscriptionOrigin(ctx context.Context, tx *sql.Tx, controllerApplicationID string, now time.Time) error {
	var serviceID, endpoint, protocol, appProtocol string
	var containerPort, hostPort int
	if err := tx.QueryRowContext(ctx, `SELECT id,endpoint,protocol,app_protocol,container_port,host_port FROM services
		WHERE application_id=? AND name=? AND source='system' AND status='ready'`, controllerApplicationID, meridianSubscriptionServiceName).
		Scan(&serviceID, &endpoint, &protocol, &appProtocol, &containerPort, &hostPort); err != nil {
		return errors.New("center: preserved Glivela subscription origin is unavailable")
	}
	if protocol != "http" || appProtocol != meridianSubscriptionProtocol || containerPort != 8080 || hostPort != 8080 {
		return errors.New("center: preserved Glivela subscription origin changed unexpectedly")
	}
	if endpoint == net.JoinHostPort(dockerruntime.CenterAlias, "8080") {
		return nil
	}
	if endpoint != net.JoinHostPort(dockerruntime.CenterAlias, strconv.Itoa(landing.SubscriptionPort)) {
		return errors.New("center: Glivela subscription origin drift is not recognized")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications publication
		LEFT JOIN cloudflare_tunnels tunnel ON publication.ingress_owner='tunnel_connector' AND tunnel.agent_id=publication.entry_node_id
		LEFT JOIN gateway_states gateway ON publication.ingress_owner='site_gateway' AND gateway.gateway_node_id=publication.entry_node_id
		WHERE publication.service_id=? AND publication.status<>'stopped' AND (
			publication.ingress_owner='tunnel_connector' AND COALESCE(tunnel.status,'') NOT IN ('ready','failed')
			OR publication.ingress_owner='site_gateway' AND COALESCE(gateway.status,'') NOT IN ('ready','failed')
		)`, serviceID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("center: finish the current subscription entry task before restoring its origin")
	}
	return s.switchMeridianSubscriptionPublication(ctx, tx, controllerApplicationID, now)
}

func (s *Store) switchMeridianSubscriptionPublication(ctx context.Context, tx *sql.Tx, controllerApplicationID string, now time.Time) error {
	var serviceID, siteID, nodeID string
	err := tx.QueryRowContext(ctx, `SELECT service.id,service.site_id,application.node_id
		FROM services service JOIN applications application ON application.id=service.application_id
		WHERE service.application_id=? AND application.app_key IN (?,?) AND application.status='running'
		AND service.name=? AND service.source='system' AND service.status IN ('ready','publishing')`, controllerApplicationID, threeXUIAppKey, meridianAppKey, meridianSubscriptionServiceName).Scan(&serviceID, &siteID, &nodeID)
	if err != nil {
		return errors.New("center: preserved subscription service is unavailable for Glivela authority")
	}
	centerEndpoint := net.JoinHostPort(dockerruntime.CenterAlias, "8080")
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE services SET protocol='http',container_port=8080,host_port=8080,endpoint=?,source='system',app_protocol='meridian/subscription',management=0,status='ready',last_error='',updated_at=?
		WHERE id=? AND application_id=? AND name=? AND source='system' AND status IN ('ready','publishing')`, centerEndpoint, stamp, serviceID, controllerApplicationID, meridianSubscriptionServiceName)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: subscription service changed before Glivela authority switch")
	}

	type publication struct {
		id, kind, owner, gatewayID, hostname, protocol string
		tls                                            bool
	}
	rows, err := tx.QueryContext(ctx, `SELECT publication.id,publication.kind,publication.ingress_owner,publication.entry_node_id,
		publication.hostname,publication.tls_enabled,service.protocol
		FROM publications publication JOIN services service ON service.id=publication.service_id
		WHERE publication.service_id=? AND publication.status<>'stopped' ORDER BY publication.id`, serviceID)
	if err != nil {
		return err
	}
	publications := []publication{}
	for rows.Next() {
		var value publication
		var tls int
		if err := rows.Scan(&value.id, &value.kind, &value.owner, &value.gatewayID, &value.hostname, &tls, &value.protocol); err != nil {
			rows.Close()
			return err
		}
		value.tls = tls == 1
		if value.gatewayID != nodeID || value.owner != ingressSiteGateway && value.owner != ingressTunnelConnector {
			rows.Close()
			return errors.New("center: subscription entry is not colocated with the Center gateway")
		}
		publications = append(publications, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(publications) == 0 {
		return errors.New("center: public subscription entry is missing")
	}
	gateways, tunnels := map[string]bool{}, map[string]bool{}
	for _, value := range publications {
		if value.owner == ingressSiteGateway {
			if err := s.upsertPublicationRoute(ctx, tx, value.id, siteID, serviceID, value.gatewayID, value.hostname, value.protocol, centerEndpoint, value.tls, now); err != nil {
				return err
			}
			gateways[value.gatewayID] = true
		} else {
			tunnels[value.gatewayID] = true
		}
		updated, err := tx.ExecContext(ctx, `UPDATE publications SET desired_revision=desired_revision+1,status='pending',last_error='',updated_at=? WHERE id=? AND status<>'stopped'`, stamp, value.id)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: subscription publication changed before Glivela authority switch")
		}
	}
	for gatewayID := range gateways {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	for gatewayID := range tunnels {
		if err := s.queueTunnelState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	return nil
}

func meridianSubscriptionPublicationReady(ctx context.Context, tx *sql.Tx) (bool, error) {
	var serviceID, endpoint string
	err := tx.QueryRowContext(ctx, `SELECT service.id,service.endpoint FROM services service
		JOIN meridian_cutover cutover ON cutover.legacy_controller_application_id=service.application_id
		WHERE cutover.id=1 AND cutover.state IN ('publish','project','verify','retire') AND cutover.subscription_authority='meridian'
		AND service.name=? AND service.source='system' AND service.status='ready'`, meridianSubscriptionServiceName).Scan(&serviceID, &endpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if endpoint != net.JoinHostPort(dockerruntime.CenterAlias, "8080") {
		return false, errors.New("center: Glivela subscription authority has an unexpected public origin")
	}
	var total, ready int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN status='ready' AND desired_revision=applied_revision THEN 1 ELSE 0 END),0)
		FROM publications WHERE service_id=? AND status<>'stopped'`, serviceID).Scan(&total, &ready); err != nil {
		return false, err
	}
	return total > 0 && ready == total, nil
}

func (s *Store) queueMissingMeridianRetirements(ctx context.Context, tx *sql.Tx, now time.Time) error {
	publicationReady, err := meridianSubscriptionPublicationReady(ctx, tx)
	if err != nil {
		return err
	}
	if !publicationReady {
		return nil
	}
	// Explicit retirement retries call this function without the full cutover
	// reconciler. Neither entries nor a controller-only host may retire while
	// an imported fixed route has lost its fresh transport evidence.
	nowMillis := s.now().UTC().UnixMilli()
	var unreadyRoutes int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants
		WHERE enabled=1 AND status<>'revoked'
		AND (status<>'ready' OR runtime_healthy<>1 OR desired_revision<>applied_revision OR health_expires_unix_ms<=?)`, nowMillis).Scan(&unreadyRoutes); err != nil {
		return err
	}
	if unreadyRoutes != 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT endpoint.id FROM meridian_endpoints endpoint
		JOIN applications application ON application.id=endpoint.application_id
		WHERE endpoint.legacy_retired=0 AND endpoint.status='ready' AND endpoint.runtime_healthy=1
		AND endpoint.desired_revision=endpoint.applied_revision
		AND NOT EXISTS(
			SELECT 1 FROM meridian_route_grants route
			WHERE route.endpoint_id=endpoint.id AND route.enabled=1 AND route.status<>'revoked'
			AND (route.status<>'ready' OR route.runtime_healthy<>1 OR route.desired_revision<>route.applied_revision OR route.health_expires_unix_ms<=?)
		)
		AND application.app_key=? AND application.status='running'
		AND NOT EXISTS(
			SELECT 1 FROM services service JOIN publications publication ON publication.service_id=service.id
			WHERE service.application_id=application.id AND publication.status<>'stopped'
			AND (publication.status<>'ready' OR publication.desired_revision<>publication.applied_revision)
		)
		AND NOT EXISTS(
			SELECT 1 FROM application_commands command
			WHERE command.application_id=endpoint.application_id AND command.kind=? AND command.state IN ('pending','running')
		)
		ORDER BY endpoint.id`, nowMillis, meridianAppKey, meridianruntime.ApplyKind)
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
	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, endpointID := range endpointIDs {
		projection, err := s.buildMeridianRuntimeTask(ctx, tx, endpointID, "")
		if err != nil || !projection.task.RetireLegacy {
			return errors.New("center: Glivela retirement authorization is unavailable")
		}
		commandID, err := randomToken(18)
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(meridianruntime.Command{EndpointID: endpointID})
		var siteID, displayName string
		if err := tx.QueryRowContext(ctx, `SELECT site_id,name FROM applications WHERE id=?`, projection.task.ApplicationID).Scan(&siteID, &displayName); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?, 'pending',?,?)`, commandID, projection.task.ApplicationID, siteID, displayName, projection.agentID, projection.agentID, meridianruntime.ApplyKind, encoded, stamp, stamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_deployments(endpoint_id,desired_revision,applied_revision,desired_sha256,applied_sha256,command_id,status,last_error,updated_at)
			VALUES(?,?,?,?,?,?,'pending','',?)
			ON CONFLICT(endpoint_id) DO UPDATE SET command_id=excluded.command_id,status='pending',last_error='',updated_at=excluded.updated_at`, endpointID, projection.task.Desired.Revision, projection.task.Desired.Revision, projection.task.Desired.ConfigSHA256, projection.task.Desired.ConfigSHA256, commandID, stamp); err != nil {
			return err
		}
		endpointUpdate, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET last_error='',updated_at=?
			WHERE id=? AND status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision AND legacy_retired=0`, stamp, endpointID)
		if err != nil {
			return err
		}
		if changed, _ := endpointUpdate.RowsAffected(); changed != 1 {
			return errors.New("center: verified Glivela endpoint changed before legacy retirement was queued")
		}
		if err := s.recordTaskEvent(ctx, tx, commandID, projection.agentID, "application.command", int64(projection.task.Desired.Revision), "queued", "Glivela legacy runtime retirement queued"); err != nil {
			return err
		}
	}
	var controllerApplicationID, controllerAgentID, controllerSiteID, controllerName, controllerAppKey, controllerStatus string
	if err := tx.QueryRowContext(ctx, `SELECT application.id,application.node_id,application.site_id,application.name,application.app_key,application.status
		FROM meridian_cutover cutover JOIN applications application ON application.id=cutover.legacy_controller_application_id
		WHERE cutover.id=1`).Scan(&controllerApplicationID, &controllerAgentID, &controllerSiteID, &controllerName, &controllerAppKey, &controllerStatus); err != nil {
		return errors.New("center: Glivela legacy subscription host is unavailable for retirement")
	}
	if controllerAppKey != meridianAppKey || controllerStatus != "running" {
		return errors.New("center: Glivela legacy subscription host package has not converged")
	}
	var controllerHasEndpoint int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_endpoints WHERE application_id=?)`, controllerApplicationID).Scan(&controllerHasEndpoint); err != nil {
		return err
	}
	if controllerHasEndpoint == 0 {
		var unretiredEntries int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_endpoints WHERE status<>'retired' AND legacy_retired=0`).Scan(&unretiredEntries); err != nil {
			return err
		}
		if unretiredEntries != 0 {
			return nil
		}
		var succeeded, active int
		if err := tx.QueryRowContext(ctx, `SELECT
			EXISTS(SELECT 1 FROM application_commands WHERE application_id=? AND kind=? AND state='succeeded'),
			EXISTS(SELECT 1 FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))`,
			controllerApplicationID, meridianruntime.LegacyRetireKind, controllerAgentID).Scan(&succeeded, &active); err != nil {
			return err
		}
		if succeeded == 0 && active == 0 {
			commandID, err := randomToken(18)
			if err != nil {
				return err
			}
			task := meridianruntime.LegacyRetireTask{ApplicationID: controllerApplicationID}
			encoded, _ := json.Marshal(task)
			if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at)
				VALUES(?,?,?,?,?,?,?,?, 'pending',?,?)`, commandID, controllerApplicationID, controllerSiteID, controllerName, controllerAgentID, controllerAgentID, meridianruntime.LegacyRetireKind, encoded, stamp, stamp); err != nil {
				return err
			}
			if err := s.recordTaskEvent(ctx, tx, commandID, controllerAgentID, "application.command", 1, "queued", "Glivela legacy subscription host retirement queued"); err != nil {
				return err
			}
		}
	}
	return nil
}

func meridianControllerLegacyRetired(ctx context.Context, tx *sql.Tx) (bool, error) {
	var controllerApplicationID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(legacy_controller_application_id,'') FROM meridian_cutover WHERE id=1`).Scan(&controllerApplicationID); err != nil || controllerApplicationID == "" {
		return false, errors.New("center: Glivela legacy subscription host identity is unavailable")
	}
	var endpointCount, retiredEndpointCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN legacy_retired=1 THEN 1 ELSE 0 END),0)
		FROM meridian_endpoints WHERE application_id=?`, controllerApplicationID).Scan(&endpointCount, &retiredEndpointCount); err != nil {
		return false, err
	}
	if endpointCount > 0 {
		return retiredEndpointCount == endpointCount, nil
	}
	var retired int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM application_commands WHERE application_id=? AND kind=? AND state='succeeded')`, controllerApplicationID, meridianruntime.LegacyRetireKind).Scan(&retired); err != nil {
		return false, err
	}
	return retired == 1, nil
}
