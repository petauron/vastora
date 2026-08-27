package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

const (
	agentConnectionModeSetting = "agent_connection_mode"
	agentConnectURLSetting     = "agent_connect_url"
)

type SetupStatusView struct {
	AdministratorConfigured bool `json:"administratorConfigured"`
	OnboardingComplete      bool `json:"onboardingComplete"`
}

type CenterNetworkInput struct {
	AgentConnectionMode string `json:"agentConnectionMode"`
	AgentConnectURL     string `json:"agentConnectUrl"`
}

type CenterNetworkConfig struct {
	AgentConnectionMode string `json:"agentConnectionMode"`
	AgentConnectURL     string `json:"agentConnectUrl"`
}

type InitialSetupInput struct {
	Site      SiteInput          `json:"site"`
	Network   CenterNetworkInput `json:"network"`
	Headscale *HeadscaleInput    `json:"headscale,omitempty"`
}

type InitialSetupResult struct {
	Site    SiteView            `json:"site"`
	Network CenterNetworkConfig `json:"network"`
}

func NormalizeAgentConnectURL(value string) (string, error) {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", errors.New("center: Agent connection URL is invalid")
	}
	if parsed.Path != "" || parsed.RawPath != "" {
		return "", errors.New("center: Agent connection URL cannot contain a path")
	}
	if parsed.Scheme == "https" {
		return value, nil
	}
	if parsed.Scheme != "http" || !loopbackHost(parsed.Hostname()) {
		return "", errors.New("center: Agent connection URL must use HTTPS; HTTP is allowed only on loopback")
	}
	return value, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.To4() != nil && address.IsLoopback()
}

func normalizeCenterNetwork(input CenterNetworkInput) (CenterNetworkConfig, error) {
	input.AgentConnectionMode = strings.TrimSpace(input.AgentConnectionMode)
	if input.AgentConnectionMode != "lan" && input.AgentConnectionMode != "headscale" && input.AgentConnectionMode != "public" {
		return CenterNetworkConfig{}, errors.New("center: Agent connection mode must be lan, headscale, or public")
	}
	connectURL, err := NormalizeAgentConnectURL(input.AgentConnectURL)
	if err != nil {
		return CenterNetworkConfig{}, err
	}
	return CenterNetworkConfig{AgentConnectionMode: input.AgentConnectionMode, AgentConnectURL: connectURL}, nil
}

func (s *Store) SetupStatus(ctx context.Context) (SetupStatusView, error) {
	var administrators, sites, networkSettings int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&administrators); err != nil {
		return SetupStatusView{}, fmt.Errorf("center: read administrators: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites`).Scan(&sites); err != nil {
		return SetupStatusView{}, fmt.Errorf("center: read sites: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE key IN (?, ?)`, agentConnectionModeSetting, agentConnectURLSetting).Scan(&networkSettings); err != nil {
		return SetupStatusView{}, fmt.Errorf("center: read initial network settings: %w", err)
	}
	return SetupStatusView{
		AdministratorConfigured: administrators > 0,
		OnboardingComplete:      administrators > 0 && sites > 0 && networkSettings == 2,
	}, nil
}

func (s *Store) CenterNetworkConfig(ctx context.Context) (CenterNetworkConfig, error) {
	var mode, connectURL string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectionModeSetting).Scan(&mode); errors.Is(err, sql.ErrNoRows) {
		return CenterNetworkConfig{}, errors.New("center: initial network setup is incomplete")
	} else if err != nil {
		return CenterNetworkConfig{}, fmt.Errorf("center: read Agent connection mode: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, agentConnectURLSetting).Scan(&connectURL); errors.Is(err, sql.ErrNoRows) {
		return CenterNetworkConfig{}, errors.New("center: initial network setup is incomplete")
	} else if err != nil {
		return CenterNetworkConfig{}, fmt.Errorf("center: read Agent connection URL: %w", err)
	}
	return normalizeCenterNetwork(CenterNetworkInput{AgentConnectionMode: mode, AgentConnectURL: connectURL})
}

func (s *Store) CompleteInitialSetup(ctx context.Context, input InitialSetupInput) (InitialSetupResult, error) {
	site, err := normalizeSiteInput(input.Site)
	if err != nil {
		return InitialSetupResult{}, err
	}
	if len(site.GatewayNodes) != 0 {
		return InitialSetupResult{}, errors.New("center: the initial site cannot have gateways before a node is connected")
	}
	networkConfig, err := normalizeCenterNetwork(input.Network)
	if err != nil {
		return InitialSetupResult{}, err
	}
	siteID, err := randomToken(18)
	if err != nil {
		return InitialSetupResult{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return InitialSetupResult{}, fmt.Errorf("center: begin initial setup: %w", err)
	}
	defer tx.Rollback()
	var administrators, sites int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM admins`).Scan(&administrators); err != nil {
		return InitialSetupResult{}, err
	}
	if administrators == 0 {
		return InitialSetupResult{}, errors.New("center: create the administrator before completing setup")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sites`).Scan(&sites); err != nil {
		return InitialSetupResult{}, err
	}
	if sites != 0 {
		return InitialSetupResult{}, errors.New("center: initial setup is already complete")
	}
	if networkConfig.AgentConnectionMode == "headscale" {
		var integrations int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&integrations); err != nil {
			return InitialSetupResult{}, err
		}
		if integrations != 1 {
			return InitialSetupResult{}, errors.New("center: configure Headscale before selecting it as the Agent connection mode")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sites(id, organization_id, name, code, description, timezone, domain_suffix, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`, siteID, defaultOrganizationID, site.Name, site.Code, site.Description, site.Timezone, site.DomainSuffix, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return InitialSetupResult{}, fmt.Errorf("center: create initial site: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key, value) VALUES(?, ?), (?, ?)`, agentConnectionModeSetting, networkConfig.AgentConnectionMode, agentConnectURLSetting, networkConfig.AgentConnectURL); err != nil {
		return InitialSetupResult{}, fmt.Errorf("center: save initial network settings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return InitialSetupResult{}, fmt.Errorf("center: commit initial setup: %w", err)
	}
	return InitialSetupResult{
		Site: SiteView{
			ID: siteID, OrganizationID: defaultOrganizationID, Name: site.Name, Code: site.Code,
			Description: site.Description, Timezone: site.Timezone, DomainSuffix: site.DomainSuffix,
			GatewayNodes: []string{}, Status: "active", CreatedAt: now, UpdatedAt: now,
		},
		Network: networkConfig,
	}, nil
}
