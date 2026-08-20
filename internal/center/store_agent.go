package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

type AgentEnrollment struct {
	Token              string    `json:"token"`
	SiteID             string    `json:"siteId"`
	ExpiresAt          time.Time `json:"expiresAt"`
	HeadscaleCommand   string    `json:"headscaleCommand,omitempty"`
	HeadscaleExpiresAt time.Time `json:"headscaleExpiresAt,omitempty"`
}

type AgentCredential struct {
	ID         string `json:"id"`
	Credential string `json:"credential"`
}

type AgentView struct {
	ID                   string                 `json:"id"`
	Name                 string                 `json:"name"`
	Version              string                 `json:"version"`
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
}

func (s *Store) CreateAgentEnrollment(ctx context.Context, siteID string) (AgentEnrollment, error) {
	siteID = strings.TrimSpace(siteID)
	if siteID == "" {
		return AgentEnrollment{}, errors.New("center: enrollment site is required")
	}
	var siteExists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites WHERE id = ? AND status = 'active'`, siteID).Scan(&siteExists); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: inspect enrollment site: %w", err)
	}
	if siteExists != 1 {
		return AgentEnrollment{}, errors.New("center: enrollment site was not found")
	}
	token, err := randomToken(32)
	if err != nil {
		return AgentEnrollment{}, err
	}
	enrollment := AgentEnrollment{Token: token, SiteID: siteID, ExpiresAt: s.now().UTC().Add(10 * time.Minute)}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO agent_enrollment_tokens(token_hash, site_id, expires_at) VALUES(?, ?, ?)`, tokenHash(token), siteID, enrollment.ExpiresAt.Format(time.RFC3339Nano)); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: create agent enrollment: %w", err)
	}
	return enrollment, nil
}

func (s *Store) EnrollAgent(ctx context.Context, enrollmentToken, name, version string) (AgentCredential, error) {
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" || len(name) > 128 {
		return AgentCredential{}, errors.New("center: agent name must be 1 to 128 characters")
	}
	if version == "" || len(version) > 128 {
		return AgentCredential{}, errors.New("center: agent version is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: begin agent enrollment: %w", err)
	}
	defer tx.Rollback()
	var expiresAt, siteID string
	var usedAt sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT expires_at, used_at, site_id FROM agent_enrollment_tokens WHERE token_hash = ?`, tokenHash(enrollmentToken)).Scan(&expiresAt, &usedAt, &siteID)
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
	id, err := randomToken(18)
	if err != nil {
		return AgentCredential{}, err
	}
	credential, err := randomToken(32)
	if err != nil {
		return AgentCredential{}, err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id, name, credential_hash, version, status, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json) VALUES(?, ?, ?, ?, 'active', ?, ?, ?, '[]', '{}')`, id, name, tokenHash(credential), version, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), siteID); err != nil {
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
	if err := tx.Commit(); err != nil {
		return AgentCredential{}, fmt.Errorf("center: commit agent enrollment: %w", err)
	}
	return AgentCredential{ID: id, Credential: credential}, nil
}

func (s *Store) RecordAgentHeartbeat(ctx context.Context, id, credential string, heartbeat NodeHeartbeat) error {
	if heartbeat.AppliedInstallations < 0 || heartbeat.AppliedInstallations > 1_000_000 {
		return errors.New("center: invalid applied installation count")
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
	seenAddresses := map[string]bool{}
	for _, candidate := range heartbeat.NetworkCandidates {
		ip := net.ParseIP(candidate.Address)
		if ip == nil || candidate.Interface == "" || (candidate.Family != "ipv4" && candidate.Family != "ipv6") || (candidate.Kind != networking.KindLAN && candidate.Kind != networking.KindHeadscale && candidate.Kind != networking.KindPublic) {
			return errors.New("center: Agent reported an invalid network candidate")
		}
		if seenAddresses[ip.String()] {
			return errors.New("center: Agent reported a duplicate network candidate")
		}
		seenAddresses[ip.String()] = true
	}
	rolesJSON, _ := json.Marshal(heartbeat.Roles)
	capabilitiesJSON, _ := json.Marshal(heartbeat.Capabilities)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	publicationCleanups := []publicationCleanup{}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET version = ?, applied_installations = ?, roles_json = ?, capabilities_json = ?, gateway_healthy = ?, last_seen_at = ? WHERE id = ?`, strings.TrimSpace(heartbeat.Version), heartbeat.AppliedInstallations, rolesJSON, capabilitiesJSON, heartbeat.GatewayHealthy, now.Format(time.RFC3339Nano), id); err != nil {
		return fmt.Errorf("center: record agent heartbeat: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_network_candidates WHERE agent_id = ?`, id); err != nil {
		return fmt.Errorf("center: replace Agent network candidates: %w", err)
	}
	for _, candidate := range heartbeat.NetworkCandidates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_network_candidates(agent_id, address, interface_name, family, kind, observed_at) VALUES(?, ?, ?, ?, ?, ?)`, id, net.ParseIP(candidate.Address).String(), candidate.Interface, candidate.Family, candidate.Kind, now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("center: record Agent network candidate: %w", err)
		}
	}
	if heartbeat.ApplicationEndpointsObserved {
		if err := s.reconcileApplicationEndpoints(ctx, tx, id, heartbeat.ApplicationEndpoints, now, &publicationCleanups); err != nil {
			return err
		}
	}
	if heartbeat.Capabilities.Gateway && !heartbeat.GatewayHealthy {
		result, err := tx.ExecContext(ctx, `UPDATE gateway_components SET generation = generation + 1, status = 'failed', lease_expires_at = '', last_error = 'gateway health check failed; queued for reconcile', updated_at = ? WHERE gateway_node_id = ? AND desired_status = 'running' AND status = 'ready'`, now.Format(time.RFC3339Nano), id)
		if err != nil {
			return fmt.Errorf("center: queue unhealthy gateway reconcile: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed != 0 {
			var generation int64
			if err := tx.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, id).Scan(&generation); err != nil {
				return err
			}
			if err := s.recordTaskEvent(ctx, tx, gatewayComponentTaskID(id, generation), id, "gateway.component.apply", generation, "queued", "gateway health check failed; queued for reconcile"); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.cleanupStoppedPublications(ctx, publicationCleanups); err != nil {
		return fmt.Errorf("center: record publication cleanup state: %w", err)
	}
	return nil
}

func (s *Store) ListAgents(ctx context.Context) ([]AgentView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, version, status, applied_installations, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json, gateway_healthy FROM agents ORDER BY status, name, id`)
	if err != nil {
		return nil, fmt.Errorf("center: list agents: %w", err)
	}
	agents := make([]AgentView, 0)
	for rows.Next() {
		var agent AgentView
		var enrolledAt, lastSeenAt string
		var rolesJSON, capabilitiesJSON []byte
		var gatewayHealthy int
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.Version, &agent.Status, &agent.AppliedInstallations, &enrolledAt, &lastSeenAt, &agent.SiteID, &rolesJSON, &capabilitiesJSON, &gatewayHealthy); err != nil {
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
	candidateRows, err := s.db.QueryContext(ctx, `SELECT agent_id, address, interface_name, family, kind, observed_at FROM agent_network_candidates ORDER BY agent_id, kind, interface_name, address`)
	if err != nil {
		return nil, fmt.Errorf("center: list network candidates: %w", err)
	}
	for candidateRows.Next() {
		var agentID, observed string
		var candidate networking.Candidate
		if err := candidateRows.Scan(&agentID, &candidate.Address, &candidate.Interface, &candidate.Family, &candidate.Kind, &observed); err != nil {
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
