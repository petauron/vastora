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

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/platform"
)

const meridianCutoverImportContext = "meridian-cutover-import:v1"

type meridianImportedCredential struct {
	model      meridian.Credential
	endpointID string
	protocolID string
	hy2Auth    string
	baseline   int64
	observed   int64
}

type meridianImportedAccount struct {
	model       meridian.Account
	token       string
	credentials []meridianImportedCredential
	grants      []meridian.RouteGrant
}

type meridianImportedEndpoint struct {
	model         meridian.RealityEndpoint
	targetIP      string
	hy2           *meridian.HysteriaEndpoint
	vlessEnabled  bool
	hy2Enabled    bool
	hy2NotAfter   string
	entryName     string
	serviceID     string
	applicationID string
	nodeID        string
}

func (value meridianImportedCredential) material() meridian.CredentialMaterial {
	material := meridian.CredentialMaterial{Credential: value.model, ProtocolID: value.protocolID}
	if value.hy2Auth != "" {
		material.HysteriaAuth = value.hy2Auth
		material.HysteriaIdentity = meridian.Identity(value.hy2Auth)
	}
	return material
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
		return MeridianCutoverView{}, errors.New("center: Meridian is already the subscription authority")
	}
	if controllerApplicationID == "" {
		return MeridianCutoverView{}, errors.New("center: the legacy subscription controller is unavailable")
	}
	now := s.now().UTC()
	formattedNow := now.Format(time.RFC3339Nano)
	switch state {
	case "inspect", "failed":
		var imported int
		if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM meridian_endpoints)+(SELECT COUNT(*) FROM meridian_accounts)+(SELECT COUNT(*) FROM meridian_credentials)`).Scan(&imported); err != nil {
			return MeridianCutoverView{}, err
		}
		if imported != 0 {
			return MeridianCutoverView{}, errors.New("center: Meridian import already exists; resume its current phase instead of starting another cutover")
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
		if state == "publish" {
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
		return MeridianCutoverView{}, fmt.Errorf("center: Meridian cutover cannot start from %s", state)
	}
	if err := tx.Commit(); err != nil {
		return MeridianCutoverView{}, err
	}
	return s.MeridianCutover(ctx)
}

func ensureMeridianCutoverIdle(ctx context.Context, tx *sql.Tx, controllerApplicationID string) error {
	var activeCommands, activeDeployments, activeMigrations, unresolvedExecutions int
	if err := tx.QueryRowContext(ctx, `WITH cutover_agents(id) AS (
		SELECT node_id FROM applications WHERE id=?
		UNION
		SELECT application.node_id FROM three_x_ui_nodes topology
		JOIN applications application ON application.id=topology.worker_application_id
		WHERE topology.master_application_id=? AND topology.status<>'stopped'
		UNION
		SELECT application.node_id FROM meridian_endpoints endpoint
		JOIN applications application ON application.id=endpoint.application_id
	)
	SELECT
		(SELECT COUNT(*) FROM application_commands WHERE agent_id IN (SELECT id FROM cutover_agents) AND (state IN ('pending','running') OR reconciliation_required=1)),
		(SELECT COUNT(*) FROM deployments WHERE agent_id IN (SELECT id FROM cutover_agents) AND (state IN ('pending','running') OR reconciliation_required=1)),
		(SELECT COUNT(*) FROM three_x_ui_migrations WHERE state IN ('backing_up','restoring','switching')),
		(SELECT COUNT(*) FROM task_executions WHERE agent_id IN (SELECT id FROM cutover_agents) AND disposition='' AND state<>'succeeded')`, controllerApplicationID, controllerApplicationID).Scan(&activeCommands, &activeDeployments, &activeMigrations, &unresolvedExecutions); err != nil {
		return err
	}
	if unresolvedExecutions != 0 {
		return errors.New("center: explicitly recover unresolved node executions before starting the Meridian cutover")
	}
	if activeCommands != 0 || activeDeployments != 0 || activeMigrations != 0 {
		return errors.New("center: finish or explicitly recover active operations before starting the Meridian cutover")
	}
	return nil
}

func (s *Store) queueMeridianLegacyExport(ctx context.Context, tx *sql.Tx, applicationID, agentID string, now time.Time) error {
	command := meridianruntime.LegacyExportCommand{ApplicationID: applicationID}
	if command.Validate() != nil {
		return errors.New("center: invalid Meridian legacy export target")
	}
	encoded, _ := json.Marshal(command)
	id, err := randomToken(18)
	if err != nil {
		return err
	}
	stamp := now.Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)`, id, applicationID, agentID, agentID, meridianruntime.LegacyExportKind, encoded, stamp, stamp); err != nil {
		return fmt.Errorf("center: queue Meridian legacy export: %w", err)
	}
	return s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "queued", "Meridian one-time legacy export queued")
}

func (s *Store) completeMeridianLegacyExport(ctx context.Context, commit projectionCommit, tx *sql.Tx, taskID, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var command meridianruntime.LegacyExportCommand
	var envelope ApplicationTaskResult
	if json.Unmarshal(inputJSON, &command) != nil || command.Validate() != nil {
		return errors.New("center: stored Meridian legacy export is invalid")
	}
	if succeeded && (len(rawResult) == 0 || json.Unmarshal(rawResult, &envelope) != nil || envelope.MeridianLegacyExport == nil || envelope.MeridianLegacyExport.Validate(command) != nil) {
		succeeded, taskError = false, "center: Agent returned an invalid Meridian legacy export"
	}
	now := s.now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if succeeded {
		if _, err := tx.ExecContext(ctx, `SAVEPOINT meridian_import`); err != nil {
			return err
		}
		exportJSON, err := json.Marshal(envelope.MeridianLegacyExport.Export)
		if err == nil {
			var importSecretID string
			importSecretID, err = s.putSecret(ctx, tx, exportJSON, meridianCutoverImportContext)
			if err == nil {
				err = s.importLegacyMeridianInTx(ctx, tx, envelope.MeridianLegacyExport.Export, importSecretID, now)
			}
		}
		if err != nil {
			_, _ = tx.ExecContext(ctx, `ROLLBACK TO meridian_import`)
			_, _ = tx.ExecContext(ctx, `RELEASE meridian_import`)
			succeeded, taskError = false, "center: Meridian import failed: "+err.Error()
		} else if _, err := tx.ExecContext(ctx, `RELEASE meridian_import`); err != nil {
			return err
		}
	}
	state, event, message := "succeeded", "succeeded", "Meridian legacy authority imported"
	if !succeeded {
		state, event = "failed", "failed"
		if strings.TrimSpace(taskError) == "" {
			taskError = "Meridian legacy export failed"
		}
		message = taskError
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET state='failed',last_error=?,updated_at=? WHERE id=1`, taskError, stamp); err != nil {
			return err
		}
	}
	publicResult := []byte(`{}`)
	if succeeded {
		publicResult, _ = json.Marshal(map[string]any{"accounts": len(envelope.MeridianLegacyExport.Export.Clients), "endpoints": len(envelope.MeridianLegacyExport.Export.Endpoints), "routes": len(envelope.MeridianLegacyExport.Export.Routes)})
	}
	commandUpdate, err := tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json=?,lease_expires_at='',error=?,updated_at=? WHERE id=? AND state='running'`, state, publicResult, taskError, stamp, taskID)
	if err != nil {
		return err
	}
	if changed, _ := commandUpdate.RowsAffected(); changed != 1 {
		return errors.New("center: Meridian legacy export command changed before its receipt was committed")
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Store) importLegacyMeridianInTx(ctx context.Context, tx *sql.Tx, exported meridianruntime.LegacyExport, importSecretID string, now time.Time) error {
	if strings.TrimSpace(importSecretID) == "" {
		return errors.New("encrypted import evidence is unavailable")
	}
	var state, authority, controllerApplicationID string
	if err := tx.QueryRowContext(ctx, `SELECT state,subscription_authority,COALESCE(legacy_controller_application_id,'') FROM meridian_cutover WHERE id=1`).Scan(&state, &authority, &controllerApplicationID); err != nil || state != "import" || authority != "legacy" || controllerApplicationID != exported.ControllerApplicationID {
		return errors.New("cutover authority changed before import")
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM meridian_endpoints)+(SELECT COUNT(*) FROM meridian_accounts)+(SELECT COUNT(*) FROM meridian_credentials)`).Scan(&existing); err != nil || existing != 0 {
		return errors.New("Meridian import target is not empty")
	}
	endpointByInbound := map[int]meridianImportedEndpoint{}
	endpointByEntry := map[string]meridianImportedEndpoint{}
	for _, legacy := range exported.Endpoints {
		if legacy.Port != meridian.DefaultRealityPort || !legacy.VLESSEnabled {
			return errors.New("every imported endpoint must keep VLESS enabled on TCP 443 before Meridian cutover")
		}
		if legacy.TotalBytes > 0 {
			return errors.New("legacy entry traffic limits must be removed before Meridian cutover; Meridian enforces one shared quota per account")
		}
		resolved, err := s.resolveLegacyMeridianEndpoint(ctx, tx, exported.ControllerApplicationID, legacy)
		if err != nil {
			return err
		}
		if _, duplicate := endpointByEntry[resolved.applicationID]; duplicate {
			return errors.New("multiple legacy endpoints resolve to one Meridian application")
		}
		endpointByInbound[legacy.InboundID], endpointByEntry[resolved.applicationID] = resolved, resolved
	}

	accounts := make([]meridianImportedAccount, 0, len(exported.Clients))
	accountByLegacyIdentity := map[string]int{}
	baseByAccountAndEndpoint := map[string]meridianImportedCredential{}
	for _, legacy := range exported.Clients {
		if legacy.LimitIP > 0 {
			return errors.New("legacy simultaneous IP limits must be removed before Meridian cutover because Meridian cannot preserve that policy")
		}
		accountID, err := randomToken(18)
		if err != nil {
			return err
		}
		account := meridianImportedAccount{model: meridian.Account{ID: accountID, DisplayName: legacy.Email, Plan: meridian.AccountPlan{TotalBytes: legacy.TotalBytes, ExpiryTime: legacy.ExpiryTime, ResetDays: legacy.ResetDays, Enabled: legacy.Enabled}, DesiredRevision: 1}, token: legacy.SubscriptionToken}
		for index, inboundID := range legacy.InboundIDs {
			endpoint, exists := endpointByInbound[inboundID]
			if !exists {
				return errors.New("legacy client references an endpoint that was not imported")
			}
			credentialID, err := randomToken(18)
			if err != nil {
				return err
			}
			credential := meridianImportedCredential{model: meridian.Credential{ID: credentialID, AccountID: accountID, Kind: meridian.NativeCredential, User: "meridian-native-" + credentialID, Identity: meridian.Identity(legacy.UUID), EntryID: endpoint.applicationID, Enabled: legacy.Enabled}, endpointID: endpoint.model.ID, protocolID: legacy.UUID, hy2Auth: legacy.HY2AuthByInbound[inboundID]}
			if index == 0 {
				credential.baseline, credential.observed = legacy.UsageBaseline, legacy.UsageObserved
			}
			account.credentials = append(account.credentials, credential)
			baseByAccountAndEndpoint[accountID+"\x00"+endpoint.model.ID] = credential
		}
		accountByLegacyIdentity[meridianruntime.LegacyIdentityFingerprint(legacy.UUID)] = len(accounts)
		accounts = append(accounts, account)
	}
	for _, legacy := range exported.Routes {
		accountIndex, exists := accountByLegacyIdentity[legacy.ParentIdentityHash]
		if !exists {
			return errors.New("legacy route parent was not imported")
		}
		endpoint, exists := endpointByInbound[legacy.InboundID]
		if !exists {
			return errors.New("legacy route endpoint was not imported")
		}
		account := &accounts[accountIndex]
		base, exists := baseByAccountAndEndpoint[account.model.ID+"\x00"+endpoint.model.ID]
		if !exists {
			return errors.New("legacy route native credential is unavailable")
		}
		routeCredentialID, err := randomToken(18)
		if err != nil {
			return err
		}
		route := meridianImportedCredential{model: meridian.Credential{ID: routeCredentialID, AccountID: account.model.ID, Kind: meridian.RouteCredential, User: meridian.RouteUser(legacy.ID), Identity: meridian.Identity(legacy.FixedUUID), EntryID: endpoint.applicationID, EgressID: legacy.EgressNodeID, Enabled: legacy.Enabled && account.model.Plan.Enabled}, endpointID: endpoint.model.ID, protocolID: legacy.FixedUUID, baseline: legacy.UsageBaseline, observed: legacy.UsageObserved}
		account.credentials = append(account.credentials, route)
		account.grants = append(account.grants, meridian.RouteGrant{ID: legacy.ID, AccountID: account.model.ID, EntryID: endpoint.applicationID, EgressID: legacy.EgressNodeID, InboundTag: endpoint.model.InboundTag, Base: base.model, Route: route.model, Mode: meridian.FixedMode, Enabled: legacy.Enabled, HideNative: legacy.HideNative, DesiredRev: 1})
	}

	planAccounts := make([]meridian.ImportAccount, 0, len(accounts))
	materials := map[string]meridian.CredentialMaterial{}
	for _, account := range accounts {
		item := meridian.ImportAccount{Account: account.model, SubscriptionTokenFingerprint: meridian.SubscriptionTokenFingerprint(account.token), HysteriaIdentities: map[string]string{}, Grants: slices.Clone(account.grants)}
		for _, credential := range account.credentials {
			item.Credentials = append(item.Credentials, credential.model)
			if credential.model.Kind == meridian.NativeCredential && credential.hy2Auth != "" {
				item.HysteriaIdentities[credential.model.ID] = meridian.Identity(credential.hy2Auth)
			}
			item.Usage = append(item.Usage, meridian.UsageMember{CredentialID: credential.model.ID, Baseline: credential.baseline, Observed: credential.observed, Active: credential.model.Enabled})
			materials[credential.model.ID] = credential.material()
		}
		planAccounts = append(planAccounts, item)
	}
	plan, err := meridian.PlanImport(planAccounts)
	if err != nil {
		return errors.New("legacy authority does not form a valid Meridian import plan")
	}
	if err := verifyMeridianImportSubscriptions(plan, materials, endpointByEntry); err != nil {
		return err
	}
	if err := s.preserveLegacyMeridianSubscriptionPublication(ctx, tx, exported.ControllerApplicationID); err != nil {
		return err
	}
	stamp := now.Format(time.RFC3339Nano)
	for _, endpoint := range endpointByInbound {
		privateSecretID, err := s.putSecret(ctx, tx, []byte(endpoint.model.PrivateKey), meridianEndpointSecretContext(endpoint.model.ID))
		if err != nil {
			return err
		}
		names, _ := json.Marshal(endpoint.model.ServerNames)
		shortIDs, _ := json.Marshal(endpoint.model.ShortIDs)
		var hy2Tag, hy2ServerName string
		var hy2CertificateSecretID, hy2PrivateKeySecretID any
		if endpoint.hy2 != nil {
			hy2Tag, hy2ServerName = endpoint.hy2.InboundTag, endpoint.hy2.ServerName
			certificateID, secretErr := s.putSecret(ctx, tx, []byte(endpoint.hy2.CertificatePEM), meridianHY2CertificateSecretContext(endpoint.model.ID))
			if secretErr != nil {
				return secretErr
			}
			privateKeyID, secretErr := s.putSecret(ctx, tx, []byte(endpoint.hy2.PrivateKeyPEM), meridianHY2PrivateKeySecretContext(endpoint.model.ID))
			if secretErr != nil {
				return secretErr
			}
			hy2CertificateSecretID, hy2PrivateKeySecretID = certificateID, privateKeyID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,vless_enabled,hy2_enabled,hy2_inbound_tag,hy2_server_name,hy2_certificate_secret_id,hy2_private_key_secret_id,hy2_certificate_not_after,desired_revision,applied_revision,runtime_healthy,legacy_retired,status,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,0,0,0,'pending','',?,?)`, endpoint.model.ID, endpoint.applicationID, endpoint.serviceID, endpoint.model.InboundTag, endpoint.model.ListenPort, endpoint.model.AdvertiseHost, endpoint.model.AdvertisePort, endpoint.model.Target, endpoint.targetIP, names, privateSecretID, endpoint.model.PublicKey, shortIDs, endpoint.model.Fingerprint, boolInt(endpoint.vlessEnabled), boolInt(endpoint.hy2Enabled), hy2Tag, hy2ServerName, hy2CertificateSecretID, hy2PrivateKeySecretID, endpoint.hy2NotAfter, stamp, stamp); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET endpoint=?,protocol='tcp',container_port=443,host_port=443,source='observed',app_protocol=?,observed_listen='0.0.0.0',status='pending',last_error='',updated_at=? WHERE id=?`, net.JoinHostPort(dockerruntime.MeridianAlias, "443"), meridianEntryProtocol, stamp, endpoint.serviceID); err != nil {
			return err
		}
	}
	for _, account := range accounts {
		tokenSecretID, err := s.putSecret(ctx, tx, []byte(account.token), meridianAccountSecretContext(account.model.ID))
		if err != nil {
			return err
		}
		status, enabled := "active", 1
		if !account.model.Plan.Enabled {
			status, enabled = "disabled", 0
		} else if account.model.Plan.ExpiryTime > 0 && account.model.Plan.ExpiryTime <= now.UnixMilli() {
			status, enabled = "expired", 0
		}
		nextReset := ""
		if account.model.Plan.ResetDays > 0 {
			nextReset = now.AddDate(0, 0, account.model.Plan.ResetDays).Format(time.RFC3339Nano)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,total_bytes,expiry_time,reset_days,next_reset_at,last_reset_at,enabled,subscription_token_secret_id,subscription_token_sha256,desired_revision,applied_revision,status,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,?, ?,?,?,1,0,?,'',?,?)`, account.model.ID, account.model.DisplayName, account.model.Plan.TotalBytes, account.model.Plan.ExpiryTime, account.model.Plan.ResetDays, nextReset, "", enabled, tokenSecretID, meridian.SubscriptionTokenFingerprint(account.token), status, stamp, stamp); err != nil {
			return err
		}
		for _, credential := range account.credentials {
			protocolSecretID, err := s.putSecret(ctx, tx, []byte(credential.protocolID), meridianCredentialSecretContext(credential.model.ID))
			if err != nil {
				return err
			}
			var egress any
			var hy2SecretID any
			hy2Identity := ""
			if credential.model.Kind == meridian.NativeCredential && credential.hy2Auth != "" {
				secretID, secretErr := s.putSecret(ctx, tx, []byte(credential.hy2Auth), meridianCredentialHY2SecretContext(credential.model.ID))
				if secretErr != nil {
					return secretErr
				}
				hy2SecretID, hy2Identity = secretID, meridian.Identity(credential.hy2Auth)
			}
			if credential.model.EgressID != "" {
				egress = credential.model.EgressID
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,hy2_auth_secret_id,hy2_identity_sha256,egress_node_id,enabled,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, credential.model.ID, credential.model.AccountID, credential.endpointID, credential.model.Kind, credential.model.User, credential.model.Identity, protocolSecretID, hy2SecretID, hy2Identity, egress, boolInt(credential.model.Enabled), stamp, stamp); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,baseline_bytes,observed_bytes,raw_up_bytes,raw_down_bytes,observed_at) VALUES(?,?,?,?,?,?)`, credential.model.ID, credential.baseline, credential.observed, credential.observed, 0, stamp); err != nil {
				return err
			}
		}
		for _, grant := range account.grants {
			endpointID := ""
			for _, endpoint := range endpointByInbound {
				if endpoint.applicationID == grant.EntryID {
					endpointID = endpoint.model.ID
					break
				}
			}
			if endpointID == "" {
				return errors.New("imported route endpoint disappeared")
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,mode,hide_native,enabled,desired_revision,applied_revision,runtime_healthy,status,last_error,created_at,updated_at) VALUES(?,?,?,?,?,?,'fixed',?,?,1,0,0,'pending','',?,?)`, grant.ID, grant.AccountID, endpointID, grant.EgressID, grant.Base.ID, grant.Route.ID, boolInt(grant.HideNative), boolInt(grant.Enabled), stamp, stamp); err != nil {
				return err
			}
		}
	}
	credentialCount, routeCount := 0, 0
	for _, account := range accounts {
		credentialCount += len(account.credentials)
		for _, grant := range account.grants {
			if grant.Enabled {
				routeCount++
			}
		}
	}
	// Move the stable public subscription URL to Center before any legacy
	// runtime can be replaced. Until the new route receipt arrives the gateway
	// continues to serve the old 3x-ui origin; both origins project the same
	// immutable imported snapshot, so this ordering cannot create an outage.
	if err := s.switchMeridianSubscriptionPublication(ctx, tx, exported.ControllerApplicationID, now); err != nil {
		return err
	}
	cutoverUpdate, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET state='publish',subscription_authority='meridian',switched_at=?,import_secret_id=?,import_sha256=?,expected_accounts=?,expected_credentials=?,expected_endpoints=?,expected_routes=?,last_error='',updated_at=? WHERE id=1 AND state='import' AND subscription_authority='legacy'`, stamp, importSecretID, plan.SHA256, len(accounts), credentialCount, len(endpointByInbound), routeCount, stamp)
	if err != nil {
		return err
	}
	if changed, _ := cutoverUpdate.RowsAffected(); changed != 1 {
		return errors.New("center: subscription authority changed during Meridian import")
	}
	return nil
}

func (s *Store) resolveLegacyMeridianEndpoint(ctx context.Context, tx *sql.Tx, controllerApplicationID string, legacy meridianruntime.LegacyEndpoint) (meridianImportedEndpoint, error) {
	applicationID := controllerApplicationID
	if legacy.RemoteNodeID > 0 {
		if err := tx.QueryRowContext(ctx, `SELECT worker_application_id FROM three_x_ui_nodes WHERE master_application_id=? AND remote_node_id=? AND status<>'stopped'`, controllerApplicationID, legacy.RemoteNodeID).Scan(&applicationID); err != nil {
			return meridianImportedEndpoint{}, errors.New("center: legacy remote endpoint no longer maps to an application")
		}
	}
	var nodeID string
	if err := tx.QueryRowContext(ctx, `SELECT application.node_id FROM applications application WHERE application.id=? AND application.app_key=? AND application.status='running'`, applicationID, threeXUIAppKey).Scan(&nodeID); err != nil {
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
	model := meridian.RealityEndpoint{ID: endpointID, EntryID: applicationID, InboundTag: legacy.Tag, ListenPort: legacy.Port, AdvertiseHost: advertiseHost, AdvertisePort: legacy.Port, Target: legacy.Target, ServerNames: slices.Clone(legacy.ServerNames), PrivateKey: legacy.PrivateKey, PublicKey: legacy.PublicKey, ShortIDs: slices.Clone(legacy.ShortIDs), Fingerprint: legacy.Fingerprint}
	if model.Validate() != nil || displayName == "" {
		return meridianImportedEndpoint{}, errors.New("center: legacy REALITY endpoint cannot be represented by Meridian")
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
	if guardStatus != "ready" || guardedHost != strings.ToLower(strings.TrimSuffix(targetHost, ".")) || guardedServerName != strings.ToLower(strings.TrimSuffix(model.ServerNames[0], ".")) || !isPublicPublicationVerificationIP(net.ParseIP(targetIP)) {
		return meridianImportedEndpoint{}, errors.New("center: legacy REALITY endpoint target guard is not ready")
	}
	resolved := meridianImportedEndpoint{model: model, targetIP: strings.TrimSpace(targetIP), vlessEnabled: legacy.VLESSEnabled, hy2Enabled: legacy.HY2Enabled, entryName: displayName, serviceID: candidates[0].id, applicationID: applicationID, nodeID: nodeID}
	if legacy.HY2Configured {
		hy2 := meridian.HysteriaEndpoint{ID: endpointID + "-hy2", EntryID: applicationID, InboundTag: legacy.HY2Tag, ListenPort: 443, AdvertiseHost: advertiseHost, AdvertisePort: 443, ServerName: legacy.HY2ServerName, CertificatePEM: legacy.HY2Certificate, PrivateKeyPEM: legacy.HY2PrivateKey}
		if hy2.Validate() != nil {
			return meridianImportedEndpoint{}, errors.New("center: legacy Hysteria endpoint cannot be represented by Meridian")
		}
		resolved.hy2 = &hy2
		notAfter, expiryErr := meridian.HysteriaCertificateNotAfter(hy2)
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

func verifyMeridianImportSubscriptions(plan meridian.ImportPlan, materials map[string]meridian.CredentialMaterial, endpoints map[string]meridianImportedEndpoint) error {
	for _, item := range plan.Accounts {
		entries := []meridian.NativeEntry{}
		credentialByID := map[string]meridian.Credential{}
		links := map[string]string{}
		for _, credential := range item.Credentials {
			endpoint, exists := endpoints[credential.EntryID]
			material, hasMaterial := materials[credential.ID]
			if !exists || !hasMaterial || material.Credential != credential {
				return errors.New("center: imported subscription material is incomplete")
			}
			if endpoint.vlessEnabled {
				link, err := meridian.LinkForCredential(endpoint.model, material, endpoint.entryName)
				if err != nil {
					return errors.New("center: imported VLESS subscription link is invalid")
				}
				links[credential.ID+"\x00"+string(meridian.VLESSReality)] = link
				if credential.Kind == meridian.NativeCredential {
					entries = append(entries, meridian.NativeEntry{Material: material, Protocol: meridian.VLESSReality, Link: link})
				}
			}
			if credential.Kind == meridian.NativeCredential && endpoint.hy2Enabled && endpoint.hy2 != nil {
				link, err := meridian.Hysteria2LinkForCredential(*endpoint.hy2, material, endpoint.entryName+" · HY2")
				if err != nil {
					return errors.New("center: imported Hysteria subscription link is invalid")
				}
				links[credential.ID+"\x00"+string(meridian.Hysteria2)] = link
				if credential.Kind == meridian.NativeCredential {
					entries = append(entries, meridian.NativeEntry{Material: material, Protocol: meridian.Hysteria2, Link: link})
				}
			}
			credentialByID[credential.ID] = credential
		}
		routes := []meridian.PublishedRoute{}
		for _, grant := range item.Grants {
			grant.Base, grant.Route = credentialByID[grant.Base.ID], credentialByID[grant.Route.ID]
			grant.AppliedRev, grant.RuntimeGood = grant.DesiredRev, true
			for _, protocol := range []meridian.ProtocolKind{meridian.VLESSReality} {
				baseLink, baseOK := links[grant.Base.ID+"\x00"+string(protocol)]
				routeLink, routeOK := links[grant.Route.ID+"\x00"+string(protocol)]
				if !baseOK && !routeOK {
					continue
				}
				if !baseOK || !routeOK {
					return errors.New("center: imported routed protocol material is incomplete")
				}
				baseMaterial, routeMaterial := materials[grant.Base.ID], materials[grant.Route.ID]
				baseIdentity, routeIdentity := baseMaterial.Credential.Identity, routeMaterial.Credential.Identity
				endpoint := endpoints[grant.EntryID]
				routes = append(routes, meridian.PublishedRoute{Grant: grant, Protocol: protocol, EntryName: endpoint.entryName, BaseLink: baseLink, RouteLink: routeLink, BaseProtocolIdentity: baseIdentity, RouteProtocolIdentity: routeIdentity})
			}
		}
		if _, err := meridian.RenderLinks(entries, item.Account.ID, meridian.FixedMode, routes, true); err != nil {
			return errors.New("center: imported base64 subscription is invalid")
		}
		if _, err := meridian.RenderMihomo(entries, item.Account.ID, meridian.FixedMode, routes); err != nil {
			return errors.New("center: imported Mihomo subscription is invalid")
		}
	}
	return nil
}

// preserveLegacyMeridianSubscriptionPublication removes the legacy
// subscription service from normal catalog pruning before the controller
// application is converted to Meridian. Its verified Agent origin remains
// active until the Center authority and every public entry are switched in one
// later transaction.
func (s *Store) preserveLegacyMeridianSubscriptionPublication(ctx context.Context, tx *sql.Tx, controllerApplicationID string) error {
	var serviceID, endpoint, protocol, status, nodeID, serviceAddress string
	err := tx.QueryRowContext(ctx, `SELECT service.id,service.endpoint,service.protocol,service.status,application.node_id,profile.service_address
		FROM services service
		JOIN applications application ON application.id=service.application_id
		JOIN agent_network_profiles profile ON profile.agent_id=application.node_id
		WHERE service.application_id=? AND service.name=? AND service.status<>'stopped'`, controllerApplicationID, meridianSubscriptionServiceName).Scan(&serviceID, &endpoint, &protocol, &status, &nodeID, &serviceAddress)
	if err != nil || protocol != "http" || status != "ready" && status != "publishing" {
		return errors.New("center: the legacy public subscription service is not ready for cutover")
	}
	host, port, splitErr := net.SplitHostPort(endpoint)
	if splitErr != nil || port != strconv.Itoa(landing.SubscriptionPort) || net.ParseIP(host) == nil || host != serviceAddress {
		return errors.New("center: the legacy subscription origin does not match its verified Agent address")
	}
	var publications, readyPublications, colocatedPublications int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status='ready' AND desired_revision=applied_revision THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN entry_node_id=? AND ingress_owner IN ('site_gateway','tunnel_connector') THEN 1 ELSE 0 END),0)
		FROM publications WHERE service_id=? AND status<>'stopped'`, nodeID, serviceID).Scan(&publications, &readyPublications, &colocatedPublications); err != nil {
		return err
	}
	if publications == 0 || readyPublications != publications || colocatedPublications != publications {
		return errors.New("center: every legacy subscription entry must be healthy and colocated with Center before cutover")
	}
	result, err := tx.ExecContext(ctx, `UPDATE services SET source='system',updated_at=? WHERE id=? AND application_id=? AND name=? AND status IN ('ready','publishing')`, s.now().UTC().Format(time.RFC3339Nano), serviceID, controllerApplicationID, meridianSubscriptionServiceName)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: legacy subscription service changed during cutover")
	}
	return nil
}

func (s *Store) queueMeridianCutoverDeployments(ctx context.Context, tx *sql.Tx, controllerApplicationID string, now time.Time) error {
	manifest, err := currentOfficialApplicationManifest(ctx, tx, "meridian", now)
	if err != nil {
		return fmt.Errorf("center: authorize Meridian cutover package: %w", err)
	}
	if len(manifest.Images) != 1 || strings.TrimSpace(manifest.Images[0].Reference) == "" {
		return errors.New("center: Meridian catalog package has no audited Xray image")
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
			return errors.New("center: legacy subscription host is unavailable for Meridian package deployment")
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
			return errors.New("center: target node already has a different Meridian application")
		}
		var serviceAddress string
		if err := tx.QueryRowContext(ctx, `SELECT service_address FROM agent_network_profiles WHERE agent_id=?`, target.nodeID).Scan(&serviceAddress); err != nil || net.ParseIP(serviceAddress) == nil {
			return errors.New("center: Meridian target node has no confirmed private service address")
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
			return errors.New("center: legacy application changed before Meridian deployment was queued")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,service_address,operation,delete_data,state,error,created_at,updated_at,application_id,runtime_generation,pre_dispatch_application_status) VALUES(?,?,?,?,?,'{}',?,'install',0,'pending','',?,?,?,?,'failed')`, deploymentID, target.nodeID, meridianAppKey, manifest.Version, manifestJSON, serviceAddress, stamp, stamp, applicationID, platform.ApplicationRuntimeGeneration); err != nil {
			return fmt.Errorf("center: queue Meridian cutover deployment: %w", err)
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
			return errors.New("center: failed Meridian deployment cannot be safely retried")
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
			return errors.New("center: failed Meridian application changed before retry")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,service_address,operation,delete_data,state,error,created_at,updated_at,application_id,runtime_generation,pre_dispatch_application_status) VALUES(?,?,?,?,?,'{}',?,'install',0,'pending','',?,?,?,?,'failed')`, deploymentID, value.agentID, meridianAppKey, value.version, value.manifest, value.serviceAddress, stamp, stamp, value.applicationID, platform.ApplicationRuntimeGeneration); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, deploymentID, value.agentID, "application.apply", applicationTaskRevision, "queued", "retry verified Meridian cutover package"); err != nil {
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
		(SELECT COUNT(*) FROM meridian_route_grants WHERE enabled=1 AND status='ready' AND runtime_healthy=1 AND desired_revision=applied_revision)`).Scan(&accounts, &credentials, &endpoints, &ready, &published, &retired, &routes, &readyRoutes); err != nil {
		return err
	}
	if accounts != expectedAccounts || credentials != expectedCredentials || endpoints != expectedEndpoints || routes != expectedRoutes || expectedEndpoints == 0 {
		return errors.New("center: Meridian cutover inventory changed after import")
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
			return errors.New("center: Meridian publication phase changed before deployment")
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
			return errors.New("center: Meridian deployment phase changed before verification")
		}
		state = "verify"
	}
	if state == "verify" && ready == expectedEndpoints && published == expectedEndpoints && readyRoutes == expectedRoutes {
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
			return errors.New("center: Meridian verification phase changed before retirement")
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
				return errors.New("center: Meridian retirement phase changed before completion")
			}
		}
	}
	return nil
}

func (s *Store) switchMeridianSubscriptionPublication(ctx context.Context, tx *sql.Tx, controllerApplicationID string, now time.Time) error {
	var serviceID, siteID, nodeID string
	err := tx.QueryRowContext(ctx, `SELECT service.id,service.site_id,application.node_id
		FROM services service JOIN applications application ON application.id=service.application_id
		WHERE service.application_id=? AND application.app_key IN (?,?) AND application.status='running'
		AND service.name=? AND service.source='system' AND service.status IN ('ready','publishing')`, controllerApplicationID, threeXUIAppKey, meridianAppKey, meridianSubscriptionServiceName).Scan(&serviceID, &siteID, &nodeID)
	if err != nil {
		return errors.New("center: preserved subscription service is unavailable for Meridian authority")
	}
	centerEndpoint := net.JoinHostPort(dockerruntime.CenterAlias, "8080")
	stamp := now.UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE services SET protocol='http',container_port=8080,host_port=8080,endpoint=?,source='system',app_protocol='meridian/subscription',management=0,status='ready',last_error='',updated_at=?
		WHERE id=? AND application_id=? AND name=? AND source='system' AND status IN ('ready','publishing')`, centerEndpoint, stamp, serviceID, controllerApplicationID, meridianSubscriptionServiceName)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("center: subscription service changed before Meridian authority switch")
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
			return errors.New("center: subscription publication changed before Meridian authority switch")
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
		return false, errors.New("center: Meridian subscription authority has an unexpected public origin")
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
	rows, err := tx.QueryContext(ctx, `SELECT endpoint.id FROM meridian_endpoints endpoint
		JOIN applications application ON application.id=endpoint.application_id
		WHERE endpoint.legacy_retired=0 AND endpoint.status='ready' AND endpoint.runtime_healthy=1
		AND endpoint.desired_revision=endpoint.applied_revision
		AND NOT EXISTS(
			SELECT 1 FROM meridian_route_grants route
			WHERE route.endpoint_id=endpoint.id AND route.enabled=1 AND route.status<>'revoked'
			AND (route.status<>'ready' OR route.runtime_healthy<>1 OR route.desired_revision<>route.applied_revision)
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
		ORDER BY endpoint.id`, meridianAppKey, meridianruntime.ApplyKind)
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
			return errors.New("center: Meridian retirement authorization is unavailable")
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
			return errors.New("center: verified Meridian endpoint changed before legacy retirement was queued")
		}
		if err := s.recordTaskEvent(ctx, tx, commandID, projection.agentID, "application.command", int64(projection.task.Desired.Revision), "queued", "Meridian legacy runtime retirement queued"); err != nil {
			return err
		}
	}
	var controllerApplicationID, controllerAgentID, controllerSiteID, controllerName, controllerAppKey, controllerStatus string
	if err := tx.QueryRowContext(ctx, `SELECT application.id,application.node_id,application.site_id,application.name,application.app_key,application.status
		FROM meridian_cutover cutover JOIN applications application ON application.id=cutover.legacy_controller_application_id
		WHERE cutover.id=1`).Scan(&controllerApplicationID, &controllerAgentID, &controllerSiteID, &controllerName, &controllerAppKey, &controllerStatus); err != nil {
		return errors.New("center: Meridian legacy subscription host is unavailable for retirement")
	}
	if controllerAppKey != meridianAppKey || controllerStatus != "running" {
		return errors.New("center: Meridian legacy subscription host package has not converged")
	}
	var controllerHasEndpoint int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_endpoints WHERE application_id=?)`, controllerApplicationID).Scan(&controllerHasEndpoint); err != nil {
		return err
	}
	if controllerHasEndpoint == 0 {
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
			if err := s.recordTaskEvent(ctx, tx, commandID, controllerAgentID, "application.command", 1, "queued", "Meridian legacy subscription host retirement queued"); err != nil {
				return err
			}
		}
	}
	return nil
}

func meridianControllerLegacyRetired(ctx context.Context, tx *sql.Tx) (bool, error) {
	var controllerApplicationID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(legacy_controller_application_id,'') FROM meridian_cutover WHERE id=1`).Scan(&controllerApplicationID); err != nil || controllerApplicationID == "" {
		return false, errors.New("center: Meridian legacy subscription host identity is unavailable")
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

func (s *Store) completeMeridianLegacyRetirement(ctx context.Context, commit projectionCommit, tx *sql.Tx, taskID, agentID string, inputJSON []byte, succeeded bool, taskError string, rawResult json.RawMessage) error {
	var task meridianruntime.LegacyRetireTask
	var envelope struct {
		MeridianLegacyRetire *meridianruntime.LegacyRetireResult `json:"meridianLegacyRetire"`
	}
	if json.Unmarshal(inputJSON, &task) != nil || task.Validate() != nil {
		return errors.New("center: stored Meridian legacy retirement task is invalid")
	}
	if succeeded && (json.Unmarshal(rawResult, &envelope) != nil || envelope.MeridianLegacyRetire == nil || envelope.MeridianLegacyRetire.Validate() != nil) {
		succeeded, taskError = false, "center: Agent did not confirm legacy subscription host retirement"
	}
	if !succeeded && strings.TrimSpace(taskError) == "" {
		taskError = "Meridian legacy subscription host retirement failed"
	}
	state, event, message := "succeeded", "succeeded", "Meridian legacy subscription host retired"
	publicResult := []byte(`{}`)
	if !succeeded {
		state, event, message = "failed", "failed", taskError
	} else {
		publicResult, _ = json.Marshal(envelope.MeridianLegacyRetire)
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	updated, err := tx.ExecContext(ctx, `UPDATE application_commands SET state=?,result_json=?,lease_expires_at='',error=?,updated_at=? WHERE id=? AND state='running'`, state, publicResult, taskError, stamp, taskID)
	if err != nil {
		return err
	}
	if changed, err := updated.RowsAffected(); err != nil || changed != 1 {
		return errors.New("center: Meridian legacy retirement command is not active")
	}
	if !succeeded {
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_cutover SET last_error=?,updated_at=? WHERE id=1 AND state='retire'`, taskError, stamp); err != nil {
			return err
		}
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.command", 1, event, message); err != nil {
		return err
	}
	if succeeded {
		if err := s.reconcileMeridianCutoverInTx(ctx, tx, stamp); err != nil {
			return err
		}
	}
	return commit(tx)
}
