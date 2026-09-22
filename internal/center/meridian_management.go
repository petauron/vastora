package center

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/meridianruntime"
)

type MeridianEndpointInput struct {
	ApplicationID  string `json:"applicationId"`
	VerificationID string `json:"verificationId"`
	TargetIP       string `json:"targetIp"`
	AdvertiseHost  string `json:"advertiseHost"`
	TargetHost     string `json:"targetHost"`
	ServerName     string `json:"serverName"`
	Fingerprint    string `json:"fingerprint"`
	RegionCode     string `json:"regionCode"`
	Name           string `json:"name"`
}

type MeridianEndpointNameInput struct {
	RegionCode string `json:"regionCode"`
	Name       string `json:"name"`
}

type MeridianEndpointRecoveryInput struct {
	ConfirmCenterAuthority bool `json:"confirmCenterAuthority"`
	ExecutionStopped       bool `json:"executionStopped"`
}

type MeridianEndpointView struct {
	ID              string   `json:"id"`
	ApplicationID   string   `json:"applicationId"`
	ApplicationName string   `json:"applicationName"`
	DisplayName     string   `json:"displayName"`
	RegionCode      string   `json:"regionCode,omitempty"`
	NodeID          string   `json:"nodeId"`
	ServiceID       string   `json:"serviceId"`
	AdvertiseHost   string   `json:"advertiseHost"`
	AdvertisePort   int      `json:"advertisePort"`
	Target          string   `json:"target"`
	ServerNames     []string `json:"serverNames"`
	PublicKey       string   `json:"publicKey"`
	ShortIDs        []string `json:"shortIds"`
	Fingerprint     string   `json:"fingerprint"`
	VLESS           bool     `json:"vless"`
	HY2             bool     `json:"hy2"`
	HY2ServerName   string   `json:"hy2ServerName,omitempty"`
	DesiredRevision int64    `json:"desiredRevision"`
	AppliedRevision int64    `json:"appliedRevision"`
	RuntimeHealthy  bool     `json:"runtimeHealthy"`
	LegacyRetired   bool     `json:"legacyRetired"`
	Status          string   `json:"status"`
	LastError       string   `json:"lastError,omitempty"`
	UpdatedAt       string   `json:"updatedAt"`
}

type MeridianAccountInput struct {
	DisplayName string `json:"displayName"`
	TotalBytes  int64  `json:"totalBytes"`
	ExpiryTime  int64  `json:"expiryTime"`
	ResetDays   int    `json:"resetDays"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

type MeridianAccountView struct {
	ID               string `json:"id"`
	DisplayName      string `json:"displayName"`
	TotalBytes       int64  `json:"totalBytes"`
	UsedBytes        int64  `json:"usedBytes"`
	RemainingBytes   int64  `json:"remainingBytes"`
	ExpiryTime       int64  `json:"expiryTime"`
	ResetDays        int    `json:"resetDays"`
	NextResetAt      string `json:"nextResetAt,omitempty"`
	LastResetAt      string `json:"lastResetAt,omitempty"`
	Enabled          bool   `json:"enabled"`
	DesiredRevision  int64  `json:"desiredRevision"`
	AppliedRevision  int64  `json:"appliedRevision"`
	Status           string `json:"status"`
	LastError        string `json:"lastError,omitempty"`
	CredentialCount  int    `json:"credentialCount"`
	RouteCount       int    `json:"routeCount"`
	SubscriptionPath string `json:"subscriptionPath,omitempty"`
	UpdatedAt        string `json:"updatedAt"`
}

type MeridianAccountCreated struct {
	Account           MeridianAccountView `json:"account"`
	SubscriptionToken string              `json:"subscriptionToken"`
	SubscriptionPath  string              `json:"subscriptionPath"`
}

type MeridianRouteGrantInput struct {
	AccountID    string `json:"accountId"`
	EndpointID   string `json:"endpointId"`
	EgressNodeID string `json:"egressNodeId"`
	HideNative   bool   `json:"hideNative"`
}

type MeridianRouteGrantView struct {
	ID              string `json:"id"`
	AccountID       string `json:"accountId"`
	EndpointID      string `json:"endpointId"`
	EgressNodeID    string `json:"egressNodeId"`
	EgressNodeName  string `json:"egressNodeName"`
	HideNative      bool   `json:"hideNative"`
	Enabled         bool   `json:"enabled"`
	DesiredRevision int64  `json:"desiredRevision"`
	AppliedRevision int64  `json:"appliedRevision"`
	RuntimeHealthy  bool   `json:"runtimeHealthy"`
	Status          string `json:"status"`
	LastError       string `json:"lastError,omitempty"`
	UpdatedAt       string `json:"updatedAt"`
}

type MeridianInventoryView struct {
	Cutover   MeridianCutoverView      `json:"cutover"`
	Endpoints []MeridianEndpointView   `json:"endpoints"`
	Accounts  []MeridianAccountView    `json:"accounts"`
	Grants    []MeridianRouteGrantView `json:"grants"`
}

func (s *Store) MeridianInventory(ctx context.Context) (MeridianInventoryView, error) {
	cutover, err := s.MeridianCutover(ctx)
	if err != nil {
		return MeridianInventoryView{}, err
	}
	endpoints, err := s.listMeridianEndpoints(ctx)
	if err != nil {
		return MeridianInventoryView{}, err
	}
	accounts, err := s.listMeridianAccounts(ctx)
	if err != nil {
		return MeridianInventoryView{}, err
	}
	grants, err := s.listMeridianRouteGrants(ctx)
	if err != nil {
		return MeridianInventoryView{}, err
	}
	return MeridianInventoryView{Cutover: cutover, Endpoints: endpoints, Accounts: accounts, Grants: grants}, nil
}

func ensureMeridianManagementWritable(ctx context.Context, tx *sql.Tx) error {
	var state, authority string
	if err := tx.QueryRowContext(ctx, `SELECT state,subscription_authority FROM meridian_cutover WHERE id=1`).Scan(&state, &authority); err != nil {
		return err
	}
	if authority != "meridian" || state != "not_required" && state != "complete" {
		return errors.New("center: Meridian management is locked until the explicit cutover completes")
	}
	return nil
}

func (s *Store) listMeridianEndpoints(ctx context.Context) ([]MeridianEndpointView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT endpoint.id,endpoint.application_id,application.name,service.display_name,service.region_code,application.node_id,endpoint.service_id,
		endpoint.advertise_host,endpoint.advertise_port,endpoint.target,endpoint.server_names_json,endpoint.public_key,
		endpoint.short_ids_json,endpoint.fingerprint,endpoint.vless_enabled,endpoint.hy2_enabled,endpoint.hy2_server_name,endpoint.desired_revision,endpoint.applied_revision,endpoint.runtime_healthy,endpoint.legacy_retired,
		endpoint.status,endpoint.last_error,endpoint.updated_at
		FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id
		JOIN services service ON service.id=endpoint.service_id
		WHERE endpoint.status<>'retired' ORDER BY application.name,endpoint.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []MeridianEndpointView{}
	for rows.Next() {
		var value MeridianEndpointView
		var names, shortIDs []byte
		var healthy, legacyRetired, vlessEnabled, hy2Enabled int
		if err := rows.Scan(&value.ID, &value.ApplicationID, &value.ApplicationName, &value.DisplayName, &value.RegionCode, &value.NodeID, &value.ServiceID,
			&value.AdvertiseHost, &value.AdvertisePort, &value.Target, &names, &value.PublicKey, &shortIDs,
			&value.Fingerprint, &vlessEnabled, &hy2Enabled, &value.HY2ServerName, &value.DesiredRevision, &value.AppliedRevision, &healthy, &legacyRetired, &value.Status, &value.LastError, &value.UpdatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal(names, &value.ServerNames) != nil || json.Unmarshal(shortIDs, &value.ShortIDs) != nil {
			return nil, errors.New("center: stored Meridian endpoint metadata is invalid")
		}
		value.RuntimeHealthy = healthy == 1
		value.LegacyRetired = legacyRetired == 1
		value.VLESS, value.HY2 = vlessEnabled == 1, hy2Enabled == 1
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) UpdateMeridianEndpointName(ctx context.Context, endpointID string, input MeridianEndpointNameInput) (MeridianEndpointView, error) {
	endpointID = strings.TrimSpace(endpointID)
	region, _, displayName, err := composeRealityDisplayName(input.RegionCode, input.Name)
	if err != nil || !meridian.ValidIdentifier(endpointID) {
		return MeridianEndpointView{}, errors.New("center: Meridian endpoint, region, and a valid node name are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return MeridianEndpointView{}, err
	}
	var serviceID, siteID, appKey, applicationStatus, endpointStatus string
	if err := tx.QueryRowContext(ctx, `SELECT endpoint.service_id,service.site_id,application.app_key,application.status,endpoint.status
		FROM meridian_endpoints endpoint JOIN services service ON service.id=endpoint.service_id
		JOIN applications application ON application.id=endpoint.application_id WHERE endpoint.id=?`, endpointID).Scan(&serviceID, &siteID, &appKey, &applicationStatus, &endpointStatus); err != nil {
		return MeridianEndpointView{}, errors.New("center: Meridian endpoint was not found")
	}
	if appKey != meridianAppKey || applicationStatus != "running" || endpointStatus == "retired" {
		return MeridianEndpointView{}, errors.New("center: Meridian endpoint is unavailable")
	}
	var duplicates int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE id<>? AND app_protocol=? AND status<>'stopped' AND display_name=? COLLATE NOCASE`, serviceID, meridianEntryProtocol, displayName).Scan(&duplicates); err != nil {
		return MeridianEndpointView{}, err
	}
	if duplicates != 0 {
		return MeridianEndpointView{}, errors.New("center: a Meridian subscription node already uses that display name")
	}
	if err := ensureRealityDisplayNameUnreserved(ctx, tx, siteID, displayName); err != nil {
		return MeridianEndpointView{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, `UPDATE services SET display_name=?,region_code=?,updated_at=? WHERE id=? AND app_protocol=? AND status<>'stopped'`, displayName, region, now, serviceID, meridianEntryProtocol)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return MeridianEndpointView{}, errors.New("center: Meridian endpoint changed while renaming")
	}
	if err := tx.Commit(); err != nil {
		return MeridianEndpointView{}, err
	}
	values, err := s.listMeridianEndpoints(ctx)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	for _, value := range values {
		if value.ID == endpointID {
			return value, nil
		}
	}
	return MeridianEndpointView{}, errors.New("center: renamed Meridian endpoint is unavailable")
}

// RecoverMeridianEndpoint is the explicit escape hatch for an uncertain
// Agent journal. It never adopts arbitrary runtime configuration into the
// account and route model. Instead it releases only the matching failed
// execution fence and queues a task that is authorized to replace the pending
// Agent state with the current complete Center projection.
func (s *Store) RecoverMeridianEndpoint(ctx context.Context, endpointID, adminID string, input MeridianEndpointRecoveryInput) error {
	endpointID, adminID = strings.TrimSpace(endpointID), strings.TrimSpace(adminID)
	if endpointID == "" || adminID == "" || !input.ConfirmCenterAuthority || !input.ExecutionStopped {
		return errors.New("center: confirm Center authority and that the previous Meridian execution has stopped")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?)`, adminID).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return errors.New("center: administrator authorization required")
	}
	var nodeID, endpointStatus, applicationStatus string
	var desiredRevision, appliedRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT application.node_id,endpoint.status,application.status,endpoint.desired_revision,endpoint.applied_revision
		FROM meridian_endpoints endpoint JOIN applications application ON application.id=endpoint.application_id
		WHERE endpoint.id=? AND application.app_key=?`, endpointID, meridianAppKey).Scan(&nodeID, &endpointStatus, &applicationStatus, &desiredRevision, &appliedRevision); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: Meridian endpoint was not found")
	} else if err != nil {
		return err
	}
	if applicationStatus != "running" || endpointStatus != "failed" || desiredRevision <= appliedRevision {
		return errors.New("center: Meridian endpoint does not have a failed unapplied revision")
	}

	var commandID string
	var commandAttempt int64
	var reconciliationRequired int
	err = tx.QueryRowContext(ctx, `SELECT id,attempt,reconciliation_required FROM application_commands
		WHERE agent_id=? AND kind=? AND json_extract(input_json,'$.endpointId')=?
		ORDER BY created_at DESC,rowid DESC LIMIT 1`, nodeID, meridianruntime.ApplyKind, endpointID).Scan(&commandID, &commandAttempt, &reconciliationRequired)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if reconciliationRequired == 1 {
		var executionID string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM task_executions WHERE agent_id=? AND task_id=? AND attempt=?
			AND state IN ('failed','unknown') AND disposition='' ORDER BY created_at DESC LIMIT 1`, nodeID, commandID, commandAttempt).Scan(&executionID); errors.Is(err, sql.ErrNoRows) {
			return errors.New("center: Meridian recovery execution evidence is unavailable")
		} else if err != nil {
			return err
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		result, err := tx.ExecContext(ctx, `UPDATE task_executions SET disposition='meridian-center-recovery',
			disposition_note='Operator confirmed the previous execution stopped and selected current Center state',
			disposition_actor=?,disposed_at=?,updated_at=? WHERE id=? AND disposition='' AND state IN ('failed','unknown')`, adminID, now, now, executionID)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian recovery execution changed; inspect again")
		}
		updated, err := tx.ExecContext(ctx, `UPDATE application_commands SET reconciliation_required=0,reconciliation_requested=0,lease_expires_at='',updated_at=?
			WHERE id=? AND agent_id=? AND state='failed' AND reconciliation_required=1`, now, commandID, nodeID)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian recovery command changed; inspect again")
		}
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded'`, nodeID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved != 0 {
		return errors.New("center: resolve the node's other uncertain execution before Meridian recovery")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1)`, nodeID).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return errors.New("center: wait for the node's active operation before Meridian recovery")
	}
	if _, err := s.queueMeridianRuntime(ctx, tx, endpointID, true); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.taskChanges.notify("agent:" + nodeID)
	return nil
}

func (s *Store) listMeridianAccounts(ctx context.Context) ([]MeridianAccountView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT account.id,account.display_name,account.total_bytes,account.expiry_time,account.reset_days,account.next_reset_at,account.last_reset_at,
		account.enabled,account.desired_revision,account.applied_revision,account.status,account.last_error,account.updated_at,
		(SELECT COUNT(*) FROM meridian_credentials credential WHERE credential.account_id=account.id),
		(SELECT COUNT(*) FROM meridian_route_grants grant_row WHERE grant_row.account_id=account.id AND grant_row.status<>'revoked')
		FROM meridian_accounts account ORDER BY account.display_name,account.id`)
	if err != nil {
		return nil, err
	}
	values := []MeridianAccountView{}
	for rows.Next() {
		var value MeridianAccountView
		var enabled int
		if err := rows.Scan(&value.ID, &value.DisplayName, &value.TotalBytes, &value.ExpiryTime, &value.ResetDays, &value.NextResetAt, &value.LastResetAt, &enabled,
			&value.DesiredRevision, &value.AppliedRevision, &value.Status, &value.LastError, &value.UpdatedAt,
			&value.CredentialCount, &value.RouteCount); err != nil {
			return nil, err
		}
		value.Enabled = enabled == 1
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range values {
		value := &values[index]
		usage, err := s.meridianAccountUsage(ctx, s.db, value.ID)
		if err != nil {
			return nil, err
		}
		projection, err := meridian.ProjectQuota(meridian.AccountPlan{TotalBytes: value.TotalBytes, ExpiryTime: value.ExpiryTime, ResetDays: value.ResetDays, Enabled: value.Enabled && value.Status == "active"}, usage)
		if err != nil {
			return nil, err
		}
		value.UsedBytes = projection.UsedBytes
		if value.TotalBytes > projection.UsedBytes {
			value.RemainingBytes = value.TotalBytes - projection.UsedBytes
		}
	}
	return values, nil
}

func (s *Store) listMeridianRouteGrants(ctx context.Context) ([]MeridianRouteGrantView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT grant_row.id,grant_row.account_id,grant_row.endpoint_id,grant_row.egress_node_id,agent.name,
		grant_row.hide_native,grant_row.enabled,grant_row.desired_revision,grant_row.applied_revision,grant_row.runtime_healthy,
		grant_row.status,grant_row.last_error,grant_row.updated_at,grant_row.health_expires_unix_ms
		FROM meridian_route_grants grant_row JOIN agents agent ON agent.id=grant_row.egress_node_id
		WHERE grant_row.status<>'revoked' ORDER BY grant_row.account_id,agent.name,grant_row.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []MeridianRouteGrantView{}
	for rows.Next() {
		var value MeridianRouteGrantView
		var hideNative, enabled, healthy int
		var healthExpires int64
		if err := rows.Scan(&value.ID, &value.AccountID, &value.EndpointID, &value.EgressNodeID, &value.EgressNodeName,
			&hideNative, &enabled, &value.DesiredRevision, &value.AppliedRevision, &healthy,
			&value.Status, &value.LastError, &value.UpdatedAt, &healthExpires); err != nil {
			return nil, err
		}
		value.HideNative, value.Enabled = hideNative == 1, enabled == 1
		value.RuntimeHealthy = value.Enabled && healthy == 1 && value.Status == "ready" &&
			value.DesiredRevision == value.AppliedRevision && healthExpires > s.now().UnixMilli()
		if value.Status == "ready" && !value.RuntimeHealthy {
			// Read-time expiry prevents a disconnected Agent's last successful
			// receipt from presenting a permanently healthy route in management.
			value.Status = "blocked"
			if value.LastError == "" {
				value.LastError = "route health evidence is unavailable or expired"
			}
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) CreateMeridianEndpoint(ctx context.Context, input MeridianEndpointInput) (MeridianEndpointView, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.VerificationID = strings.TrimSpace(input.VerificationID)
	input.TargetIP = strings.TrimSpace(input.TargetIP)
	input.AdvertiseHost = strings.TrimSpace(input.AdvertiseHost)
	input.TargetHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.TargetHost), "."))
	input.ServerName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.ServerName), "."))
	input.Fingerprint = strings.TrimSpace(input.Fingerprint)
	region, _, displayName, nameErr := composeRealityDisplayName(input.RegionCode, input.Name)
	if input.Fingerprint == "" {
		input.Fingerprint = "chrome"
	}
	if nameErr != nil || input.ApplicationID == "" || input.VerificationID == "" || !isPublicPublicationVerificationIP(net.ParseIP(input.TargetIP)) || !validRealityTargetHostname(input.TargetHost) || !validRealityTargetHostname(input.ServerName) {
		return MeridianEndpointView{}, errors.New("center: Meridian application, unique region-prefixed node name, and a completed node-side REALITY target check are required")
	}
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return MeridianEndpointView{}, fmt.Errorf("center: generate Meridian REALITY key: %w", err)
	}
	shortIDRaw := make([]byte, 8)
	if _, err := rand.Read(shortIDRaw); err != nil {
		return MeridianEndpointView{}, fmt.Errorf("center: generate Meridian short ID: %w", err)
	}
	endpointID, err := randomToken(18)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	serviceID, err := randomToken(18)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	inboundToken, err := randomToken(12)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	privateValue := base64.RawURLEncoding.EncodeToString(privateKey.Bytes())
	publicValue := base64.RawURLEncoding.EncodeToString(privateKey.PublicKey().Bytes())
	endpoint := meridian.RealityEndpoint{ID: endpointID, EntryID: input.ApplicationID, InboundTag: "meridian-" + inboundToken,
		ListenPort: meridian.DefaultRealityPort, AdvertiseHost: input.AdvertiseHost, AdvertisePort: meridian.DefaultRealityPort,
		PrivateKey: privateValue, PublicKey: publicValue,
		ShortIDs: []string{hex.EncodeToString(shortIDRaw)}, Fingerprint: input.Fingerprint}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return MeridianEndpointView{}, err
	}
	var siteID, nodeID, serviceAddress, publicAddress, status, appKey string
	if err := tx.QueryRowContext(ctx, `SELECT application.site_id,application.node_id,
		COALESCE(profile.service_address,''),COALESCE(profile.public_address,''),
		application.status,application.app_key
		FROM applications application JOIN agents agent ON agent.id=application.node_id
		LEFT JOIN agent_network_profiles profile ON profile.agent_id=agent.id WHERE application.id=?`, input.ApplicationID).Scan(
		&siteID, &nodeID, &serviceAddress, &publicAddress, &status, &appKey); err != nil {
		return MeridianEndpointView{}, errors.New("center: Meridian application was not found")
	}
	if appKey != meridianAppKey || status != "running" || net.ParseIP(serviceAddress) == nil {
		return MeridianEndpointView{}, errors.New("center: Meridian application is not ready for an endpoint")
	}
	if err := s.ensureAllMeridianSubscriptionSnapshotsInTx(ctx, tx); err != nil {
		return MeridianEndpointView{}, err
	}
	var previousPrivateSecretID string
	var previousHY2CertificateSecretID, previousHY2PrivateKeySecretID sql.NullString
	reactivate := false
	retiredErr := tx.QueryRowContext(ctx, `SELECT id,service_id,private_key_secret_id,hy2_certificate_secret_id,hy2_private_key_secret_id
		FROM meridian_endpoints WHERE application_id=? AND status='retired'`, input.ApplicationID).Scan(
		&endpointID, &serviceID, &previousPrivateSecretID, &previousHY2CertificateSecretID, &previousHY2PrivateKeySecretID,
	)
	if retiredErr == nil {
		reactivate = true
		endpoint.ID = endpointID
	} else if !errors.Is(retiredErr, sql.ErrNoRows) {
		return MeridianEndpointView{}, retiredErr
	}
	if _, _, err := validateNodeDirectPublicIngress(ctx, tx, nodeID); err != nil {
		return MeridianEndpointView{}, err
	}
	var duplicates int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE app_protocol=? AND status<>'stopped' AND display_name=? COLLATE NOCASE`, meridianEntryProtocol, displayName).Scan(&duplicates); err != nil {
		return MeridianEndpointView{}, err
	}
	if duplicates != 0 {
		return MeridianEndpointView{}, errors.New("center: a Meridian subscription node already uses that display name")
	}
	if err := ensureRealityDisplayNameUnreserved(ctx, tx, siteID, displayName); err != nil {
		return MeridianEndpointView{}, err
	}
	verified, err := s.selectedRealityTarget(ctx, tx, RealityCommandInput{
		ApplicationID:  input.ApplicationID,
		VerificationID: input.VerificationID,
		TargetIP:       input.TargetIP,
		TargetHost:     input.TargetHost,
		ServerName:     input.ServerName,
	}, nodeID, serviceAddress, publicAddress)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	endpoint.Target = net.JoinHostPort(verified.TargetHost, "443")
	endpoint.ServerNames = []string{verified.ServerName}
	if endpoint.AdvertiseHost == "" {
		if reactivate {
			var previousHostname string
			var cleanupPending int
			err := tx.QueryRowContext(ctx, `SELECT hostname,cleanup_pending FROM publications
				WHERE service_id=? AND kind=? AND status='stopped' ORDER BY updated_at DESC LIMIT 1`, serviceID, publicationShared443).Scan(&previousHostname, &cleanupPending)
			if err == nil {
				if cleanupPending != 0 {
					return MeridianEndpointView{}, errors.New("center: the previous Meridian publication is still removing external resources")
				}
				if domainSuffixPattern.MatchString(previousHostname) {
					endpoint.AdvertiseHost = previousHostname
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return MeridianEndpointView{}, err
			}
		}
		if endpoint.AdvertiseHost == "" {
			var zone string
			if err := tx.QueryRowContext(ctx, `SELECT domain_suffix FROM sites WHERE id=?`, siteID).Scan(&zone); err != nil {
				return MeridianEndpointView{}, err
			}
			endpoint.AdvertiseHost, err = randomPublicationHostnameInZone(ctx, tx, zone)
			if err != nil {
				return MeridianEndpointView{}, err
			}
		}
	} else if !domainSuffixPattern.MatchString(endpoint.AdvertiseHost) {
		return MeridianEndpointView{}, errors.New("center: Meridian public entry requires a valid hostname")
	}
	if endpoint.Validate() != nil {
		return MeridianEndpointView{}, errors.New("center: Meridian endpoint settings are invalid")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if reactivate {
		result, err := tx.ExecContext(ctx, `UPDATE services SET site_id=?,name='inbound-1',display_name=?,region_code=?,protocol='tcp',container_port=443,host_port=443,
			endpoint=?,source='observed',app_protocol=?,management=0,observed_listen='0.0.0.0',status='pending',last_error='',updated_at=?
			WHERE id=? AND application_id=? AND status='stopped'`, siteID, displayName, region, net.JoinHostPort(serviceAddress, "443"), meridianEntryProtocol, now, serviceID, input.ApplicationID)
		if err != nil {
			return MeridianEndpointView{}, fmt.Errorf("center: restore Meridian service: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return MeridianEndpointView{}, errors.New("center: retired Meridian service is not reusable")
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,region_code,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'tcp',443,443,?,'observed',?,0,'0.0.0','pending',?,?)`, serviceID, input.ApplicationID, siteID, "inbound-1", displayName, region, net.JoinHostPort(serviceAddress, "443"), meridianEntryProtocol, now, now); err != nil {
		return MeridianEndpointView{}, fmt.Errorf("center: create Meridian service: %w", err)
	}
	secretID, err := s.putSecret(ctx, tx, []byte(privateValue), meridianEndpointSecretContext(endpointID))
	if err != nil {
		return MeridianEndpointView{}, err
	}
	names, _ := json.Marshal(endpoint.ServerNames)
	shortIDs, _ := json.Marshal(endpoint.ShortIDs)
	if reactivate {
		result, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET service_id=?,inbound_tag=?,listen_port=?,advertise_host=?,advertise_port=?,target=?,target_ip=?,
			server_names_json=?,private_key_secret_id=?,public_key=?,short_ids_json=?,fingerprint=?,vless_enabled=1,hy2_enabled=0,hy2_inbound_tag='',hy2_server_name='',
			hy2_certificate_secret_id=NULL,hy2_private_key_secret_id=NULL,hy2_certificate_not_after='',desired_revision=desired_revision+1,applied_revision=0,
			runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=? AND application_id=? AND status='retired'`, serviceID, endpoint.InboundTag, endpoint.ListenPort,
			endpoint.AdvertiseHost, endpoint.AdvertisePort, endpoint.Target, verified.TargetIP, names, secretID, endpoint.PublicKey, shortIDs, endpoint.Fingerprint, now, endpointID, input.ApplicationID)
		if err != nil {
			return MeridianEndpointView{}, fmt.Errorf("center: restore Meridian endpoint: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return MeridianEndpointView{}, errors.New("center: retired Meridian endpoint is not reusable")
		}
		for _, previousSecretID := range []sql.NullString{{String: previousPrivateSecretID, Valid: previousPrivateSecretID != ""}, previousHY2CertificateSecretID, previousHY2PrivateKeySecretID} {
			if previousSecretID.Valid && previousSecretID.String != secretID {
				if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id=?`, previousSecretID.String); err != nil {
					return MeridianEndpointView{}, err
				}
			}
		}
	} else if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,fingerprint,vless_enabled,hy2_enabled,hy2_inbound_tag,hy2_server_name,hy2_certificate_secret_id,hy2_private_key_secret_id,hy2_certificate_not_after,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, endpointID, input.ApplicationID, serviceID, endpoint.InboundTag, endpoint.ListenPort,
		endpoint.AdvertiseHost, endpoint.AdvertisePort, endpoint.Target, verified.TargetIP, names, secretID, endpoint.PublicKey, shortIDs, endpoint.Fingerprint, 1, 0, "", "", nil, nil, "", "pending", now, now); err != nil {
		return MeridianEndpointView{}, fmt.Errorf("center: create Meridian endpoint: %w", err)
	}
	if err := ensureMeridianEndpointPublication(ctx, tx, serviceID, nodeID, endpoint.AdvertiseHost, verified.ServerName, now); err != nil {
		return MeridianEndpointView{}, err
	}
	if err := s.addExistingAccountsToMeridianEndpoint(ctx, tx, endpointID, input.ApplicationID, now); err != nil {
		return MeridianEndpointView{}, err
	}
	if err := tx.Commit(); err != nil {
		return MeridianEndpointView{}, err
	}
	values, err := s.listMeridianEndpoints(ctx)
	if err != nil {
		return MeridianEndpointView{}, err
	}
	for _, value := range values {
		if value.ID == endpointID {
			return value, nil
		}
	}
	return MeridianEndpointView{}, errors.New("center: created Meridian endpoint is unavailable")
}

func ensureMeridianEndpointPublication(ctx context.Context, tx *sql.Tx, serviceID, nodeID, hostname, serverName, stamp string) error {
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications
		WHERE entry_node_id=? AND sni_hostname=? AND status<>'stopped' AND service_id<>?`, nodeID, serverName, serviceID).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate != 0 {
		return errors.New("center: this REALITY SNI is already published on the Meridian node")
	}

	var publicationID, status string
	var cleanupPending int
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT id,status,cleanup_pending,desired_revision FROM publications
		WHERE service_id=? AND kind=? ORDER BY updated_at DESC LIMIT 1`, serviceID, publicationShared443).Scan(&publicationID, &status, &cleanupPending, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		publicationID, err = randomToken(18)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,status,created_at,updated_at)
			VALUES(?,?,?,'application_node',?,?,?,'manual',0,'pending',?,?)`, publicationID, serviceID, publicationShared443, nodeID, hostname, serverName, stamp, stamp)
		if err != nil {
			return fmt.Errorf("center: create Meridian public entry: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if status != "stopped" {
		return errors.New("center: Meridian public entry already exists")
	}
	if cleanupPending != 0 {
		return errors.New("center: the previous Meridian publication is still removing external resources")
	}
	_, err = tx.ExecContext(ctx, `UPDATE publications SET ingress_owner='application_node',entry_node_id=?,hostname=?,sni_hostname=?,dns_provider='manual',
		dns_record_id='',access_application_id='',tls_enabled=0,desired_revision=?,applied_revision=0,status='pending',last_error='',action_required=0,
		cleanup_pending=0,cleanup_attempt=0,cleanup_retry_at='',updated_at=? WHERE id=?`, nodeID, hostname, serverName, revision+1, stamp, publicationID)
	if err != nil {
		return fmt.Errorf("center: restore Meridian public entry: %w", err)
	}
	return nil
}

func (s *Store) CreateMeridianAccount(ctx context.Context, input MeridianAccountInput) (MeridianAccountCreated, error) {
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	plan := meridian.AccountPlan{TotalBytes: input.TotalBytes, ExpiryTime: input.ExpiryTime, ResetDays: input.ResetDays, Enabled: enabled}
	if input.DisplayName == "" || len([]rune(input.DisplayName)) > meridian.MaxDisplayNameLength || plan.Validate() != nil || input.ExpiryTime > 0 && input.ExpiryTime <= s.now().UTC().UnixMilli() {
		return MeridianAccountCreated{}, errors.New("center: invalid Meridian account plan")
	}
	accountID, err := randomToken(18)
	if err != nil {
		return MeridianAccountCreated{}, err
	}
	token, err := randomToken(32)
	if err != nil {
		return MeridianAccountCreated{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MeridianAccountCreated{}, err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return MeridianAccountCreated{}, err
	}
	var endpointCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_endpoints WHERE status<>'retired'`).Scan(&endpointCount); err != nil {
		return MeridianAccountCreated{}, err
	}
	if endpointCount == 0 {
		return MeridianAccountCreated{}, errors.New("center: create a Meridian endpoint before creating accounts")
	}
	if err := s.ensureAllMeridianSubscriptionSnapshotsInTx(ctx, tx); err != nil {
		return MeridianAccountCreated{}, err
	}
	secretID, err := s.putSecret(ctx, tx, []byte(token), meridianAccountSecretContext(accountID))
	if err != nil {
		return MeridianAccountCreated{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	nextReset := ""
	if input.ResetDays > 0 {
		nextReset = s.now().UTC().AddDate(0, 0, input.ResetDays).Format(time.RFC3339Nano)
	}
	status := "active"
	if !enabled {
		status = "disabled"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_accounts(id,display_name,total_bytes,expiry_time,reset_days,next_reset_at,enabled,subscription_token_secret_id,subscription_token_sha256,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, accountID, input.DisplayName, input.TotalBytes, input.ExpiryTime, input.ResetDays, nextReset, boolInt(enabled), secretID, meridian.SubscriptionTokenFingerprint(token), status, now, now); err != nil {
		return MeridianAccountCreated{}, fmt.Errorf("center: create Meridian account: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,application_id FROM meridian_endpoints WHERE status<>'retired' ORDER BY id`)
	if err != nil {
		return MeridianAccountCreated{}, err
	}
	type endpointRef struct{ id, applicationID string }
	refs := []endpointRef{}
	for rows.Next() {
		var ref endpointRef
		if err := rows.Scan(&ref.id, &ref.applicationID); err != nil {
			rows.Close()
			return MeridianAccountCreated{}, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Close(); err != nil {
		return MeridianAccountCreated{}, err
	}
	for _, ref := range refs {
		if _, err := s.insertMeridianCredential(ctx, tx, accountID, ref.id, ref.applicationID, meridian.NativeCredential, "", true, now); err != nil {
			return MeridianAccountCreated{}, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=?`, now, ref.id); err != nil {
			return MeridianAccountCreated{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return MeridianAccountCreated{}, err
	}
	accounts, err := s.listMeridianAccounts(ctx)
	if err != nil {
		return MeridianAccountCreated{}, err
	}
	for _, value := range accounts {
		if value.ID == accountID {
			path := "/sub/" + token
			value.SubscriptionPath = path
			return MeridianAccountCreated{Account: value, SubscriptionToken: token, SubscriptionPath: path}, nil
		}
	}
	return MeridianAccountCreated{}, errors.New("center: created Meridian account is unavailable")
}

func (s *Store) UpdateMeridianAccount(ctx context.Context, accountID string, input MeridianAccountInput) (MeridianAccountView, error) {
	accountID, input.DisplayName = strings.TrimSpace(accountID), strings.TrimSpace(input.DisplayName)
	if input.Enabled == nil {
		return MeridianAccountView{}, errors.New("center: Meridian account enabled state is required")
	}
	plan := meridian.AccountPlan{TotalBytes: input.TotalBytes, ExpiryTime: input.ExpiryTime, ResetDays: input.ResetDays, Enabled: *input.Enabled}
	if !meridian.ValidIdentifier(accountID) || input.DisplayName == "" || len([]rune(input.DisplayName)) > meridian.MaxDisplayNameLength || plan.Validate() != nil || input.ExpiryTime > 0 && input.ExpiryTime <= s.now().UTC().UnixMilli() {
		return MeridianAccountView{}, errors.New("center: invalid Meridian account plan")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MeridianAccountView{}, err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return MeridianAccountView{}, err
	}
	nowTime := s.now().UTC()
	now := nowTime.Format(time.RFC3339Nano)
	var previousResetDays int
	var previousNextReset string
	if err := tx.QueryRowContext(ctx, `SELECT reset_days,next_reset_at FROM meridian_accounts WHERE id=?`, accountID).Scan(&previousResetDays, &previousNextReset); err != nil {
		return MeridianAccountView{}, errors.New("center: Meridian account was not found")
	}
	if err := s.ensureMeridianSubscriptionSnapshotsForAccountEndpointsInTx(ctx, tx, accountID); err != nil {
		return MeridianAccountView{}, err
	}
	nextReset := previousNextReset
	if input.ResetDays == 0 {
		nextReset = ""
	} else if input.ResetDays != previousResetDays || nextReset == "" {
		nextReset = nowTime.AddDate(0, 0, input.ResetDays).Format(time.RFC3339Nano)
	}
	status := "active"
	if !*input.Enabled {
		status = "disabled"
	}
	result, err := tx.ExecContext(ctx, `UPDATE meridian_accounts SET display_name=?,total_bytes=?,expiry_time=?,reset_days=?,next_reset_at=?,enabled=?,status=?,desired_revision=desired_revision+1,last_error='',updated_at=? WHERE id=?`,
		input.DisplayName, input.TotalBytes, input.ExpiryTime, input.ResetDays, nextReset, boolInt(*input.Enabled), status, now, accountID)
	if err != nil {
		return MeridianAccountView{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return MeridianAccountView{}, errors.New("center: Meridian account was not found")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=?
		WHERE status<>'retired' AND id IN (SELECT endpoint_id FROM meridian_credentials WHERE account_id=?)`, now, accountID); err != nil {
		return MeridianAccountView{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET desired_revision=desired_revision+1,runtime_healthy=0,status=CASE WHEN status IN ('revoking','revoked') THEN status ELSE 'pending' END,last_error='',updated_at=? WHERE account_id=?`, now, accountID); err != nil {
		return MeridianAccountView{}, err
	}
	if err := tx.Commit(); err != nil {
		return MeridianAccountView{}, err
	}
	accounts, err := s.listMeridianAccounts(ctx)
	if err != nil {
		return MeridianAccountView{}, err
	}
	for _, value := range accounts {
		if value.ID == accountID {
			return value, nil
		}
	}
	return MeridianAccountView{}, errors.New("center: updated Meridian account is unavailable")
}

func (s *Store) CreateMeridianRouteGrant(ctx context.Context, input MeridianRouteGrantInput) (MeridianRouteGrantView, error) {
	input.AccountID = strings.TrimSpace(input.AccountID)
	input.EndpointID = strings.TrimSpace(input.EndpointID)
	input.EgressNodeID = strings.TrimSpace(input.EgressNodeID)
	if !meridian.ValidIdentifier(input.AccountID) || !meridian.ValidIdentifier(input.EndpointID) || !meridian.ValidIdentifier(input.EgressNodeID) {
		return MeridianRouteGrantView{}, errors.New("center: invalid Meridian route grant")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MeridianRouteGrantView{}, err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return MeridianRouteGrantView{}, err
	}
	var applicationID, baseCredentialID, accountStatus string
	var accountEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT endpoint.application_id,credential.id,account.status,account.enabled
		FROM meridian_endpoints endpoint JOIN meridian_accounts account ON account.id=?
		JOIN meridian_credentials credential ON credential.endpoint_id=endpoint.id AND credential.account_id=account.id AND credential.kind='native'
		WHERE endpoint.id=? AND endpoint.status<>'retired' AND endpoint.vless_enabled=1`, input.AccountID, input.EndpointID).Scan(&applicationID, &baseCredentialID, &accountStatus, &accountEnabled); err != nil {
		return MeridianRouteGrantView{}, errors.New("center: Meridian account or endpoint is unavailable")
	}
	if accountStatus != "active" || accountEnabled != 1 {
		return MeridianRouteGrantView{}, errors.New("center: disabled Meridian account cannot receive a route")
	}
	if err := s.authorizeMeridianEntrySource(ctx, tx, input.EndpointID); err != nil {
		return MeridianRouteGrantView{}, err
	}
	if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, input.EndpointID); err != nil {
		return MeridianRouteGrantView{}, err
	}
	var landingReady int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM landing_server_states server JOIN agents agent ON agent.id=server.node_id
		WHERE server.node_id=? AND server.status='ready' AND server.desired_revision=server.applied_revision
		AND agent.status='active' AND agent.credential_revoked_at='' AND agent.tailscale_ownership='managed' AND agent.last_seen_at>?)`,
		input.EgressNodeID, s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano)).Scan(&landingReady); err != nil || landingReady != 1 {
		return MeridianRouteGrantView{}, errors.New("center: landing runtime is not ready")
	}
	grantID, err := randomToken(18)
	if err != nil {
		return MeridianRouteGrantView{}, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	routeCredentialID, err := s.insertMeridianCredential(ctx, tx, input.AccountID, input.EndpointID, applicationID, meridian.RouteCredential, input.EgressNodeID, true, now)
	if err != nil {
		return MeridianRouteGrantView{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_route_grants(id,account_id,endpoint_id,egress_node_id,base_credential_id,route_credential_id,mode,hide_native,enabled,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'fixed',?,1,'pending',?,?)`, grantID, input.AccountID, input.EndpointID, input.EgressNodeID, baseCredentialID, routeCredentialID, boolInt(input.HideNative), now, now); err != nil {
		return MeridianRouteGrantView{}, fmt.Errorf("center: create Meridian route grant: %w", err)
	}
	if err := s.refreshClientLandingSources(ctx, tx, input.EgressNodeID); err != nil {
		return MeridianRouteGrantView{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=?`, now, input.EndpointID); err != nil {
		return MeridianRouteGrantView{}, err
	}
	if err := tx.Commit(); err != nil {
		return MeridianRouteGrantView{}, err
	}
	grants, err := s.listMeridianRouteGrants(ctx)
	if err != nil {
		return MeridianRouteGrantView{}, err
	}
	for _, value := range grants {
		if value.ID == grantID {
			return value, nil
		}
	}
	return MeridianRouteGrantView{}, errors.New("center: created Meridian route grant is unavailable")
}

func (s *Store) RevokeMeridianRouteGrant(ctx context.Context, grantID string) error {
	grantID = strings.TrimSpace(grantID)
	if !meridian.ValidIdentifier(grantID) {
		return errors.New("center: invalid Meridian route grant")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureMeridianManagementWritable(ctx, tx); err != nil {
		return err
	}
	var endpointID, routeCredentialID, status string
	if err := tx.QueryRowContext(ctx, `SELECT endpoint_id,route_credential_id,status FROM meridian_route_grants WHERE id=?`, grantID).Scan(&endpointID, &routeCredentialID, &status); err != nil {
		return errors.New("center: Meridian route grant was not found")
	}
	if status == "revoked" || status == "revoking" {
		return errors.New("center: Meridian route grant is already being removed")
	}
	if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_credentials SET enabled=0,updated_at=? WHERE id=?`, now, routeCredentialID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET enabled=0,desired_revision=desired_revision+1,runtime_healthy=0,status='revoking',last_error='',updated_at=? WHERE id=?`, now, grantID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,runtime_healthy=0,status='pending',last_error='',updated_at=? WHERE id=?`, now, endpointID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) addExistingAccountsToMeridianEndpoint(ctx context.Context, tx *sql.Tx, endpointID, applicationID, now string) error {
	rows, err := tx.QueryContext(ctx, `SELECT account.id,EXISTS(
		SELECT 1 FROM meridian_credentials credential WHERE credential.account_id=account.id AND credential.endpoint_id=? AND credential.kind='native'
	) FROM meridian_accounts account ORDER BY account.id`, endpointID)
	if err != nil {
		return err
	}
	type accountRef struct {
		id       string
		existing bool
	}
	accounts := []accountRef{}
	for rows.Next() {
		var account accountRef
		if err := rows.Scan(&account.id, &account.existing); err != nil {
			rows.Close()
			return err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, account := range accounts {
		if account.existing {
			if _, err := tx.ExecContext(ctx, `UPDATE meridian_credentials SET enabled=1,updated_at=? WHERE account_id=? AND endpoint_id=? AND kind='native'`, now, account.id, endpointID); err != nil {
				return err
			}
		} else {
			if _, err := s.insertMeridianCredential(ctx, tx, account.id, endpointID, applicationID, meridian.NativeCredential, "", true, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) insertMeridianCredential(ctx context.Context, tx *sql.Tx, accountID, endpointID, applicationID string, kind meridian.CredentialKind, egressNodeID string, enabled bool, now string) (string, error) {
	credentialID, err := randomToken(18)
	if err != nil {
		return "", err
	}
	protocolID := uuid.NewString()
	secretID, err := s.putSecret(ctx, tx, []byte(protocolID), meridianCredentialSecretContext(credentialID))
	if err != nil {
		return "", err
	}
	user := "meridian-" + credentialID
	var hy2SecretID any
	hy2Identity := ""
	var hy2Configured int
	if err := tx.QueryRowContext(ctx, `SELECT hy2_inbound_tag<>'' FROM meridian_endpoints WHERE id=?`, endpointID).Scan(&hy2Configured); err != nil {
		return "", errors.New("center: Meridian endpoint is unavailable")
	}
	if kind == meridian.NativeCredential && hy2Configured == 1 {
		hy2Auth, tokenErr := randomToken(32)
		if tokenErr != nil {
			return "", tokenErr
		}
		value, secretErr := s.putSecret(ctx, tx, []byte(hy2Auth), meridianCredentialHY2SecretContext(credentialID))
		if secretErr != nil {
			return "", secretErr
		}
		hy2SecretID, hy2Identity = value, meridian.Identity(hy2Auth)
	}
	var egress any
	if egressNodeID != "" {
		egress = egressNodeID
	}
	credential := meridian.Credential{ID: credentialID, AccountID: accountID, Kind: kind, User: user, Identity: meridian.Identity(protocolID), EntryID: applicationID, EgressID: egressNodeID, Enabled: enabled}
	if credential.Validate() != nil {
		return "", errors.New("center: generated Meridian credential is invalid")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_credentials(id,account_id,endpoint_id,kind,user_name,identity_sha256,protocol_secret_id,hy2_auth_secret_id,hy2_identity_sha256,egress_node_id,enabled,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, credentialID, accountID, endpointID, kind, user, credential.Identity, secretID, hy2SecretID, hy2Identity, egress, boolInt(enabled), now, now); err != nil {
		return "", fmt.Errorf("center: create Meridian credential: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meridian_usage_watermarks(credential_id,baseline_bytes,observed_bytes,raw_up_bytes,raw_down_bytes,observed_at) VALUES(?,0,0,0,0,?)`, credentialID, now); err != nil {
		return "", err
	}
	return credentialID, nil
}
