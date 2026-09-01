package center

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/secret"
)

const agentEnrollmentReplayLifetime = 24 * time.Hour
const agentConnectedMaxAge = 45 * time.Second

type AgentEnrollment struct {
	Token            string    `json:"token"`
	SiteID           string    `json:"siteId"`
	InstallerURL     string    `json:"installerUrl"`
	CACertificatePEM string    `json:"caCertificatePem,omitempty"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

type AgentEnrollmentSpec struct {
	SiteID           string
	Name             string
	CenterURL        string
	Gateway          bool
	Tunnel           bool
	UseHeadscale     bool
	CAFingerprint    string
	CACertificatePEM string
}

type AgentEnrollmentInstallProfile struct {
	Name               string
	CenterURL          string
	Roles              []string
	Capabilities       NodeCapabilities
	HeadscaleCommand   string
	HeadscaleURL       string
	HeadscaleAddresses []string
	CAFingerprint      string
	CACertificatePEM   string
}

type AgentCredential struct {
	ID           string           `json:"id"`
	Credential   string           `json:"credential"`
	Name         string           `json:"name"`
	Roles        []string         `json:"roles"`
	Capabilities NodeCapabilities `json:"capabilities"`
}

type AgentView struct {
	ID                    string                   `json:"id"`
	Name                  string                   `json:"name"`
	Version               string                   `json:"version"`
	OperatingSystem       string                   `json:"operatingSystem"`
	Architecture          string                   `json:"architecture"`
	Status                string                   `json:"status"`
	AppliedInstallations  int                      `json:"appliedInstallations"`
	EnrolledAt            time.Time                `json:"enrolledAt"`
	LastSeenAt            time.Time                `json:"lastSeenAt"`
	Connected             bool                     `json:"connected"`
	SiteID                string                   `json:"siteId"`
	Roles                 []string                 `json:"roles"`
	Capabilities          NodeCapabilities         `json:"capabilities"`
	NetworkCandidates     []networking.Candidate   `json:"networkCandidates"`
	PublicEgress          *networking.PublicEgress `json:"publicEgress,omitempty"`
	NetworkProfile        *networking.Profile      `json:"networkProfile,omitempty"`
	GatewayHealthy        bool                     `json:"gatewayHealthy"`
	TailscaleOwnership    string                   `json:"tailscaleOwnership"`
	CredentialRevoked     bool                     `json:"credentialRevoked"`
	RemoteUpdateSupported bool                     `json:"remoteUpdateSupported"`
	Update                *AgentUpdateView         `json:"update,omitempty"`
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
	spec.CAFingerprint = strings.ToLower(strings.TrimSpace(spec.CAFingerprint))
	spec.CACertificatePEM = strings.TrimSpace(spec.CACertificatePEM)
	if spec.CACertificatePEM != "" && !strings.HasPrefix(centerURL, "https://") {
		return AgentEnrollment{}, errors.New("center: Agent CA certificate can be used only with an HTTPS Center URL")
	}
	if spec.CACertificatePEM != "" {
		block, rest := pem.Decode([]byte(spec.CACertificatePEM))
		if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
			return AgentEnrollment{}, errors.New("center: Agent CA certificate must contain exactly one PEM certificate")
		}
		certificate, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil || !certificate.IsCA || !certificate.BasicConstraintsValid {
			return AgentEnrollment{}, errors.New("center: Agent CA certificate is invalid")
		}
		digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
		certificateFingerprint := hex.EncodeToString(digest[:])
		if spec.CAFingerprint != "" && spec.CAFingerprint != certificateFingerprint {
			return AgentEnrollment{}, errors.New("center: Agent CA certificate does not match its expected fingerprint")
		}
		spec.CAFingerprint = certificateFingerprint
		spec.CACertificatePEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}))
	} else if spec.CAFingerprint != "" {
		return AgentEnrollment{}, errors.New("center: Agent CA certificate is required with a private CA fingerprint")
	}
	if spec.CAFingerprint != "" {
		decoded, decodeErr := hex.DecodeString(spec.CAFingerprint)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return AgentEnrollment{}, errors.New("center: Agent CA fingerprint must be a SHA-256 public-key fingerprint")
		}
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
	enrollment := AgentEnrollment{Token: token, SiteID: spec.SiteID, InstallerURL: installerURL, CACertificatePEM: spec.CACertificatePEM, ExpiresAt: s.now().UTC().Add(10 * time.Minute)}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: begin agent enrollment: %w", err)
	}
	defer tx.Rollback()
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollment_operations WHERE expires_at <= ?`, now); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: delete expired Agent enrollment operations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id IN (SELECT bootstrap_secret_id FROM agent_enrollment_tokens WHERE expires_at <= ? AND bootstrap_secret_id IS NOT NULL)`, now); err != nil {
		return AgentEnrollment{}, fmt.Errorf("center: delete expired Agent bootstrap secrets: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_enrollment_tokens WHERE expires_at <= ? AND NOT EXISTS (
		SELECT 1 FROM agent_enrollment_operations operations WHERE operations.token_hash = agent_enrollment_tokens.token_hash
	)`, now); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_enrollment_tokens(token_hash, site_id, name, center_url, roles_json, capabilities_json, bootstrap_secret_id, ca_fingerprint, ca_certificate_pem, expires_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, tokenHash(token), spec.SiteID, spec.Name, centerURL, rolesJSON, capabilitiesJSON, bootstrapSecretID, spec.CAFingerprint, spec.CACertificatePEM, enrollment.ExpiresAt.Format(time.RFC3339Nano)); err != nil {
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
	var usedAt, bootstrapSecretID, recoveryExpiresAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT tokens.name, tokens.center_url, tokens.roles_json, tokens.capabilities_json,
		tokens.bootstrap_secret_id, tokens.ca_fingerprint, tokens.ca_certificate_pem, tokens.expires_at, tokens.used_at, operations.expires_at
		FROM agent_enrollment_tokens tokens
		LEFT JOIN agent_enrollment_operations operations ON operations.token_hash = tokens.token_hash
		WHERE tokens.token_hash = ?`, tokenHash(token)).Scan(&profile.Name, &profile.CenterURL, &rolesJSON, &capabilitiesJSON, &bootstrapSecretID, &profile.CAFingerprint, &profile.CACertificatePEM, &expiresAt, &usedAt, &recoveryExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentEnrollmentInstallProfile{}, errors.New("center: agent enrollment token is invalid")
	}
	if err != nil {
		return AgentEnrollmentInstallProfile{}, fmt.Errorf("center: read agent enrollment: %w", err)
	}
	validUntil := expiresAt
	if usedAt.Valid {
		if !recoveryExpiresAt.Valid {
			return AgentEnrollmentInstallProfile{}, errors.New("center: agent enrollment token is invalid")
		}
		validUntil = recoveryExpiresAt.String
	}
	expires, err := time.Parse(time.RFC3339Nano, validUntil)
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

func (s *Store) EnrollAgent(ctx context.Context, enrollmentToken, version, operatingSystem, architecture string, publicKey []byte) (AgentCredential, error) {
	operationID, err := randomToken(18)
	if err != nil {
		return AgentCredential{}, err
	}
	return s.EnrollAgentOperation(ctx, enrollmentToken, operationID, version, operatingSystem, architecture, publicKey)
}

func (s *Store) EnrollAgentOperation(ctx context.Context, enrollmentToken, operationID, version, operatingSystem, architecture string, publicKey []byte) (AgentCredential, error) {
	operationID = strings.TrimSpace(operationID)
	if !validAgentEnrollmentOperationID(operationID) {
		return AgentCredential{}, errors.New("center: valid Agent enrollment operation ID is required")
	}
	version = strings.TrimSpace(version)
	if version == "" || len(version) > 128 {
		return AgentCredential{}, errors.New("center: agent version is required")
	}
	if err := controlplane.ValidatePublicKey(publicKey); err != nil {
		return AgentCredential{}, errors.New("center: valid Agent X25519 public key is required")
	}
	target, err := platform.Parse(operatingSystem, architecture)
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: invalid Agent platform: %w", err)
	}
	requestPayload, err := json.Marshal(struct {
		Version         string `json:"version"`
		OperatingSystem string `json:"operatingSystem"`
		Architecture    string `json:"architecture"`
		PublicKey       []byte `json:"publicKey"`
	}{Version: version, OperatingSystem: target.OS, Architecture: target.Architecture, PublicKey: publicKey})
	if err != nil {
		return AgentCredential{}, err
	}
	requestHash := sha256.Sum256(requestPayload)
	enrollmentTokenHash := tokenHash(enrollmentToken)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: begin agent enrollment: %w", err)
	}
	defer tx.Rollback()
	var expiresAt, siteID, name string
	var rolesJSON, capabilitiesJSON []byte
	var usedAt, bootstrapSecretID sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT expires_at, used_at, site_id, name, roles_json, capabilities_json, bootstrap_secret_id FROM agent_enrollment_tokens WHERE token_hash = ?`, enrollmentTokenHash).Scan(&expiresAt, &usedAt, &siteID, &name, &rolesJSON, &capabilitiesJSON, &bootstrapSecretID)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentCredential{}, errors.New("center: agent enrollment token is invalid")
	}
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: read agent enrollment: %w", err)
	}
	if usedAt.Valid {
		return s.replayAgentEnrollment(ctx, tx, enrollmentTokenHash, operationID, requestHash[:])
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
	response := AgentCredential{ID: id, Credential: credential, Name: name, Roles: profile.Roles, Capabilities: profile.Capabilities}
	encodedResponse, err := json.Marshal(response)
	if err != nil {
		return AgentCredential{}, err
	}
	responseSecretID, err := s.putSecret(ctx, tx, encodedResponse, agentEnrollmentOperationSecretContext(enrollmentTokenHash, operationID))
	if err != nil {
		return AgentCredential{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agents(id, name, credential_hash, x25519_public_key, version, operating_system, architecture, status, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json) VALUES(?, ?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)`, id, name, tokenHash(credential), append([]byte(nil), publicKey...), version, target.OS, target.Architecture, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), siteID, rolesJSON, capabilitiesJSON); err != nil {
		return AgentCredential{}, fmt.Errorf("center: save agent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_enrollment_operations(token_hash, operation_id, request_hash, agent_id, response_secret_id, expires_at, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)`, enrollmentTokenHash, operationID, requestHash[:], id, responseSecretID, now.Add(agentEnrollmentReplayLifetime).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return AgentCredential{}, fmt.Errorf("center: save Agent enrollment operation: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_enrollment_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`, now.Format(time.RFC3339Nano), enrollmentTokenHash)
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
	return response, nil
}

func (s *Store) replayAgentEnrollment(ctx context.Context, tx *sql.Tx, enrollmentTokenHash []byte, operationID string, requestHash []byte) (AgentCredential, error) {
	var storedOperationID, agentID, responseSecretID, replayExpiresAt string
	var storedRequestHash []byte
	err := tx.QueryRowContext(ctx, `SELECT operation_id, request_hash, agent_id, response_secret_id, expires_at
		FROM agent_enrollment_operations WHERE token_hash = ?`, enrollmentTokenHash).Scan(&storedOperationID, &storedRequestHash, &agentID, &responseSecretID, &replayExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentCredential{}, errors.New("center: agent enrollment token is invalid")
	}
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: read Agent enrollment operation: %w", err)
	}
	if storedOperationID != operationID || !bytes.Equal(storedRequestHash, requestHash) {
		return AgentCredential{}, errors.New("center: agent enrollment token is invalid")
	}
	expires, err := time.Parse(time.RFC3339Nano, replayExpiresAt)
	if err != nil || !expires.After(s.now()) {
		return AgentCredential{}, errors.New("center: Agent enrollment recovery window has expired")
	}
	var sealedResponse []byte
	if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, responseSecretID).Scan(&sealedResponse); err != nil {
		return AgentCredential{}, fmt.Errorf("center: read Agent enrollment response: %w", err)
	}
	encodedResponse, err := secret.Open(s.key, sealedResponse, []byte(agentEnrollmentOperationSecretContext(enrollmentTokenHash, operationID)))
	if err != nil {
		return AgentCredential{}, fmt.Errorf("center: decrypt Agent enrollment response: %w", err)
	}
	var response AgentCredential
	if json.Unmarshal(encodedResponse, &response) != nil || response.ID != agentID || response.Credential == "" {
		return AgentCredential{}, errors.New("center: stored Agent enrollment response is invalid")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND status = 'active' AND credential_revoked_at = '' AND credential_hash = ?`, agentID, tokenHash(response.Credential)).Scan(&active); err != nil || active != 1 {
		return AgentCredential{}, errors.New("center: replayed Agent identity is no longer active")
	}
	return response, nil
}

func agentEnrollmentOperationSecretContext(enrollmentTokenHash []byte, operationID string) string {
	return "agent-enrollment-operation:" + hex.EncodeToString(enrollmentTokenHash) + ":" + operationID
}

func validAgentEnrollmentOperationID(value string) bool {
	if len(value) < 16 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

// RevokeAgentCredential immediately closes the Agent control channel without
// changing workload or topology state. It is intentionally independent from
// DisableAgent, whose business preconditions may require applications to stop.
func (s *Store) RevokeAgentCredential(ctx context.Context, agentID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agents SET credential_revoked_at = ? WHERE id = ? AND status = 'active' AND credential_revoked_at = ''`, s.now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(agentID))
	if err != nil {
		return fmt.Errorf("center: revoke Agent credential: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("center: active Agent credential was not found")
	}
	s.taskChanges.notify("agent:" + strings.TrimSpace(agentID))
	return nil
}

func (s *Store) RecordAgentHeartbeat(ctx context.Context, id, credential string, heartbeat NodeHeartbeat) error {
	if len(heartbeat.PublicKey) != 0 && controlplane.ValidatePublicKey(heartbeat.PublicKey) != nil {
		return errors.New("center: Agent heartbeat requires its X25519 public key")
	}
	if heartbeat.AppliedInstallations < 0 || heartbeat.AppliedInstallations > 1_000_000 {
		return errors.New("center: invalid applied installation count")
	}
	if heartbeat.ApplicationRuntimeGeneration < 0 || heartbeat.ApplicationRuntimeGeneration > platform.ApplicationRuntimeGeneration {
		return errors.New("center: unsupported Agent application runtime generation")
	}
	heartbeat.GatewayConfigHash = strings.ToLower(strings.TrimSpace(heartbeat.GatewayConfigHash))
	decodedGatewayHash, gatewayHashErr := hex.DecodeString(heartbeat.GatewayConfigHash)
	if heartbeat.GatewayRevision < 0 || (heartbeat.GatewayRevision == 0 && heartbeat.GatewayConfigHash != "") || (heartbeat.GatewayRevision > 0 && (gatewayHashErr != nil || len(decodedGatewayHash) != sha256.Size)) {
		return errors.New("center: Agent reported an invalid live Gateway revision")
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
	if heartbeat.PublicEgress != nil {
		if err := normalizeAgentPublicEgress(heartbeat.PublicEgress, heartbeat.NetworkCandidates, s.now().UTC()); err != nil {
			return err
		}
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
	var previousRuntimeGeneration int
	var storedPublicKey []byte
	if err := tx.QueryRowContext(ctx, `SELECT runtime_generation, x25519_public_key FROM agents WHERE id = ?`, id).Scan(&previousRuntimeGeneration, &storedPublicKey); err != nil {
		return fmt.Errorf("center: read Agent runtime generation: %w", err)
	}
	if len(storedPublicKey) == 0 && len(heartbeat.PublicKey) == 0 {
		return errors.New("center: Agent heartbeat requires its X25519 public key")
	}
	if len(storedPublicKey) != 0 && len(heartbeat.PublicKey) != 0 && !bytes.Equal(storedPublicKey, heartbeat.PublicKey) {
		return errors.New("center: Agent X25519 identity changed; revoke and enroll it again")
	}
	replacePublicEgress := heartbeat.Startup
	publicEgress := networking.PublicEgress{}
	if heartbeat.PublicEgress != nil {
		publicEgress = *heartbeat.PublicEgress
	}
	publicEgressObservedAt := ""
	if !publicEgress.ObservedAt.IsZero() {
		publicEgressObservedAt = publicEgress.ObservedAt.Format(time.RFC3339Nano)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET x25519_public_key = CASE WHEN length(x25519_public_key) = 0 THEN ? ELSE x25519_public_key END, version = ?, applied_installations = ?, roles_json = ?, capabilities_json = ?, gateway_healthy = ?, runtime_generation = ?, remote_update_supported = ?, tailscale_ownership = ?, last_seen_at = ?, public_egress_address = CASE WHEN ? THEN ? ELSE public_egress_address END, public_egress_bind_address = CASE WHEN ? THEN ? ELSE public_egress_bind_address END, public_egress_mode = CASE WHEN ? THEN ? ELSE public_egress_mode END, public_egress_observed_at = CASE WHEN ? THEN ? ELSE public_egress_observed_at END WHERE id = ?`, heartbeat.PublicKey, strings.TrimSpace(heartbeat.Version), heartbeat.AppliedInstallations, rolesJSON, capabilitiesJSON, heartbeat.GatewayHealthy, heartbeat.ApplicationRuntimeGeneration, heartbeat.RemoteUpdateSupported, heartbeat.TailscaleOwnership, now.Format(time.RFC3339Nano), replacePublicEgress, publicEgress.Address, replacePublicEgress, publicEgress.BindAddress, replacePublicEgress, publicEgress.Mode, replacePublicEgress, publicEgressObservedAt, id); err != nil {
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
	} else if heartbeat.ApplicationRuntimeGeneration > 0 {
		if err := s.queueRuntimeApplicationDeployments(ctx, tx, id, heartbeat.ApplicationRuntimeGeneration, now); err != nil {
			return err
		}
	}
	if heartbeat.Capabilities.Gateway && !heartbeat.GatewayHealthy {
		if err := s.queueUnhealthyGatewayReconcile(ctx, tx, id, now); err != nil {
			return err
		}
	} else if heartbeat.Capabilities.Gateway {
		if err := s.queueMismatchedGatewayReconcile(ctx, tx, id, heartbeat.GatewayRevision, heartbeat.GatewayConfigHash, now); err != nil {
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
	if heartbeat.Startup {
		if err := s.quarantineReadyRealityGuardsForAgent(ctx, id); err != nil {
			return fmt.Errorf("center: quarantine REALITY guards after Agent startup: %w", err)
		}
		if err := s.startRealityGuardHardening(ctx); err != nil {
			return fmt.Errorf("center: revalidate REALITY guards after Agent startup: %w", err)
		}
	}
	if err := s.cleanupStoppedPublications(ctx, publicationCleanups); err != nil {
		return fmt.Errorf("center: record publication cleanup state: %w", err)
	}
	return nil
}

func (s *Store) queueMismatchedGatewayReconcile(ctx context.Context, tx *sql.Tx, agentID string, liveRevision int64, liveHash string, now time.Time) error {
	var desiredRevision, appliedRevision int64
	var stateStatus string
	var encoded []byte
	err := tx.QueryRowContext(ctx, `SELECT desired_revision, applied_revision, status, desired_json FROM gateway_states WHERE gateway_node_id = ?`, agentID).Scan(&desiredRevision, &appliedRevision, &stateStatus, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("center: inspect live Gateway revision: %w", err)
	}
	if stateStatus != "ready" || desiredRevision != appliedRevision {
		return nil
	}
	var desired gateway.DesiredState
	if json.Unmarshal(encoded, &desired) != nil || desired.Validate() != nil || desired.Revision != desiredRevision {
		return errors.New("center: stored gateway desired state is invalid")
	}
	certificates, err := s.gatewayCertificates(ctx, tx, agentID, desired)
	if err != nil {
		return err
	}
	expectedHash, err := gateway.ConfigurationHash(desired, certificates)
	if err != nil {
		return fmt.Errorf("center: hash expected Gateway configuration: %w", err)
	}
	if liveRevision == appliedRevision && liveHash == expectedHash {
		return nil
	}
	if err := s.queueGatewayState(ctx, tx, agentID, now); err != nil {
		return fmt.Errorf("center: queue mismatched live Gateway state: %w", err)
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

func normalizeAgentPublicEgress(value *networking.PublicEgress, candidates []networking.Candidate, now time.Time) error {
	value.Address = strings.TrimSpace(value.Address)
	value.BindAddress = strings.TrimSpace(value.BindAddress)
	value.Mode = strings.TrimSpace(value.Mode)
	publicIP := net.ParseIP(value.Address)
	bindIP := net.ParseIP(value.BindAddress)
	if publicIP == nil || publicIP.To4() == nil || networking.Classify("external", publicIP) != networking.KindPublic || bindIP == nil || bindIP.To4() == nil {
		return errors.New("center: Agent reported an invalid public egress mapping")
	}
	value.Address = publicIP.String()
	value.BindAddress = bindIP.String()
	bindKind := ""
	for _, candidate := range candidates {
		if candidate.Address == value.BindAddress && (candidate.Kind == networking.KindLAN || candidate.Kind == networking.KindPublic) {
			bindKind = candidate.Kind
			break
		}
	}
	if bindKind == "" {
		return errors.New("center: Agent public egress does not match a reported local address")
	}
	switch value.Mode {
	case networking.PublicModeDirect:
		if bindKind != networking.KindPublic || value.Address != value.BindAddress {
			return errors.New("center: Agent reported an invalid direct public egress")
		}
	case networking.PublicModeNAT:
		if bindKind == networking.KindPublic && value.Address == value.BindAddress {
			return errors.New("center: Agent reported a direct public address as NAT")
		}
	default:
		return errors.New("center: Agent reported an invalid public egress mode")
	}
	if value.ObservedAt.IsZero() {
		return errors.New("center: Agent reported an invalid public egress observation time")
	}
	if value.ObservedAt.After(now.Add(2 * time.Minute)) {
		return errors.New("center: Agent reported a future public egress observation")
	}
	value.ObservedAt = value.ObservedAt.UTC()
	return nil
}

func (s *Store) ListAgents(ctx context.Context) ([]AgentView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, version, operating_system, architecture, status, applied_installations, enrolled_at, last_seen_at, site_id, roles_json, capabilities_json, gateway_healthy, tailscale_ownership, credential_revoked_at <> '', public_egress_address, public_egress_bind_address, public_egress_mode, public_egress_observed_at, remote_update_supported FROM agents ORDER BY status, name, id`)
	if err != nil {
		return nil, fmt.Errorf("center: list agents: %w", err)
	}
	agents := make([]AgentView, 0)
	for rows.Next() {
		var agent AgentView
		var enrolledAt, lastSeenAt, publicAddress, publicBindAddress, publicMode, publicObservedAt string
		var rolesJSON, capabilitiesJSON []byte
		var gatewayHealthy int
		if err := rows.Scan(&agent.ID, &agent.Name, &agent.Version, &agent.OperatingSystem, &agent.Architecture, &agent.Status, &agent.AppliedInstallations, &enrolledAt, &lastSeenAt, &agent.SiteID, &rolesJSON, &capabilitiesJSON, &gatewayHealthy, &agent.TailscaleOwnership, &agent.CredentialRevoked, &publicAddress, &publicBindAddress, &publicMode, &publicObservedAt, &agent.RemoteUpdateSupported); err != nil {
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
		agent.Connected = agent.Status == "active" && !agent.CredentialRevoked && agent.LastSeenAt.After(s.now().Add(-agentConnectedMaxAge))
		agent.GatewayHealthy = gatewayHealthy == 1
		if publicAddress != "" || publicBindAddress != "" || publicMode != "" || publicObservedAt != "" {
			observedAt, parseErr := time.Parse(time.RFC3339Nano, publicObservedAt)
			if parseErr != nil {
				return nil, errors.New("center: invalid stored Agent public egress timestamp")
			}
			agent.PublicEgress = &networking.PublicEgress{Address: publicAddress, BindAddress: publicBindAddress, Mode: publicMode, ObservedAt: observedAt}
		}
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
	updateRows, err := s.db.QueryContext(ctx, `SELECT u.id, u.agent_id, u.target_version, u.state, u.last_error, u.updated_at
		FROM agent_updates u WHERE u.rowid = (SELECT latest.rowid FROM agent_updates latest WHERE latest.agent_id = u.agent_id ORDER BY latest.created_at DESC, latest.rowid DESC LIMIT 1)`)
	if err != nil {
		return nil, fmt.Errorf("center: list Agent updates: %w", err)
	}
	for updateRows.Next() {
		var update AgentUpdateView
		var agentID, updatedAt string
		if err := updateRows.Scan(&update.ID, &agentID, &update.TargetVersion, &update.State, &update.LastError, &updatedAt); err != nil {
			updateRows.Close()
			return nil, fmt.Errorf("center: scan Agent update: %w", err)
		}
		update.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			updateRows.Close()
			return nil, errors.New("center: invalid stored Agent update timestamp")
		}
		if index, ok := byID[agentID]; ok {
			agents[index].Update = &update
		}
	}
	if err := updateRows.Close(); err != nil {
		return nil, err
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
	profileRows, err := s.db.QueryContext(ctx, `SELECT agent_id, service_address, lan_address, headscale_address, public_address, public_bind_address, public_mode, enabled_kinds_json, direct_public, public_verified_at, confirmed_at, candidate_observed_at FROM agent_network_profiles ORDER BY agent_id`)
	if err != nil {
		return nil, fmt.Errorf("center: list network profiles: %w", err)
	}
	for profileRows.Next() {
		var agentID, verified, confirmed, observed string
		var enabled []byte
		var direct int
		profile := networking.Profile{}
		if err := profileRows.Scan(&agentID, &profile.ServiceAddress, &profile.LANAddress, &profile.HeadscaleAddress, &profile.PublicAddress, &profile.PublicBindAddress, &profile.PublicMode, &enabled, &direct, &verified, &confirmed, &observed); err != nil {
			profileRows.Close()
			return nil, err
		}
		if json.Unmarshal(enabled, &profile.EnabledKinds) != nil {
			profileRows.Close()
			return nil, errors.New("center: invalid stored network profile")
		}
		profile.DirectPublic = direct == 1
		if verified != "" {
			profile.PublicVerifiedAt, err = time.Parse(time.RFC3339Nano, verified)
			if err != nil {
				profileRows.Close()
				return nil, errors.New("center: invalid public ingress verification timestamp")
			}
		}
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
