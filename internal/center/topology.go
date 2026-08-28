package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
)

const defaultOrganizationID = "organization-default"

var (
	siteCodePattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	domainSuffixPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

type NodeCapabilities struct {
	Docker  bool `json:"docker"`
	Gateway bool `json:"gateway"`
	Tunnel  bool `json:"tunnel"`
	Metrics bool `json:"metrics"`
	Logs    bool `json:"logs"`
}

type NodeHeartbeat struct {
	Version                      string
	AppliedInstallations         int
	Roles                        []string
	Capabilities                 NodeCapabilities
	NetworkCandidates            []networking.Candidate
	ApplicationEndpoints         []ApplicationEndpointObservation
	ApplicationEndpointsObserved bool
	GatewayHealthy               bool
	ApplicationRuntimeGeneration int
	TailscaleOwnership           string
}

type ApplicationEndpointObservation struct {
	AppKey            string `json:"appKey"`
	Name              string `json:"name"`
	Protocol          string `json:"protocol"`
	AppProtocol       string `json:"appProtocol"`
	Listen            string `json:"listen"`
	Port              int    `json:"port"`
	Enabled           bool   `json:"enabled"`
	RemoteNodeID      int    `json:"remoteNodeId,omitempty"`
	InboundTag        string `json:"inboundTag,omitempty"`
	InboundTotalBytes int64  `json:"inboundTotalBytes,omitempty"`
}

type SiteInput struct {
	Name         string   `json:"name"`
	Code         string   `json:"code"`
	Description  string   `json:"description"`
	Timezone     string   `json:"timezone"`
	DomainSuffix string   `json:"domainSuffix"`
	GatewayNodes []string `json:"gatewayNodes"`
}

type OrganizationView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type SiteView struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Name           string    `json:"name"`
	Code           string    `json:"code"`
	Description    string    `json:"description"`
	Timezone       string    `json:"timezone"`
	DomainSuffix   string    `json:"domainSuffix"`
	GatewayNodes   []string  `json:"gatewayNodes"`
	GatewayStatus  string    `json:"gatewayStatus"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type ApplicationView struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	NodeID            string     `json:"nodeId"`
	SiteID            string     `json:"siteId"`
	AppKey            string     `json:"appKey"`
	Image             string     `json:"image"`
	Status            string     `json:"status"`
	Runtime           string     `json:"runtime"`
	Role              string     `json:"role,omitempty"`
	ControllerID      string     `json:"controllerApplicationId,omitempty"`
	NodeSyncStatus    string     `json:"nodeSyncStatus,omitempty"`
	NodeSyncError     string     `json:"nodeSyncError,omitempty"`
	RestorePointState string     `json:"restorePointState,omitempty"`
	RestorePointAt    *time.Time `json:"restorePointAt,omitempty"`
	InstalledVersion  string     `json:"installedVersion,omitempty"`
	AvailableVersion  string     `json:"availableVersion,omitempty"`
	UpdateAvailable   bool       `json:"updateAvailable"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

type ServiceView struct {
	ID             string    `json:"id"`
	ApplicationID  string    `json:"applicationId"`
	SiteID         string    `json:"siteId"`
	Name           string    `json:"name"`
	DisplayName    string    `json:"displayName,omitempty"`
	RegionCode     string    `json:"regionCode,omitempty"`
	Protocol       string    `json:"protocol"`
	ContainerPort  int       `json:"containerPort"`
	HostPort       int       `json:"hostPort"`
	Endpoint       string    `json:"endpoint"`
	Source         string    `json:"source"`
	AppProtocol    string    `json:"appProtocol,omitempty"`
	Management     bool      `json:"management"`
	ObservedListen string    `json:"observedListen,omitempty"`
	Status         string    `json:"status"`
	LastError      string    `json:"lastError,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type RouteView struct {
	ID              string    `json:"id"`
	PublicationID   string    `json:"publicationId"`
	SiteID          string    `json:"siteId"`
	ServiceID       string    `json:"serviceId"`
	GatewayNodeID   string    `json:"gatewayNodeId"`
	Hostname        string    `json:"hostname"`
	Protocol        string    `json:"protocol"`
	Upstreams       []string  `json:"upstreams"`
	TLSEnabled      bool      `json:"tlsEnabled"`
	Status          string    `json:"status"`
	DesiredRevision int64     `json:"desiredRevision"`
	AppliedRevision int64     `json:"appliedRevision"`
	LastError       string    `json:"lastError,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func normalizeSiteInput(input SiteInput) (SiteInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Code = strings.ToLower(strings.TrimSpace(input.Code))
	input.Description = strings.TrimSpace(input.Description)
	input.Timezone = strings.TrimSpace(input.Timezone)
	input.DomainSuffix = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.DomainSuffix), "."))
	if input.Name == "" || len(input.Name) > 128 || !siteCodePattern.MatchString(input.Code) {
		return SiteInput{}, errors.New("center: site name and valid lowercase code are required")
	}
	if input.DomainSuffix != "" && !domainSuffixPattern.MatchString(input.DomainSuffix) {
		return SiteInput{}, errors.New("center: invalid site domain suffix")
	}
	if input.Timezone == "" {
		return SiteInput{}, errors.New("center: site timezone is required")
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return SiteInput{}, errors.New("center: invalid site timezone")
	}
	input.GatewayNodes = uniqueStrings(input.GatewayNodes)
	return input, nil
}

func (s *Store) CreateSite(ctx context.Context, input SiteInput) (SiteView, error) {
	var err error
	input, err = normalizeSiteInput(input)
	if err != nil {
		return SiteView{}, err
	}
	id, err := randomToken(18)
	if err != nil {
		return SiteView{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SiteView{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO sites(id, organization_id, name, code, description, timezone, domain_suffix, status, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`, id, defaultOrganizationID, input.Name, input.Code, input.Description, input.Timezone, input.DomainSuffix, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return SiteView{}, fmt.Errorf("center: create site: %w", err)
	}
	if err := s.replaceSiteGateways(ctx, tx, id, input.GatewayNodes, now); err != nil {
		return SiteView{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteView{}, err
	}
	return SiteView{ID: id, OrganizationID: defaultOrganizationID, Name: input.Name, Code: input.Code, Description: input.Description, Timezone: input.Timezone, DomainSuffix: input.DomainSuffix, GatewayNodes: uniqueStrings(input.GatewayNodes), Status: "active", CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Store) UpdateSite(ctx context.Context, id string, input SiteInput) (SiteView, error) {
	var err error
	input, err = normalizeSiteInput(input)
	if err != nil {
		return SiteView{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SiteView{}, err
	}
	defer tx.Rollback()
	var existingCode, existingDomainSuffix, certificateSecretID string
	if err := tx.QueryRowContext(ctx, `SELECT s.code, s.domain_suffix, COALESCE(c.secret_id, '')
		FROM sites s LEFT JOIN site_certificates c ON c.site_id = s.id WHERE s.id = ?`, id).Scan(&existingCode, &existingDomainSuffix, &certificateSecretID); errors.Is(err, sql.ErrNoRows) {
		return SiteView{}, errors.New("center: site not found")
	} else if err != nil {
		return SiteView{}, fmt.Errorf("center: read site configuration: %w", err)
	}
	namespaceChanged := input.Code != existingCode || input.DomainSuffix != existingDomainSuffix
	if namespaceChanged {
		var activePublications int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications p JOIN services svc ON svc.id = p.service_id
			WHERE svc.site_id = ? AND p.status <> 'stopped'`, id).Scan(&activePublications); err != nil {
			return SiteView{}, err
		}
		if activePublications != 0 {
			return SiteView{}, errors.New("center: stop this Site's access points before changing its domain namespace")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM site_certificates WHERE site_id = ?`, id); err != nil {
			return SiteView{}, err
		}
		if certificateSecretID != "" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, certificateSecretID); err != nil {
				return SiteView{}, err
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE sites SET name = ?, code = ?, description = ?, timezone = ?, domain_suffix = ?, updated_at = ? WHERE id = ?`, input.Name, input.Code, input.Description, input.Timezone, input.DomainSuffix, now.Format(time.RFC3339Nano), id)
	if err != nil {
		return SiteView{}, fmt.Errorf("center: update site: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return SiteView{}, errors.New("center: site not found")
	}
	if err := s.replaceSiteGateways(ctx, tx, id, input.GatewayNodes, now); err != nil {
		return SiteView{}, err
	}
	if err := tx.Commit(); err != nil {
		return SiteView{}, err
	}
	return s.Site(ctx, id)
}

func (s *Store) replaceSiteGateways(ctx context.Context, tx *sql.Tx, siteID string, gatewayIDs []string, now time.Time) error {
	previous := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT agent_id FROM site_gateways WHERE site_id = ?`, siteID)
	if err != nil {
		return fmt.Errorf("center: read existing site gateways: %w", err)
	}
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			rows.Close()
			return err
		}
		previous[agentID] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	desired := uniqueStrings(gatewayIDs)
	desiredSet := make(map[string]bool, len(desired))
	for _, agentID := range desired {
		desiredSet[agentID] = true
	}
	for agentID := range previous {
		if desiredSet[agentID] {
			continue
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE gateway_node_id = ? AND status <> 'stopped'`, agentID).Scan(&active); err != nil {
			return err
		}
		if active != 0 {
			return fmt.Errorf("center: stop publications using gateway %q before removing it from the site", agentID)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM site_gateways WHERE site_id = ?`, siteID); err != nil {
		return fmt.Errorf("center: replace site gateways: %w", err)
	}
	for _, agentID := range desired {
		var capable int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND site_id = ? AND json_extract(capabilities_json, '$.gateway') = 1`, agentID, siteID).Scan(&capable); err != nil {
			return fmt.Errorf("center: validate site gateway: %w", err)
		}
		if capable != 1 {
			return fmt.Errorf("center: node %q is not a gateway-capable node in this site", agentID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO site_gateways(site_id, agent_id, created_at) VALUES(?, ?, ?)`, siteID, agentID, now.Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("center: add site gateway: %w", err)
		}
		if !previous[agentID] {
			if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_components(gateway_node_id, desired_status, generation, status, updated_at)
				VALUES(?, 'running', 1, 'pending', ?)
				ON CONFLICT(gateway_node_id) DO UPDATE SET desired_status = 'running', generation = gateway_components.generation + 1, status = 'pending', lease_expires_at = '', last_error = '', updated_at = excluded.updated_at`, agentID, now.Format(time.RFC3339Nano)); err != nil {
				return fmt.Errorf("center: queue gateway installation: %w", err)
			}
			var generation int64
			if err := tx.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, agentID).Scan(&generation); err != nil {
				return err
			}
			if err := s.recordTaskEvent(ctx, tx, gatewayComponentTaskID(agentID, generation), agentID, "gateway.component.apply", generation, "queued", "gateway selected for site "+siteID); err != nil {
				return err
			}
			backfilled, err := s.backfillGatewayRoutes(ctx, tx, siteID, agentID, now)
			if err != nil {
				return err
			}
			systemState := gateway.DesiredState{Revision: 1}
			if err := s.appendSystemGatewayRoutes(ctx, tx, agentID, &systemState, map[string]gateway.Listener{}); err != nil {
				return err
			}
			if backfilled != 0 || len(systemState.Routes) != 0 {
				if err := s.queueGatewayState(ctx, tx, agentID, now); err != nil {
					return err
				}
			}
		}
	}
	for agentID := range previous {
		if desiredSet[agentID] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE site_id = ? AND gateway_node_id = ?`, siteID, agentID); err != nil {
			return fmt.Errorf("center: remove gateway routes: %w", err)
		}
		state := gateway.DesiredState{Revision: 1}
		if err := s.appendSystemGatewayRoutes(ctx, tx, agentID, &state, map[string]gateway.Listener{}); err != nil {
			return err
		}
		if len(state.Routes) != 0 {
			if err := s.queueGatewayState(ctx, tx, agentID, now); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_components SET desired_status = 'stopped', generation = generation + 1, status = 'pending', lease_expires_at = '', last_error = '', updated_at = ? WHERE gateway_node_id = ?`, now.Format(time.RFC3339Nano), agentID); err != nil {
			return fmt.Errorf("center: queue gateway removal: %w", err)
		}
		var generation int64
		if err := tx.QueryRowContext(ctx, `SELECT generation FROM gateway_components WHERE gateway_node_id = ?`, agentID).Scan(&generation); err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, gatewayComponentTaskID(agentID, generation), agentID, "gateway.component.apply", generation, "queued", "gateway removed from site "+siteID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM gateway_states WHERE gateway_node_id = ?`, agentID); err != nil {
			return fmt.Errorf("center: remove gateway desired state: %w", err)
		}
	}
	return nil
}

func (s *Store) backfillGatewayRoutes(ctx context.Context, tx *sql.Tx, siteID, gatewayID string, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, s.id, p.hostname, s.protocol, s.endpoint, p.tls_enabled
		FROM publications p JOIN services s ON s.id = p.service_id
		WHERE s.site_id = ? AND p.gateway_node_id = ? AND p.kind IN ('lan_gateway', 'headscale_gateway', 'public_direct')
		AND p.status <> 'stopped' AND s.status <> 'stopped' ORDER BY p.id`, siteID, gatewayID)
	if err != nil {
		return 0, fmt.Errorf("center: list services for gateway backfill: %w", err)
	}
	type publication struct {
		id        string
		serviceID string
		hostname  string
		protocol  string
		endpoint  string
		tls       bool
	}
	publications := []publication{}
	for rows.Next() {
		var value publication
		var tls int
		if err := rows.Scan(&value.id, &value.serviceID, &value.hostname, &value.protocol, &value.endpoint, &tls); err != nil {
			rows.Close()
			return 0, err
		}
		value.tls = tls == 1
		if _, _, err := net.SplitHostPort(value.endpoint); err != nil {
			rows.Close()
			return 0, errors.New("center: stored service endpoint is invalid")
		}
		publications = append(publications, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, value := range publications {
		routeID, err := randomToken(18)
		if err != nil {
			return 0, err
		}
		upstreams, _ := json.Marshal([]string{value.endpoint})
		if _, err := tx.ExecContext(ctx, `INSERT INTO routes(id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, tls_enabled, status, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)
			ON CONFLICT(publication_id, gateway_node_id) DO UPDATE SET hostname = excluded.hostname, protocol = excluded.protocol,
			upstreams_json = excluded.upstreams_json, tls_enabled = excluded.tls_enabled, status = 'pending', last_error = '', updated_at = excluded.updated_at`,
			routeID, value.id, siteID, value.serviceID, gatewayID, value.hostname, value.protocol, upstreams, value.tls, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return 0, fmt.Errorf("center: backfill gateway route: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'pending', last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), value.id); err != nil {
			return 0, err
		}
	}
	return len(publications), nil
}

func (s *Store) UpdateAgent(ctx context.Context, agentID, name, siteID string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 128 {
		return errors.New("center: node name must be 1 to 128 characters")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin node site assignment: %w", err)
	}
	defer tx.Rollback()
	var blockedGateways, activeApplications int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_gateways WHERE agent_id = ? AND site_id <> ?`, agentID, siteID).Scan(&blockedGateways); err != nil {
		return fmt.Errorf("center: inspect node gateway assignment: %w", err)
	}
	if blockedGateways != 0 {
		return errors.New("center: remove the node as a gateway from its current site before moving it")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM applications WHERE node_id = ? AND site_id <> ? AND status <> 'stopped'`, agentID, siteID).Scan(&activeApplications); err != nil {
		return fmt.Errorf("center: inspect node applications: %w", err)
	}
	if activeApplications != 0 {
		return errors.New("center: stop active applications before moving the node to another site")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agents SET name = ?, site_id = ? WHERE id = ? AND status = 'active' AND EXISTS(SELECT 1 FROM sites WHERE id = ? AND status = 'active')`, name, siteID, agentID, siteID)
	if err != nil {
		return fmt.Errorf("center: assign node site: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("center: node or site not found")
	}
	return tx.Commit()
}

func (s *Store) DisableAgent(ctx context.Context, agentID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("center: begin node disable: %w", err)
	}
	defer tx.Rollback()
	checks := []struct {
		query   string
		message string
	}{
		{`SELECT COUNT(*) FROM applications WHERE node_id = ? AND status <> 'stopped'`, "center: uninstall active applications before disabling this node"},
		{`SELECT COUNT(*) FROM site_gateways WHERE agent_id = ?`, "center: remove this node as a Site gateway before disabling it"},
		{`SELECT COUNT(*) FROM publications WHERE gateway_node_id = ? AND status <> 'stopped'`, "center: stop publications using this node before disabling it"},
		{`SELECT COUNT(*) FROM deployments WHERE agent_id = ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, "center: wait for active node tasks before disabling it"},
		{`SELECT COUNT(*) FROM cloudflare_tunnels WHERE agent_id = ? AND status <> 'stopped'`, "center: stop the Cloudflare Tunnel connector before disabling this node"},
	}
	for _, check := range checks {
		var count int
		if err := tx.QueryRowContext(ctx, check.query, agentID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return errors.New(check.message)
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE agents SET status = 'disabled' WHERE id = ? AND status = 'active'`, agentID)
	if err != nil {
		return fmt.Errorf("center: disable node: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("center: active node not found")
	}
	return tx.Commit()
}

func (s *Store) Site(ctx context.Context, id string) (SiteView, error) {
	sites, err := s.listSites(ctx, id)
	if err != nil {
		return SiteView{}, err
	}
	if len(sites) != 1 {
		return SiteView{}, errors.New("center: site not found")
	}
	return sites[0], nil
}

func (s *Store) ListSites(ctx context.Context) ([]SiteView, error) { return s.listSites(ctx, "") }

func (s *Store) listSites(ctx context.Context, onlyID string) ([]SiteView, error) {
	query := `SELECT id, organization_id, name, code, description, timezone, domain_suffix, status, created_at, updated_at FROM sites`
	args := []any{}
	if onlyID != "" {
		query += ` WHERE id = ?`
		args = append(args, onlyID)
	}
	query += ` ORDER BY name, id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("center: list sites: %w", err)
	}
	result := make([]SiteView, 0)
	for rows.Next() {
		var site SiteView
		var createdAt, updatedAt string
		if err := rows.Scan(&site.ID, &site.OrganizationID, &site.Name, &site.Code, &site.Description, &site.Timezone, &site.DomainSuffix, &site.Status, &createdAt, &updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		site.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		site.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		result = append(result, site)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		site := &result[index]
		gatewayRows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM site_gateways WHERE site_id = ? ORDER BY agent_id`, site.ID)
		if err != nil {
			return nil, err
		}
		for gatewayRows.Next() {
			var agentID string
			if err := gatewayRows.Scan(&agentID); err != nil {
				gatewayRows.Close()
				return nil, err
			}
			site.GatewayNodes = append(site.GatewayNodes, agentID)
		}
		if err := gatewayRows.Close(); err != nil {
			return nil, err
		}
		if err := gatewayRows.Err(); err != nil {
			return nil, err
		}
		if site.GatewayNodes == nil {
			site.GatewayNodes = []string{}
		}
		if len(site.GatewayNodes) == 0 {
			site.GatewayStatus = "unconfigured"
		} else {
			var unhealthy int
			if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM site_gateways sg
				JOIN agents a ON a.id = sg.agent_id
				LEFT JOIN gateway_components c ON c.gateway_node_id = sg.agent_id
				WHERE sg.site_id = ? AND (a.last_seen_at <= ? OR a.gateway_healthy = 0 OR c.status IS NULL OR c.status <> 'ready')`, site.ID, s.now().UTC().Add(-45*time.Second).Format(time.RFC3339Nano)).Scan(&unhealthy); err != nil {
				return nil, err
			}
			site.GatewayStatus = "ready"
			if unhealthy != 0 {
				site.GatewayStatus = "degraded"
			}
		}
	}
	return result, nil
}

func (s *Store) ListOrganizations(ctx context.Context) ([]OrganizationView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, updated_at FROM organizations ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("center: list organizations: %w", err)
	}
	defer rows.Close()
	result := make([]OrganizationView, 0)
	for rows.Next() {
		var value OrganizationView
		var created, updated string
		if err := rows.Scan(&value.ID, &value.Name, &created, &updated); err != nil {
			return nil, err
		}
		value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		result = append(result, value)
	}
	return result, rows.Err()
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
