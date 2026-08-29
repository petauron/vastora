package center

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

type AgentEnrollment struct {
	Token        string    `json:"token"`
	SiteID       string    `json:"siteId"`
	InstallerURL string    `json:"installerUrl"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type AgentEnrollmentSpec struct {
	SiteID       string
	Name         string
	CenterURL    string
	Gateway      bool
	Tunnel       bool
	UseHeadscale bool
}

type AgentEnrollmentInstallProfile struct {
	Name               string
	CenterURL          string
	Roles              []string
	Capabilities       NodeCapabilities
	HeadscaleCommand   string
	HeadscaleURL       string
	HeadscaleAddresses []string
}

type AgentCredential struct {
	ID           string           `json:"id"`
	Credential   string           `json:"credential"`
	Name         string           `json:"name"`
	Roles        []string         `json:"roles"`
	Capabilities NodeCapabilities `json:"capabilities"`
}

type AgentView struct {
	ID                   string                 `json:"id"`
	Name                 string                 `json:"name"`
	Version              string                 `json:"version"`
	OperatingSystem      string                 `json:"operatingSystem"`
	Architecture         string                 `json:"architecture"`
	Status               string                 `json:"status"`
	AppliedInstallations int                    `json:"appliedInstallations"`
	EnrolledAt           time.Time              `json:"enrolledAt"`
	LastSeenAt           time.Time              `json:"lastSeenAt"`
	Connected            bool                   `json:"connected"`
	SiteID               string                 `json:"siteId"`
	Roles                []string               `json:"roles"`
	Capabilities         NodeCapabilities       `json:"capabilities"`
	NetworkCandidates    []networking.Candidate `json:"networkCandidates"`
	NetworkProfile       *networking.Profile    `json:"networkProfile,omitempty"`
	GatewayHealthy       bool                   `json:"gatewayHealthy"`
	TailscaleOwnership   string                 `json:"tailscaleOwnership"`
}

func (s *Store) CreateAgentEnrollment(ctx context.Context, spec AgentEnrollmentSpec) (AgentEnrollment, error) {
	spec.SiteID = strings.TrimSpace(spec.SiteID)
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" || len(spec.Name) > 128 {
		return AgentEnrollment{}, errors.New("center: agent name must be 1 to 128 characters")
	}
	centerURL, err := NormalizeAgentConnectURL(spec.CenterURL)
	if err != nil {
		return AgentEnrollment{}, err
	}
	roles := []string{"worker"}
	if spec.Gateway {
		roles = append(roles, "gateway")
	}
	capabilities := NodeCapabilities{Docker: true, Gateway: spec.Gateway, Tunnel: spec.Tunnel}
	rolesJSON, _ := json.Marshal(roles)
	capabilitiesJSON, _ := json.Marshal(capabilities)
	if spec.SiteID == "" {
		return AgentEnrollment{}, errors.New("center: enrollment site is required")
	}
	var siteExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id = ? AND status = 'active'`, spec.SiteID).Scan(&siteExists); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: inspect enrollment site: %w", err)
	}
	if siteExists != 1 {
		return AgentEnrollment{}, errors.New("center: enrollment site was not found")
	}
	token, err := randomToken(32)
	if err != nil {
		return AgentEnrollment{}, err
	}
	bootstrapCommand := ""
	if spec.UseHeadscale {
		join, err := s.CreateHeadscaleBootstrap(ctx, spec.Gateway)
		if err != nil {
			return AgentEnrollment{}, err
		}
		bootstrapCommand = join.Command
	}
	installerURL := centerURL
	if spec.UseHeadscale {
		if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&installerURL); err != nil {
			return AgentEnrollment{}, errors.New("center: Headscale is not configured for public Agent bootstrap")
		}
	}
	enrollment := AgentEnrollment{Token: token, SiteID: spec.SiteID, InstallerURL: installerURL, ExpiresAt: s.now().UTC().Add(10 * time.Minute)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: begin agent enrollment: %w", err)
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id IN (SELECT bootstrap_secret_id FROM agent_enrollment_tokens WHERE expires_at <= ? AND bootstrap_secret_id IS NOT NULL)`, now); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: delete expired Agent bootstrap secrets: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollment_tokens WHERE expires_at <= ?`, now); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: delete expired Agent enrollments: %w", err)
	}
	var bootstrapSecretID sql.NullString
	if bootstrapCommand != "" {
		secretID, err := s.putSecret(ctx, tx, []byte(bootstrapCommand), agentEnrollmentSecretContext(token))
		if err != nil {
			return AgentEnrollment{}, err
		}
		bootstrapSecretID = sql.NullString{String: secretID, Valid: true}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_enrollment_tokens(token_hash, site_id, name, center_url, roles_json, capabilities_json, bootstrap_secret_id, expires_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, tokenHash(token), spec.SiteID, spec.Name, centerURL, rolesJSON, capabilitiesJSON, bootstrapSecretID, enrollment.ExpiresAt.Format(time.RFC3339Nano)); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: create agent enrollment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: commit agent enrollment: %w", err)
	}
	return enrollment, nil
}

func (s *Store) AgentEnrollmentInstallProfile(ctx context.Context, token string) (AgentEnrollmentInstallProfile, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return AgentEnrollmentInstallProfile{}, errors.New("center: agent enrollment token is invalid")
	}
	var profile AgentEnrollmentInstallProfile
	var rolesJSON, capabilitiesJSON []byte
	var expiresAt string
	var usedAt, bootstrapSecretID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT name, center_url, roles_json, capabilities_json, bootstrap_secret_id, expires_at, used_at FROM agent_enrollment_tokens WHERE token_hash = ?`, tokenHash(token)).Scan(&profile.Name, &profile.CenterURL, &rolesJSON, &capabilitiesJSON, &bootstrapSecretID, &expiresAt, &usedAt)
	if errors.Is(err, sql.ErrNoRows) || usedAt.Valid {
		return AgentEnrollmentInstallProfile{}, errors.New("center: agent enrollment token is invalid")
	}
	if err != nil {
		return AgentEnrollmentInstallProfile{}, fmt.Errorf("center: read agent enrollment: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return AgentEnrollmentInstallProfile{}, errors.New("center: agent enrollment token has expired")
	}
	if err := decodeAgentEnrollmentRuntime(rolesJSON, capabilitiesJSON, &profile); err != nil {
		return AgentEnrollmentInstallProfile{}, err
	}
	if normalized, err := NormalizeAgentConnectURL(profile.CenterURL); err != nil || normalized != profile.CenterURL {
		return AgentEnrollmentInstallProfile{}, errors.New("center: stored Agent connection URL is invalid")
	}
	if bootstrapSecretID.Valid {
		command, err := s.getSecret(ctx, bootstrapSecretID.String, agentEnrollmentSecretContext(token))
		if err != nil {
			return AgentEnrollmentInstallProfile{}, err
		}
		profile.HeadscaleCommand = string(command)
		isolation, err := s.tailscaleIsolationDesiredState(ctx, "")
		if err != nil {
			return AgentEnrollmentInstallProfile{}, err
		}
		if isolation == nil {
			return AgentEnrollmentInstallProfile{}, errors.New("center: Headscale isolation state is unavailable")
		}
		profile.HeadscaleURL = isolation.ControlURL
		profile.HeadscaleAddresses = append([]string(nil), isolation.ControlAddresses...)
	}
	return profile, nil
}

func decodeAgentEnrollmentRuntime(rolesJSON, capabilitiesJSON []byte, profile *AgentEnrollmentInstallProfile) error {
	if json.Unmarshal(rolesJSON, &profile.Roles) != nil || json.Unmarshal(capabilitiesJSON, &profile.Capabilities) != nil {
		return errors.New("center: stored Agent enrollment profile is invalid")
	}
	if len(profile.Roles) == 0 || len(profile.Roles) > 2 || !containsString(profile.Roles, "worker") {
		return errors.New("center: stored Agent enrollment roles are invalid")
	}
	for _, role := range profile.Roles {
		if role != "worker" && role != "gateway" {
			return errors.New("center: stored Agent enrollment roles are invalid")
		}
	}
	if !profile.Capabilities.Docker || profile.Capabilities.Gateway != containsString(profile.Roles, "gateway") || profile.Capabilities.Metrics || profile.Capabilities.Logs {
		return errors.New("center: stored Agent enrollment capabilities are invalid")
	}
	return nil
}

func agentEnrollmentSecretContext(token string) string {
	return "agent-enrollment:" + hex.EncodeToString(tokenHash(token))
}

func (s *Store) EnrollAgent(ctx context.Context, enrollmentToken, version, operatingSystem, architecture string) (AgentCredential, error) {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 128 {
		return AgentCredential{}, errors.New("center: agent version is required")
	}
	target, err := platform.Parse(operatingSystem, architecture)
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: invalid Agent platform: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: begin agent enrollment: %w", err)
	}
	defer tx.Rollback()
	var expiresAt, siteID, name string
	var rolesJSON, capabilitiesJSON []byte
	var usedAt, bootstrapSecretID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT expires_at, used_at, site_id, name, roles_json, capabilities_json, bootstrap_secret_id FROM agent_enrollment_tokens WHERE token_hash = ?`, tokenHash(enrollmentToken)).Scan(&expiresAt, &usedAt, &siteID, &name, &rolesJSON, &capabilitiesJSON, &bootstrapSecretID)
	if errors.Is(err, sql.ErrNoRows) || usedAt.Valid {
		return AgentCredential{}, errors.New("center: agent enrollment token is invalid")
	}
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: read agent enrollment: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(s.now()) {
		return AgentCredential{}, errors.New("center: agent enrollment token has expired")
	}
	profile := AgentEnrollmentInstallProfile{Name: name}
	if err := decodeAgentEnrollmentRuntime(rolesJSON, capabilitiesJSON, &profile); err != nil {
		return AgentCredential{}, err
	}
	id, err := randomToken(18)
	if err != nil {
		return AgentCredential{}, err
	}
	credential, err := randomToken(32)
	if err != nil {
		return AgentCredential{}, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id, name, credential_hash, version, operating_system, architecture, status, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json) VALUES(?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)`, id, name, tokenHash(credential), version, target.OS, target.Architecture, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), siteID, rolesJSON, capabilitiesJSON); err != nil {
		return AgentCredential{}, fmt.Errorf("center: save agent: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`, now.Format(time.RFC3339Nano), tokenHash(enrollmentToken))
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: consume agent enrollment: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return AgentCredential{}, errors.New("center: agent enrollment token is invalid")
	}
	if bootstrapSecretID.Valid {
		if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, bootstrapSecretID.String); err != nil {
			return AgentCredential{}, fmt.Errorf("center: delete consumed Agent bootstrap secret: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return AgentCredential{}, fmt.Errorf("center: commit agent enrollment: %w", err)
	}
	return AgentCredential{ID: id, Credential: credential, Name: name, Roles: profile.Roles, Capabilities: profile.Capabilities}, nil
}

func (s *Store) RecordAgentHeartbeat(ctx context.Context, id, credential string, heartbeat NodeHeartbeat) error {
	if heartbeat.AppliedInstallations < 0 || heartbeat.AppliedInstallations > 1_000_000 {
		return errors.New("center: invalid applied installation count")
	}
	if heartbeat.ApplicationRuntimeGeneration < 0 || heartbeat.ApplicationRuntimeGeneration > platform.ApplicationRuntimeGeneration {
		return errors.New("center: unsupported Agent application runtime generation")
	}
	if heartbeat.TailscaleOwnership != "managed" && heartbeat.TailscaleOwnership != "external" && heartbeat.TailscaleOwnership != "" {
		return errors.New("center: Agent reported an unsupported Tailscale ownership")
	}
	if err := s.authenticateAgent(ctx, id, credential); err != nil {
		return err
	}
	heartbeat.Roles = uniqueStrings(heartbeat.Roles)
	for _, role := range heartbeat.Roles {
		if role != "worker" && role != "gateway" {
			return errors.New("center: Agent reported an unsupported role")
		}
	}
	if heartbeat.Capabilities.Gateway && !containsString(heartbeat.Roles, "gateway") {
		return errors.New("center: gateway capability requires gateway role")
	}
	if heartbeat.Capabilities.Docker && !containsString(heartbeat.Roles, "worker") {
		return errors.New("center: Docker capability requires worker role")
	}
	if heartbeat.Capabilities.Tunnel && !containsString(heartbeat.Roles, "worker") {
		return errors.New("center: tunnel capability requires worker role")
	}
	if len(heartbeat.NetworkCandidates) > 128 {
		return errors.New("center: Agent reported too many network addresses")
	}
	if len(heartbeat.ApplicationEndpoints) > 512 {
		return errors.New("center: Agent reported too many application endpoints")
	}
	if !heartbeat.ApplicationEndpointsObserved && len(heartbeat.ApplicationEndpoints) != 0 {
		return errors.New("center: Agent reported application endpoints without a complete observation")
	}
	reportedNetworkCandidates := len(heartbeat.NetworkCandidates)
	seenAddresses := map[string]bool{}
	filteredCandidates := make([]networking.Candidate, 0, len(heartbeat.NetworkCandidates))
	reportedHeadscaleAddress := false
	for _, candidate := range heartbeat.NetworkCandidates {
		ip := net.ParseIP(candidate.Address)
		if ip == nil || ip.To4() == nil || candidate.Interface == "" || (candidate.Kind != networking.KindLAN && candidate.Kind != networking.KindHeadscale && candidate.Kind != networking.KindPublic) {
			return errors.New("center: Agent reported an invalid network candidate")
		}
		kind := networking.Classify(candidate.Interface, ip)
		if kind == "" {
			continue
		}
		candidate.Address = ip.String()
		candidate.Kind = kind
		if seenAddresses[ip.String()] {
			return errors.New("center: Agent reported a duplicate network candidate")
		}
		seenAddresses[ip.String()] = true
		filteredCandidates = append(filteredCandidates, candidate)
		if candidate.Kind == networking.KindHeadscale {
			reportedHeadscaleAddress = true
		}
	}
	heartbeat.NetworkCandidates = filteredCandidates
	rolesJSON, _ := json.Marshal(heartbeat.Roles)
	capabilitiesJSON, _ := json.Marshal(heartbeat.Capabilities)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	publicationCleanups := []publicationCleanup{}
	var previousRuntimeGeneration int
	if err := tx.QueryRowContext(ctx, `SELECT runtime_generation FROM agents WHERE id = ?`, id).Scan(&previousRuntimeGeneration); err != nil {
		return fmt.Errorf("center: read Agent runtime generation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET version = ?, applied_installations = ?, roles_json = ?, capabilities_json = ?, gateway_healthy = ?, runtime_generation = CASE WHEN runtime_generation < ? THEN ? ELSE runtime_generation END, tailscale_ownership = ?, last_seen_at = ? WHERE id = ?`, strings.TrimSpace(heartbeat.Version), heartbeat.AppliedInstallations, rolesJSON, capabilitiesJSON, heartbeat.GatewayHealthy, heartbeat.ApplicationRuntimeGeneration, heartbeat.ApplicationRuntimeGeneration, heartbeat.TailscaleOwnership, now.Format(time.RFC3339Nano), id); err != nil {
		return fmt.Errorf("center: record agent heartbeat: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_network_candidates WHERE agent_id = ?`, id); err != nil {
		return fmt.Errorf("center: replace Agent network candidates: %w", err)
	}
	for _, candidate := range heartbeat.NetworkCandidates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_network_candidates(agent_id, address, interface_name, kind, observed_at) VALUES(?, ?, ?, ?, ?)`, id, net.ParseIP(candidate.Address).String(), candidate.Interface, candidate.Kind, now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("center: record Agent network candidate: %w", err)
		}
	}
	if reportedNetworkCandidates != 0 {
		if err := invalidateUnusableNetworkProfile(ctx, tx, id); err != nil {
			return err
		}
	}
	if heartbeat.ApplicationEndpointsObserved {
		if err := s.reconcileApplicationEndpoints(ctx, tx, id, heartbeat.ApplicationEndpoints, now, &publicationCleanups); err != nil {
			return err
		}
	}
	if heartbeat.ApplicationRuntimeGeneration > previousRuntimeGeneration {
		if err := s.queueApplicationRuntimeMigration(ctx, tx, id, heartbeat.ApplicationRuntimeGeneration, now); err != nil {
			return err
		}
	}
	if heartbeat.Capabilities.Gateway && !heartbeat.GatewayHealthy {
		if err := s.queueUnhealthyGatewayReconcile(ctx, tx, id, now); err != nil {
			return err
		}
	}
	if heartbeat.Capabilities.Gateway && reportedHeadscaleAddress {
		needsPrivateListener, err := gatewayStateNeedsReportedHeadscaleListener(ctx, tx, id, heartbeat.NetworkCandidates)
		if err != nil {
			return err
		}
		if needsPrivateListener {
			if err := s.queueGatewayState(ctx, tx, id, now); err != nil {
				return err
			}
		}
	}
	if err := s.queueScheduledThreeXUIBackup(ctx, tx, id, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.cleanupStoppedPublications(ctx, publicationCleanups); err != nil {
		return fmt.Errorf("center: record publication cleanup state: %w", err)
	}
	return nil
}

func (s *Store) queueUnhealthyGatewayReconcile(ctx context.Context, tx *sql.Tx, agentID string, now time.Time) error {
	var desiredRevision, appliedRevision int64
	var stateStatus string
	err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status FROM gateway_states WHERE gateway_node_id = ?`, agentID).Scan(&desiredRevision, &appliedRevision, &stateStatus)
	if err == nil {
		if desiredRevision > appliedRevision || stateStatus != "ready" {
			return nil
		}
		if err := s.queueGatewayState(ctx, tx, agentID, now); err != nil {
			return fmt.Errorf("center: queue unhealthy gateway state: %w", err)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("center: inspect unhealthy gateway state: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE gateway_components SET generation = generation + 1, status = 'failed', lease_expires_at = '', last_error = 'gateway health check failed; queued for reconcile', updated_at = ? WHERE gateway_node_id = ? AND desired_status = 'running' AND status = 'ready'`, now.Format(time.RFC3339Nano), agentID)
	if err != nil {
		return fmt.Errorf("center: queue unhealthy gateway component: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return nil
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, agentID).Scan(&generation); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, gatewayComponentTaskID(agentID, generation), agentID, "gateway.component.apply", generation, "queued", "gateway health check failed; queued for reconcile")
}

func gatewayStateNeedsReportedHeadscaleListener(ctx context.Context, tx *sql.Tx, agentID string, candidates []networking.Candidate) (bool, error) {
	reported := make(map[string]struct{})
	for _, candidate := range candidates {
		if candidate.Kind == networking.KindHeadscale {
			reported[candidate.Address] = struct{}{}
		}
	}
	if len(reported) == 0 {
		return false, nil
	}
	var encoded []byte
	err := tx.QueryRowContext(ctx, `SELECT desired_json FROM gateway_states WHERE gateway_node_id = ?`, agentID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("center: inspect private gateway listener: %w", err)
	}
	var desired gateway.DesiredState
	if err := json.Unmarshal(encoded, &desired); err != nil {
		return false, errors.New("center: stored gateway desired state is invalid")
	}
	for _, listener := range desired.Listeners {
		if listener.Kind == "headscale" {
			if _, current := reported[listener.Address]; current {
				return false, nil
			}
		}
	}
	return true, nil
}

func invalidateUnusableNetworkProfile(ctx context.Context, tx *sql.Tx, agentID string) error {
	profile, err := networkProfile(ctx, tx, agentID)
	if err != nil || profile == nil {
		return err
	}
	candidates, err := networkCandidates(ctx, tx, agentID)
	if err != nil {
		return err
	}
	if err := networking.ValidateProfile(candidates, *profile); err == nil {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_network_profiles WHERE agent_id = ?`, agentID); err != nil {
		return fmt.Errorf("center: invalidate stale Agent network profile: %w", err)
	}
	return nil
}

func (s *Store) ListAgents(ctx context.Context) ([]AgentView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, version, operating_system, architecture, status, applied_installations, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json, gateway_healthy, tailscale_ownership FROM agents ORDER BY status, name, id`)
	if err != nil {
		return nil, fmt.Errorf("center: list agents: %w", err)
	}
	agents := make([]AgentView, 0)
	for rows.Next() {
		var agent AgentView
		var enrolledAt, lastSeenAt string
		var rolesJSON, capabilitiesJSON []byte
		var gatewayHealthy int
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.Version, &agent.OperatingSystem, &agent.Architecture, &agent.Status, &agent.AppliedInstallations, &enrolledAt, &lastSeenAt, &agent.SiteID, &rolesJSON, &capabilitiesJSON, &gatewayHealthy, &agent.TailscaleOwnership); err != nil {
			return nil, fmt.Errorf("center: scan agent: %w", err)
		}
		var err error
		agent.EnrolledAt, err = time.Parse(time.RFC3339Nano, enrolledAt)
		if err != nil {
			return nil, fmt.Errorf("center: parse agent enrollment time: %w", err)
		}
		agent.LastSeenAt, err = time.Parse(time.RFC3339Nano, lastSeenAt)
		if err != nil {
			return nil, fmt.Errorf("center: parse agent heartbeat time: %w", err)
		}
		agent.Connected = agent.Status == "active" && agent.LastSeenAt.After(s.now().Add(-45*time.Second))
		agent.GatewayHealthy = gatewayHealthy == 1
		if json.Unmarshal(rolesJSON, &agent.Roles) != nil || json.Unmarshal(capabilitiesJSON, &agent.Capabilities) != nil {
			return nil, errors.New("center: invalid stored Agent capabilities")
		}
		if agent.Roles == nil {
			agent.Roles = []string{}
		}
		agents = append(agents, agent)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	byID := make(map[string]int, len(agents))
	for index := range agents {
		byID[agents[index].ID] = index
		agents[index].NetworkCandidates = []networking.Candidate{}
	}
	candidateRows, err := s.db.QueryContext(ctx, `SELECT agent_id, address, interface_name, kind, observed_at FROM agent_network_candidates ORDER BY agent_id, kind, interface_name, address`)
	if err != nil {
		return nil, fmt.Errorf("center: list network candidates: %w", err)
	}
	for candidateRows.Next() {
		var agentID, observed string
		var candidate networking.Candidate
		if err := candidateRows.Scan(&agentID, &candidate.Address, &candidate.Interface, &candidate.Kind, &observed); err != nil {
			candidateRows.Close()
			return nil, err
		}
		candidate.ObservedAt, err = time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			candidateRows.Close()
			return nil, errors.New("center: invalid network candidate timestamp")
		}
		if index, ok := byID[agentID]; ok {
			agents[index].NetworkCandidates = append(agents[index].NetworkCandidates, candidate)
		}
	}
	if err := candidateRows.Close(); err != nil {
		return nil, err
	}
	profileRows, err := s.db.QueryContext(ctx, `SELECT agent_id, service_address, lan_address, headscale_address, public_address, enabled_kinds_json, direct_public, confirmed_at, candidate_observed_at FROM agent_network_profiles ORDER BY agent_id`)
	if err != nil {
		return nil, fmt.Errorf("center: list network profiles: %w", err)
	}
	for profileRows.Next() {
		var agentID, confirmed, observed string
		var enabled []byte
		var direct int
		profile := networking.Profile{}
		if err := profileRows.Scan(&agentID, &profile.ServiceAddress, &profile.LANAddress, &profile.HeadscaleAddress, &profile.PublicAddress, &enabled, &direct, &confirmed, &observed); err != nil {
			profileRows.Close()
			return nil, err
		}
		if json.Unmarshal(enabled, &profile.EnabledKinds) != nil {
			profileRows.Close()
			return nil, errors.New("center: invalid stored network profile")
		}
		profile.DirectPublic = direct == 1
		profile.ConfirmedAt, err = time.Parse(time.RFC3339Nano, confirmed)
		if err != nil {
			profileRows.Close()
			return nil, errors.New("center: invalid network profile timestamp")
		}
		profile.CandidateObserved, err = time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			profileRows.Close()
			return nil, errors.New("center: invalid network observation timestamp")
		}
		if index, ok := byID[agentID]; ok {
			agents[index].NetworkProfile = &profile
		}
	}
	if err := profileRows.Close(); err != nil {
		return nil, err
	}
	return agents, nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
