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
	publicationLAN        = "lan_gateway"
	publicationHeadscale  = "headscale_gateway"
	publicationPublic     = "public_direct"
	publicationShared443  = "public_shared_443"
	publicationCloudflare = "cloudflare_tunnel"
)

type PublicationInput struct {
	ServiceID       string `json:"serviceId"`
	Kind            string `json:"kind"`
	GatewayNodeID   string `json:"gatewayNodeId,omitempty"`
	Hostname        string `json:"hostname"`
	SNIHostname     string `json:"sniHostname,omitempty"`
	DNSProvider     string `json:"dnsProvider"`
	ConfirmHighRisk bool   `json:"confirmHighRisk,omitempty"`
}

type PublicationView struct {
	ID              string                `json:"id"`
	ServiceID       string                `json:"serviceId"`
	Kind            string                `json:"kind"`
	GatewayNodeID   string                `json:"gatewayNodeId,omitempty"`
	Hostname        string                `json:"hostname"`
	SNIHostname     string                `json:"sniHostname,omitempty"`
	DNSProvider     string                `json:"dnsProvider"`
	DNSRecordID     string                `json:"dnsRecordId,omitempty"`
	TLSEnabled      bool                  `json:"tlsEnabled"`
	DesiredRevision int64                 `json:"desiredRevision"`
	AppliedRevision int64                 `json:"appliedRevision"`
	Status          string                `json:"status"`
	LastError       string                `json:"lastError,omitempty"`
	AccessURL       string                `json:"accessUrl,omitempty"`
	DNSRecord       *DNSRecordInstruction `json:"dnsRecord,omitempty"`
	CreatedAt       time.Time             `json:"createdAt"`
	UpdatedAt       time.Time             `json:"updatedAt"`
}

type DNSRecordInstruction struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Proxy bool   `json:"proxy"`
}

func (s *Store) CreatePublication(ctx context.Context, input PublicationInput) (PublicationView, error) {
	input.ServiceID = strings.TrimSpace(input.ServiceID)
	input.Kind = strings.TrimSpace(input.Kind)
	input.GatewayNodeID = strings.TrimSpace(input.GatewayNodeID)
	input.Hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.Hostname), "."))
	input.SNIHostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.SNIHostname), "."))
	input.DNSProvider = strings.TrimSpace(input.DNSProvider)
	if input.ServiceID == "" || !domainSuffixPattern.MatchString(input.Hostname) {
		return PublicationView{}, errors.New("center: service and a valid hostname are required")
	}
	if !validPublicationKind(input.Kind) {
		return PublicationView{}, errors.New("center: unsupported publication kind")
	}
	if input.Kind == publicationShared443 {
		if !domainSuffixPattern.MatchString(input.SNIHostname) {
			return PublicationView{}, errors.New("center: shared 443 requires a valid TLS SNI hostname")
		}
	} else if input.SNIHostname != "" {
		return PublicationView{}, errors.New("center: TLS SNI hostname is only valid for shared 443 access")
	}
	if !validPublicationDNS(input.Kind, input.DNSProvider) {
		return PublicationView{}, errors.New("center: DNS provider is not valid for this publication")
	}
	if input.DNSProvider == "cloudflare" {
		var zoneName string
		if err := s.db.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&zoneName); errors.Is(err, sql.ErrNoRows) {
			return PublicationView{}, errors.New("center: configure Cloudflare before using managed DNS or Tunnel publication")
		} else if err != nil {
			return PublicationView{}, err
		}
		if input.Hostname != zoneName && !strings.HasSuffix(input.Hostname, "."+zoneName) {
			return PublicationView{}, errors.New("center: publication hostname must belong to the configured Cloudflare Zone")
		}
	}
	if input.DNSProvider == "headscale" {
		var mode string
		if err := s.db.QueryRowContext(ctx, `SELECT mode FROM network_integrations WHERE kind = 'headscale' AND status = 'configured'`).Scan(&mode); errors.Is(err, sql.ErrNoRows) {
			return PublicationView{}, errors.New("center: configure built-in Headscale before using managed Headscale DNS")
		} else if err != nil {
			return PublicationView{}, err
		}
		if mode != "builtin" {
			return PublicationView{}, errors.New("center: external Headscale requires manual DNS configuration")
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublicationView{}, err
	}
	defer tx.Rollback()
	var siteID, appNodeID, protocol, endpoint, serviceStatus, observedListen string
	var management int
	if err := tx.QueryRowContext(ctx, `SELECT s.site_id, a.node_id, s.protocol, s.endpoint, s.status, s.management, s.observed_listen
		FROM services s JOIN applications a ON a.id = s.application_id WHERE s.id = ?`, input.ServiceID).Scan(&siteID, &appNodeID, &protocol, &endpoint, &serviceStatus, &management, &observedListen); errors.Is(err, sql.ErrNoRows) {
		return PublicationView{}, errors.New("center: service not found")
	} else if err != nil {
		return PublicationView{}, err
	}
	if serviceStatus == "stopped" || serviceStatus == "failed" {
		return PublicationView{}, errors.New("center: service must be running before it can be published")
	}
	if management == 1 && (input.Kind == publicationPublic || input.Kind == publicationCloudflare) && !input.ConfirmHighRisk {
		return PublicationView{}, errors.New("center: publishing a management page publicly requires explicit high-risk confirmation")
	}
	if _, _, err := net.SplitHostPort(endpoint); err != nil {
		return PublicationView{}, errors.New("center: stored service endpoint is invalid")
	}
	webService := protocol == "http" || protocol == "https"
	if !webService && input.Kind != publicationPublic && input.Kind != publicationShared443 {
		return PublicationView{}, errors.New("center: raw TCP/UDP services only support direct public or shared 443 publication")
	}
	if input.Kind == publicationShared443 && protocol != "tcp" {
		return PublicationView{}, errors.New("center: shared 443 requires a raw TCP service with TLS SNI")
	}

	gatewayID := input.GatewayNodeID
	if gatewayID == "" && (input.Kind == publicationPublic || input.Kind == publicationShared443 || input.Kind == publicationCloudflare) {
		gatewayID = appNodeID
	}
	if input.Kind == publicationLAN || input.Kind == publicationHeadscale || input.Kind == publicationShared443 || (input.Kind == publicationPublic && webService) {
		if gatewayID == "" {
			return PublicationView{}, errors.New("center: select an entry node for this publication")
		}
		if err := validateGatewayForPublication(ctx, tx, siteID, gatewayID, input.Kind); err != nil {
			return PublicationView{}, err
		}
	}
	if input.Kind == publicationPublic && !webService && gatewayID != appNodeID {
		return PublicationView{}, errors.New("center: raw public ports must listen on the application node")
	}
	if input.Kind == publicationPublic && !webService {
		publicAddress, err := validateDirectPublicNode(ctx, tx, appNodeID)
		if err != nil {
			return PublicationView{}, err
		}
		listenIP := net.ParseIP(observedListen)
		if listenIP != nil && !listenIP.IsUnspecified() && listenIP.String() != publicAddress {
			return PublicationView{}, errors.New("center: raw port is not listening on the confirmed public address")
		}
		_, port, _ := net.SplitHostPort(endpoint)
		if port == "443" {
			var shared int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE gateway_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped'`, appNodeID).Scan(&shared); err != nil {
				return PublicationView{}, err
			}
			if shared != 0 {
				return PublicationView{}, errors.New("center: this gateway already shares public port 443; use a non-443 application port and the shared 443 access method")
			}
		}
	}
	if input.Kind == publicationShared443 {
		if err := validatePublicationOrigin(ctx, tx, appNodeID, gatewayID, endpoint); err != nil {
			return PublicationView{}, err
		}
		_, port, _ := net.SplitHostPort(endpoint)
		if appNodeID == gatewayID && port == "443" {
			return PublicationView{}, errors.New("center: move the application inbound away from port 443 before enabling the shared 443 gateway")
		}
		occupied, err := gatewayHasDirectRaw443(ctx, tx, gatewayID)
		if err != nil {
			return PublicationView{}, err
		}
		if occupied {
			return PublicationView{}, errors.New("center: a direct raw publication already owns port 443 on this gateway; move that inbound before enabling shared 443")
		}
		var duplicate int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE gateway_node_id = ? AND sni_hostname = ? AND status <> 'stopped'`, gatewayID, input.SNIHostname).Scan(&duplicate); err != nil {
			return PublicationView{}, err
		}
		if duplicate != 0 {
			return PublicationView{}, errors.New("center: this SNI hostname is already used on the selected gateway")
		}
	}
	if input.Kind == publicationCloudflare {
		if gatewayID == "" {
			return PublicationView{}, errors.New("center: select a tunnel node")
		}
		if err := validateTunnelNode(ctx, tx, siteID, gatewayID); err != nil {
			return PublicationView{}, err
		}
	}
	if webService && gatewayID != "" {
		if err := validatePublicationOrigin(ctx, tx, appNodeID, gatewayID, endpoint); err != nil {
			return PublicationView{}, err
		}
	}

	now := s.now().UTC()
	tlsEnabled := webService && (input.Kind == publicationPublic || input.Kind == publicationCloudflare)
	id, revision := "", int64(1)
	var existingStatus string
	var cleanupPending int
	err = tx.QueryRowContext(ctx, `SELECT id, status, cleanup_pending, desired_revision FROM publications WHERE service_id = ? AND kind = ? AND hostname = ?`, input.ServiceID, input.Kind, input.Hostname).Scan(&id, &existingStatus, &cleanupPending, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		id, err = randomToken(18)
		if err != nil {
			return PublicationView{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider, tls_enabled, status, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, input.ServiceID, input.Kind, nullableString(gatewayID), input.Hostname, input.SNIHostname, input.DNSProvider, tlsEnabled, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return PublicationView{}, fmt.Errorf("center: create publication: %w", err)
		}
		revision = 1
	} else if err != nil {
		return PublicationView{}, err
	} else {
		if existingStatus != "stopped" {
			return PublicationView{}, errors.New("center: this service access entry already exists")
		}
		if cleanupPending != 0 {
			return PublicationView{}, errors.New("center: the previous access entry is still removing external resources; retry after cleanup succeeds")
		}
		revision++
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET gateway_node_id = ?, sni_hostname = ?, dns_provider = ?, dns_record_id = '', tls_enabled = ?, desired_revision = ?, status = 'pending', last_error = '', cleanup_attempt = 0, cleanup_retry_at = '', updated_at = ? WHERE id = ?`, nullableString(gatewayID), input.SNIHostname, input.DNSProvider, tlsEnabled, revision, now.Format(time.RFC3339Nano), id); err != nil {
			return PublicationView{}, fmt.Errorf("center: reactivate publication: %w", err)
		}
	}
	taskID := dnsTaskID(id, revision)
	if input.DNSProvider != "manual" {
		if err := s.recordTaskEvent(ctx, tx, taskID, gatewayID, "dns.record.apply", revision, "queued", input.DNSProvider+" DNS record queued"); err != nil {
			return PublicationView{}, err
		}
	}
	if isGatewayPublication(input.Kind, webService) {
		if err := s.upsertPublicationRoute(ctx, tx, id, siteID, input.ServiceID, gatewayID, input.Hostname, protocol, endpoint, tlsEnabled, now); err != nil {
			return PublicationView{}, err
		}
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return PublicationView{}, err
		}
	} else if input.Kind == publicationShared443 {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return PublicationView{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return PublicationView{}, err
	}
	if _, err := s.reconcilePublicationDNS(ctx, id, gatewayID, input.DNSProvider, revision); err != nil {
		return PublicationView{}, err
	}
	return s.Publication(ctx, id)
}

func (s *Store) StopPublication(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind, dnsProvider, dnsRecordID, status string
	var revision int64
	var gatewayID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT kind, gateway_node_id, dns_provider, dns_record_id, desired_revision, status FROM publications WHERE id = ?`, id).Scan(&kind, &gatewayID, &dnsProvider, &dnsRecordID, &revision, &status); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: publication not found")
	} else if err != nil {
		return err
	}
	if status == "stopped" {
		return errors.New("center: publication is already stopped")
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped', desired_revision = desired_revision + 1,
		cleanup_pending = CASE WHEN dns_record_id <> '' OR kind = 'cloudflare_tunnel' OR dns_provider = 'headscale' THEN 1 ELSE 0 END,
		cleanup_attempt = 0, cleanup_retry_at = '', last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	if gatewayID.Valid {
		if kind != publicationCloudflare {
			if _, err := tx.ExecContext(ctx, `DELETE FROM routes WHERE publication_id = ?`, id); err != nil {
				return err
			}
			if err := s.queueGatewayState(ctx, tx, gatewayID.String, now); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return s.cleanupStoppedPublications(ctx, []publicationCleanup{{ID: id, Kind: kind, GatewayID: gatewayID.String, DNSProvider: dnsProvider, DNSRecordID: dnsRecordID, Revision: revision + 1}})
}

func (s *Store) Publication(ctx context.Context, id string) (PublicationView, error) {
	var value PublicationView
	var gatewayID sql.NullString
	var tls int
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, service_id, kind, gateway_node_id, hostname, sni_hostname, dns_provider, dns_record_id, tls_enabled, desired_revision, applied_revision, status, last_error, created_at, updated_at FROM publications WHERE id = ?`, id).Scan(
		&value.ID, &value.ServiceID, &value.Kind, &gatewayID, &value.Hostname, &value.SNIHostname, &value.DNSProvider, &value.DNSRecordID, &tls, &value.DesiredRevision, &value.AppliedRevision, &value.Status, &value.LastError, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return PublicationView{}, errors.New("center: publication not found")
	}
	if err != nil {
		return PublicationView{}, err
	}
	value.GatewayNodeID = gatewayID.String
	value.TLSEnabled = tls == 1
	value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if value.Status == "ready" {
		value.AccessURL, err = s.publicationAccessURL(ctx, value)
		if err != nil {
			return PublicationView{}, err
		}
	}
	value.DNSRecord, err = s.publicationDNSRecord(ctx, value)
	if err != nil {
		return PublicationView{}, err
	}
	return value, nil
}

func (s *Store) publicationAccessURL(ctx context.Context, publication PublicationView) (string, error) {
	var protocol, serviceName, appKey string
	if err := s.db.QueryRowContext(ctx, `SELECT s.protocol, s.name, a.app_key FROM services s JOIN applications a ON a.id = s.application_id WHERE s.id = ?`, publication.ServiceID).Scan(&protocol, &serviceName, &appKey); err != nil {
		return "", err
	}
	if protocol != "http" && protocol != "https" {
		return "", nil
	}
	path := "/"
	apps, err := s.ListApps(ctx)
	if err != nil {
		return "", err
	}
	for _, app := range apps {
		if app.Key == appKey && app.App.Homepage != nil && app.App.Homepage.Service == serviceName {
			path = app.App.Homepage.Path
			break
		}
	}
	scheme := "http"
	if publication.TLSEnabled {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: publication.Hostname, Path: path}).String(), nil
}

func (s *Store) ListPublications(ctx context.Context) ([]PublicationView, error) {
	apps, err := s.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	return s.listPublications(ctx, apps)
}

func (s *Store) listPublications(ctx context.Context, apps []AppView) ([]PublicationView, error) {
	homepagePaths := map[string]string{}
	for _, app := range apps {
		if app.App.Homepage != nil {
			homepagePaths[app.Key+"\x00"+app.App.Homepage.Service] = app.App.Homepage.Path
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT
		p.id, p.service_id, p.kind, COALESCE(p.gateway_node_id, ''), p.hostname, p.sni_hostname, p.dns_provider, p.dns_record_id,
		p.tls_enabled, p.desired_revision, p.applied_revision, p.status, p.last_error, p.created_at, p.updated_at,
		s.protocol, s.name, a.app_key,
		COALESCE(n.lan_address, ''), COALESCE(n.headscale_address, ''), COALESCE(n.public_address, ''), COALESCE(t.tunnel_id, '')
		FROM publications p
		JOIN services s ON s.id = p.service_id
		JOIN applications a ON a.id = s.application_id
		LEFT JOIN agent_network_profiles n ON n.agent_id = p.gateway_node_id
		LEFT JOIN cloudflare_tunnels t ON t.agent_id = p.gateway_node_id
		ORDER BY p.updated_at DESC, p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []PublicationView{}
	for rows.Next() {
		var value PublicationView
		var tls int
		var created, updated, protocol, serviceName, appKey string
		var lanAddress, headscaleAddress, publicAddress, tunnelID string
		if err := rows.Scan(
			&value.ID, &value.ServiceID, &value.Kind, &value.GatewayNodeID, &value.Hostname, &value.SNIHostname, &value.DNSProvider, &value.DNSRecordID,
			&tls, &value.DesiredRevision, &value.AppliedRevision, &value.Status, &value.LastError, &created, &updated,
			&protocol, &serviceName, &appKey, &lanAddress, &headscaleAddress, &publicAddress, &tunnelID,
		); err != nil {
			return nil, err
		}
		value.TLSEnabled = tls == 1
		value.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, errors.New("center: invalid publication creation timestamp")
		}
		value.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, errors.New("center: invalid publication update timestamp")
		}
		if value.Status == "ready" && (protocol == "http" || protocol == "https") {
			path := homepagePaths[appKey+"\x00"+serviceName]
			if path == "" {
				path = "/"
			}
			scheme := "http"
			if value.TLSEnabled {
				scheme = "https"
			}
			value.AccessURL = (&url.URL{Scheme: scheme, Host: value.Hostname, Path: path}).String()
		}
		address := ""
		switch value.Kind {
		case publicationLAN:
			address = lanAddress
		case publicationHeadscale:
			address = headscaleAddress
		case publicationPublic, publicationShared443:
			address = publicAddress
		case publicationCloudflare:
			if tunnelID != "" {
				value.DNSRecord = &DNSRecordInstruction{Type: "CNAME", Name: value.Hostname, Value: tunnelID + ".cfargotunnel.com", Proxy: true}
			}
		default:
			return nil, errors.New("center: stored publication kind is invalid")
		}
		if value.Kind != publicationCloudflare {
			ip := net.ParseIP(address)
			if ip == nil {
				if value.Status == "stopped" {
					values = append(values, value)
					continue
				}
				return nil, errors.New("center: publication entry address is invalid")
			}
			recordType := "AAAA"
			if ip.To4() != nil {
				recordType = "A"
			}
			value.DNSRecord = &DNSRecordInstruction{Type: recordType, Name: value.Hostname, Value: ip.String()}
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
