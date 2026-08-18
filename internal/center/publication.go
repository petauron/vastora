package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/networking"
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
	DNSProvider     string `json:"dnsProvider"`
	ConfirmHighRisk bool   `json:"confirmHighRisk,omitempty"`
}

type PublicationView struct {
	ID              string                `json:"id"`
	ServiceID       string                `json:"serviceId"`
	Kind            string                `json:"kind"`
	GatewayNodeID   string                `json:"gatewayNodeId,omitempty"`
	Hostname        string                `json:"hostname"`
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
	input.DNSProvider = strings.TrimSpace(input.DNSProvider)
	if input.ServiceID == "" || !domainSuffixPattern.MatchString(input.Hostname) {
		return PublicationView{}, errors.New("center: service and a valid hostname are required")
	}
	if !validPublicationKind(input.Kind) {
		return PublicationView{}, errors.New("center: unsupported publication kind")
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
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE gateway_node_id = ? AND hostname = ? AND status <> 'stopped'`, gatewayID, input.Hostname).Scan(&duplicate); err != nil {
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

	id, err := randomToken(18)
	if err != nil {
		return PublicationView{}, err
	}
	now := s.now().UTC()
	tlsEnabled := webService && (input.Kind == publicationPublic || input.Kind == publicationCloudflare)
	if _, err := tx.ExecContext(ctx, `INSERT INTO publications(id, service_id, kind, gateway_node_id, hostname, dns_provider, tls_enabled, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, input.ServiceID, input.Kind, nullableString(gatewayID), input.Hostname, input.DNSProvider, tlsEnabled, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return PublicationView{}, fmt.Errorf("center: create publication: %w", err)
	}
	dnsTaskID := dnsTaskID(id, 1)
	if input.DNSProvider != "manual" {
		if err := s.recordTaskEvent(ctx, tx, dnsTaskID, gatewayID, "dns.record.apply", 1, "queued", input.DNSProvider+" DNS record queued"); err != nil {
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
	if input.DNSProvider == "cloudflare" {
		if err := s.reconcileCloudflarePublication(ctx, id); err != nil {
			message := strings.TrimSpace(err.Error())
			if len(message) > 1024 {
				message = message[:1024]
			}
			_, _ = s.db.ExecContext(ctx, `UPDATE publications SET status = 'failed', last_error = ?, updated_at = ? WHERE id = ?`, message, s.now().UTC().Format(time.RFC3339Nano), id)
			_ = s.recordStandaloneTaskEvent(ctx, dnsTaskID, gatewayID, "dns.record.apply", 1, "failed", message)
		} else {
			_ = s.recordStandaloneTaskEvent(ctx, dnsTaskID, gatewayID, "dns.record.apply", 1, "succeeded", "Cloudflare DNS record applied")
		}
	}
	if input.DNSProvider == "headscale" {
		if err := s.reconcileHeadscaleDNS(ctx); err != nil {
			message := strings.TrimSpace(err.Error())
			if len(message) > 1024 {
				message = message[:1024]
			}
			_, _ = s.db.ExecContext(ctx, `UPDATE publications SET status = 'failed', last_error = ?, updated_at = ? WHERE id = ?`, message, s.now().UTC().Format(time.RFC3339Nano), id)
			_ = s.recordStandaloneTaskEvent(ctx, dnsTaskID, gatewayID, "dns.record.apply", 1, "failed", message)
		} else {
			_ = s.recordStandaloneTaskEvent(ctx, dnsTaskID, gatewayID, "dns.record.apply", 1, "succeeded", "Headscale DNS records applied")
		}
	}
	return s.Publication(ctx, id)
}

func (s *Store) StopPublication(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind, dnsProvider, dnsRecordID string
	var revision int64
	var gatewayID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT kind, gateway_node_id, dns_provider, dns_record_id, desired_revision FROM publications WHERE id = ?`, id).Scan(&kind, &gatewayID, &dnsProvider, &dnsRecordID, &revision); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: publication not found")
	} else if err != nil {
		return err
	}
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'stopped', desired_revision = desired_revision + 1, last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), id); err != nil {
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

type publicationCleanup struct {
	ID          string
	Kind        string
	GatewayID   string
	DNSProvider string
	DNSRecordID string
	Revision    int64
}

func publicationCleanups(rows *sql.Rows) ([]publicationCleanup, error) {
	defer rows.Close()
	values := []publicationCleanup{}
	for rows.Next() {
		var value publicationCleanup
		if err := rows.Scan(&value.ID, &value.Kind, &value.GatewayID, &value.DNSProvider, &value.DNSRecordID, &value.Revision); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) servicePublicationCleanups(ctx context.Context, tx *sql.Tx, serviceID string) ([]publicationCleanup, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, kind, COALESCE(gateway_node_id, ''), dns_provider, dns_record_id, desired_revision + 1
		FROM publications WHERE service_id = ? AND status <> 'stopped' ORDER BY id`, serviceID)
	if err != nil {
		return nil, err
	}
	return publicationCleanups(rows)
}

func (s *Store) applicationPublicationCleanups(ctx context.Context, tx *sql.Tx, applicationID string) ([]publicationCleanup, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.kind, COALESCE(p.gateway_node_id, ''), p.dns_provider, p.dns_record_id, p.desired_revision + 1
		FROM publications p JOIN services s ON s.id = p.service_id
		WHERE s.application_id = ? AND p.status <> 'stopped' ORDER BY p.id`, applicationID)
	if err != nil {
		return nil, err
	}
	return publicationCleanups(rows)
}

func (s *Store) cleanupStoppedPublications(ctx context.Context, values []publicationCleanup) error {
	var cleanupErrors []error
	headscaleValues := []publicationCleanup{}
	for _, value := range values {
		if value.DNSRecordID != "" || value.Kind == publicationCloudflare {
			err := s.removeCloudflarePublication(ctx, value.ID, value.Kind, value.GatewayID, value.DNSRecordID)
			if recordErr := s.finishPublicationCleanup(ctx, value, "cloudflare", err); recordErr != nil {
				cleanupErrors = append(cleanupErrors, recordErr)
			}
			continue
		}
		if value.DNSProvider == "headscale" {
			headscaleValues = append(headscaleValues, value)
		}
	}
	if len(headscaleValues) != 0 {
		err := s.reconcileHeadscaleDNS(ctx)
		for _, value := range headscaleValues {
			if recordErr := s.finishPublicationCleanup(ctx, value, "headscale", err); recordErr != nil {
				cleanupErrors = append(cleanupErrors, recordErr)
			}
		}
	}
	return errors.Join(cleanupErrors...)
}

func (s *Store) finishPublicationCleanup(ctx context.Context, value publicationCleanup, provider string, operationErr error) error {
	message := ""
	if operationErr != nil {
		message = strings.TrimSpace(operationErr.Error())
		if len(message) > 1024 {
			message = message[:1024]
		}
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE publications SET last_error = ?, updated_at = ? WHERE id = ? AND status = 'stopped'`, message, s.now().UTC().Format(time.RFC3339Nano), value.ID); err != nil {
		if operationErr != nil {
			return errors.Join(operationErr, err)
		}
		return err
	}
	return s.recordDNSRemoval(ctx, value.ID, value.GatewayID, value.Revision, provider, operationErr)
}

func dnsTaskID(publicationID string, revision int64) string {
	return fmt.Sprintf("dns-%s-r%d", publicationID, revision)
}

func (s *Store) recordDNSRemoval(ctx context.Context, publicationID, agentID string, revision int64, provider string, operationErr error) error {
	event, message := "succeeded", provider+" DNS record removed"
	if operationErr != nil {
		event, message = "failed", operationErr.Error()
	}
	if err := s.recordStandaloneTaskEvent(ctx, dnsTaskID(publicationID, revision), agentID, "dns.record.remove", revision, event, message); err != nil && operationErr == nil {
		return err
	}
	return operationErr
}

func (s *Store) Publication(ctx context.Context, id string) (PublicationView, error) {
	var value PublicationView
	var gatewayID sql.NullString
	var tls int
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, service_id, kind, gateway_node_id, hostname, dns_provider, dns_record_id, tls_enabled, desired_revision, applied_revision, status, last_error, created_at, updated_at FROM publications WHERE id = ?`, id).Scan(
		&value.ID, &value.ServiceID, &value.Kind, &gatewayID, &value.Hostname, &value.DNSProvider, &value.DNSRecordID, &tls, &value.DesiredRevision, &value.AppliedRevision, &value.Status, &value.LastError, &created, &updated)
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

func (s *Store) publicationDNSRecord(ctx context.Context, publication PublicationView) (*DNSRecordInstruction, error) {
	record := &DNSRecordInstruction{Name: publication.Hostname}
	var address string
	switch publication.Kind {
	case publicationLAN:
		err := s.db.QueryRowContext(ctx, `SELECT n.lan_address FROM agent_network_profiles n JOIN publications p ON p.gateway_node_id = n.agent_id WHERE p.id = ?`, publication.ID).Scan(&address)
		if err != nil {
			return nil, err
		}
	case publicationHeadscale:
		err := s.db.QueryRowContext(ctx, `SELECT n.headscale_address FROM agent_network_profiles n JOIN publications p ON p.gateway_node_id = n.agent_id WHERE p.id = ?`, publication.ID).Scan(&address)
		if err != nil {
			return nil, err
		}
	case publicationPublic, publicationShared443:
		err := s.db.QueryRowContext(ctx, `SELECT n.public_address FROM agent_network_profiles n JOIN publications p ON p.gateway_node_id = n.agent_id WHERE p.id = ?`, publication.ID).Scan(&address)
		if err != nil {
			return nil, err
		}
	case publicationCloudflare:
		var tunnelID string
		err := s.db.QueryRowContext(ctx, `SELECT t.tunnel_id FROM cloudflare_tunnels t JOIN publications p ON p.gateway_node_id = t.agent_id WHERE p.id = ?`, publication.ID).Scan(&tunnelID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		record.Type, record.Value, record.Proxy = "CNAME", tunnelID+".cfargotunnel.com", true
		return record, nil
	default:
		return nil, errors.New("center: stored publication kind is invalid")
	}
	ip := net.ParseIP(address)
	if ip == nil {
		return nil, errors.New("center: publication entry address is invalid")
	}
	record.Type, record.Value = "AAAA", ip.String()
	if ip.To4() != nil {
		record.Type = "A"
	}
	return record, nil
}

// VerifyPublication performs an on-demand DNS and reachability check from the
// Center. A check that has not passed remains pending rather than being marked
// failed, because DNS and certificate propagation are expected to be eventual.
func (s *Store) VerifyPublication(ctx context.Context, id string) (PublicationView, error) {
	publication, err := s.Publication(ctx, id)
	if err != nil {
		return PublicationView{}, err
	}
	if publication.Status == "stopped" {
		return PublicationView{}, errors.New("center: stopped publication cannot be verified")
	}
	var protocol, endpoint, routeStatus string
	err = s.db.QueryRowContext(ctx, `SELECT s.protocol, s.endpoint,
		COALESCE((SELECT r.status FROM routes r WHERE r.publication_id = p.id LIMIT 1), '')
		FROM publications p JOIN services s ON s.id = p.service_id WHERE p.id = ?`, id).Scan(&protocol, &endpoint, &routeStatus)
	if err != nil {
		return PublicationView{}, err
	}
	if routeStatus != "" && routeStatus != "ready" {
		return s.recordPublicationVerification(ctx, id, "gateway configuration is not ready")
	}
	addresses, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, publication.Hostname)
	if lookupErr != nil || len(addresses) == 0 {
		return s.recordPublicationVerification(ctx, id, "DNS record has not propagated")
	}
	if publication.Kind != publicationCloudflare && publication.DNSRecord != nil {
		matched := false
		for _, address := range addresses {
			if address.IP.String() == publication.DNSRecord.Value {
				matched = true
				break
			}
		}
		if !matched {
			return s.recordPublicationVerification(ctx, id, "DNS does not resolve to the selected entry address")
		}
	}

	if protocol == "tcp" || protocol == "udp" {
		_, port, splitErr := net.SplitHostPort(endpoint)
		if splitErr != nil {
			return PublicationView{}, errors.New("center: stored service endpoint is invalid")
		}
		if protocol == "udp" {
			return s.recordPublicationVerification(ctx, id, "UDP reachability cannot be proven automatically; verify it from an external client")
		}
		if publication.Kind == publicationShared443 {
			port = "443"
		}
		connection, dialErr := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(publication.Hostname, port))
		if dialErr != nil {
			return s.recordPublicationVerification(ctx, id, "public port is not reachable")
		}
		_ = connection.Close()
		return s.markPublicationReady(ctx, id)
	}

	scheme := "http"
	if publication.TLSEnabled {
		scheme = "https"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+publication.Hostname+"/", nil)
	if err != nil {
		return PublicationView{}, err
	}
	client := &http.Client{Timeout: 12 * time.Second}
	response, requestErr := client.Do(request)
	if requestErr != nil {
		return s.recordPublicationVerification(ctx, id, "service health check has not passed: "+requestErr.Error())
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode >= 500 {
		return s.recordPublicationVerification(ctx, id, "service returned "+response.Status)
	}
	return s.markPublicationReady(ctx, id)
}

func (s *Store) recordPublicationVerification(ctx context.Context, id, message string) (PublicationView, error) {
	message = strings.TrimSpace(message)
	if len(message) > 1024 {
		message = message[:1024]
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE publications SET status = 'pending', last_error = ?, updated_at = ? WHERE id = ? AND status <> 'stopped'`, message, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return PublicationView{}, err
	}
	return s.Publication(ctx, id)
}

func (s *Store) markPublicationReady(ctx context.Context, id string) (PublicationView, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE publications SET applied_revision = desired_revision, status = 'ready', last_error = '', updated_at = ? WHERE id = ? AND status <> 'stopped'`, s.now().UTC().Format(time.RFC3339Nano), id); err != nil {
		return PublicationView{}, err
	}
	return s.Publication(ctx, id)
}

func (s *Store) ListPublications(ctx context.Context) ([]PublicationView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM publications ORDER BY updated_at DESC, id`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	values := make([]PublicationView, 0, len(ids))
	for _, id := range ids {
		value, err := s.Publication(ctx, id)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (s *Store) upsertPublicationRoute(ctx context.Context, tx *sql.Tx, publicationID, siteID, serviceID, gatewayID, hostname, protocol, endpoint string, tlsEnabled bool, now time.Time) error {
	upstreams, _ := json.Marshal([]string{endpoint})
	var routeID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM routes WHERE publication_id = ? AND gateway_node_id = ?`, publicationID, gatewayID).Scan(&routeID)
	if errors.Is(err, sql.ErrNoRows) {
		routeID, err = randomToken(18)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO routes(id, publication_id, site_id, service_id, gateway_node_id, hostname, protocol, upstreams_json, tls_enabled, status, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, routeID, publicationID, siteID, serviceID, gatewayID, hostname, protocol, upstreams, tlsEnabled, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	} else if err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE routes SET hostname = ?, protocol = ?, upstreams_json = ?, tls_enabled = ?, status = 'pending', last_error = '', updated_at = ? WHERE id = ?`, hostname, protocol, upstreams, tlsEnabled, now.Format(time.RFC3339Nano), routeID)
	}
	if err != nil {
		return fmt.Errorf("center: save publication route: %w", err)
	}
	return nil
}

func (s *Store) reconcileApplicationPublications(ctx context.Context, tx *sql.Tx, applicationID string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT p.id, p.kind, p.gateway_node_id, p.hostname, p.tls_enabled, s.id, s.site_id, s.protocol, s.endpoint
		FROM publications p JOIN services s ON s.id = p.service_id
		WHERE s.application_id = ? AND p.status <> 'stopped' AND s.status <> 'stopped'`, applicationID)
	if err != nil {
		return err
	}
	type item struct {
		publicationID, kind, gatewayID, hostname, serviceID, siteID, protocol, endpoint string
		tls                                                                             bool
	}
	items := []item{}
	for rows.Next() {
		var value item
		var gatewayID sql.NullString
		var tls int
		if err := rows.Scan(&value.publicationID, &value.kind, &gatewayID, &value.hostname, &tls, &value.serviceID, &value.siteID, &value.protocol, &value.endpoint); err != nil {
			rows.Close()
			return err
		}
		value.gatewayID, value.tls = gatewayID.String, tls == 1
		items = append(items, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	gateways, tunnels := map[string]bool{}, map[string]bool{}
	for _, value := range items {
		web := value.protocol == "http" || value.protocol == "https"
		if isGatewayPublication(value.kind, web) {
			if err := s.upsertPublicationRoute(ctx, tx, value.publicationID, value.siteID, value.serviceID, value.gatewayID, value.hostname, value.protocol, value.endpoint, value.tls, now); err != nil {
				return err
			}
			gateways[value.gatewayID] = true
		}
		if value.kind == publicationShared443 {
			gateways[value.gatewayID] = true
		}
		if value.kind == publicationCloudflare {
			tunnels[value.gatewayID] = true
		}
		if _, err := tx.ExecContext(ctx, `UPDATE publications SET status = 'pending', desired_revision = desired_revision + 1, last_error = '', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), value.publicationID); err != nil {
			return err
		}
	}
	for id := range gateways {
		if err := s.queueGatewayState(ctx, tx, id, now); err != nil {
			return err
		}
	}
	for id := range tunnels {
		if err := s.queueTunnelState(ctx, tx, id, now); err != nil {
			return err
		}
	}
	return nil
}

func validateGatewayForPublication(ctx context.Context, tx *sql.Tx, siteID, gatewayID, kind string) error {
	var capabilitiesJSON, enabledJSON []byte
	var direct int
	var gatewaySite string
	if err := tx.QueryRowContext(ctx, `SELECT a.site_id, a.capabilities_json, p.enabled_kinds_json, p.direct_public
		FROM agents a
		JOIN agent_network_profiles p ON p.agent_id = a.id
		JOIN site_gateways sg ON sg.agent_id = a.id AND sg.site_id = a.site_id
		WHERE a.id = ?`, gatewayID).Scan(&gatewaySite, &capabilitiesJSON, &enabledJSON, &direct); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: entry node must be selected as a site Gateway with a confirmed network profile")
	} else if err != nil {
		return err
	}
	if gatewaySite != siteID {
		return errors.New("center: entry node must belong to the service site")
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(capabilitiesJSON, &capabilities) != nil || !capabilities.Gateway {
		return errors.New("center: entry node does not report Gateway capability")
	}
	var enabled []string
	if json.Unmarshal(enabledJSON, &enabled) != nil {
		return errors.New("center: entry node has an invalid network profile")
	}
	required := networking.KindLAN
	if kind == publicationHeadscale {
		required = networking.KindHeadscale
	}
	if kind == publicationPublic || kind == publicationShared443 {
		required = networking.KindPublic
		if direct != 1 {
			return errors.New("center: entry node is not approved for direct public ingress")
		}
	}
	for _, value := range enabled {
		if value == required {
			return nil
		}
	}
	return fmt.Errorf("center: entry node does not have %s networking enabled", required)
}

func validateTunnelNode(ctx context.Context, tx *sql.Tx, siteID, nodeID string) error {
	var capabilitiesJSON []byte
	var nodeSite string
	if err := tx.QueryRowContext(ctx, `SELECT site_id, capabilities_json FROM agents WHERE id = ?`, nodeID).Scan(&nodeSite, &capabilitiesJSON); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: tunnel node not found")
	} else if err != nil {
		return err
	}
	var capabilities NodeCapabilities
	if nodeSite != siteID || json.Unmarshal(capabilitiesJSON, &capabilities) != nil || !capabilities.Tunnel {
		return errors.New("center: selected node does not have Tunnel capability in this site")
	}
	return nil
}

func validatePublicationOrigin(ctx context.Context, tx *sql.Tx, applicationNodeID, gatewayNodeID, endpoint string) error {
	if applicationNodeID == gatewayNodeID {
		return nil
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return errors.New("center: stored service endpoint is invalid")
	}
	originIP := net.ParseIP(host)
	if originIP == nil || originIP.IsLoopback() || originIP.IsUnspecified() {
		return errors.New("center: cross-node entry requires a routable private service address")
	}
	var serviceAddress, lanAddress, headscaleAddress string
	var originEnabledJSON, gatewayEnabledJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT service_address, lan_address, headscale_address, enabled_kinds_json FROM agent_network_profiles WHERE agent_id = ?`, applicationNodeID).Scan(&serviceAddress, &lanAddress, &headscaleAddress, &originEnabledJSON); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: application node needs a confirmed network profile")
	} else if err != nil {
		return err
	}
	serviceIP := net.ParseIP(serviceAddress)
	if serviceIP == nil || !originIP.Equal(serviceIP) {
		return errors.New("center: service endpoint no longer matches the application node network profile")
	}
	if err := tx.QueryRowContext(ctx, `SELECT enabled_kinds_json FROM agent_network_profiles WHERE agent_id = ?`, gatewayNodeID).Scan(&gatewayEnabledJSON); errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: cross-node entry requires a confirmed gateway network profile")
	} else if err != nil {
		return err
	}
	var originEnabled, gatewayEnabled []string
	if json.Unmarshal(originEnabledJSON, &originEnabled) != nil || json.Unmarshal(gatewayEnabledJSON, &gatewayEnabled) != nil {
		return errors.New("center: node has an invalid network profile")
	}
	originKinds := map[string]bool{}
	for _, kind := range originEnabled {
		if kind == networking.KindLAN && net.ParseIP(lanAddress) != nil && originIP.Equal(net.ParseIP(lanAddress)) {
			originKinds[kind] = true
		}
		if kind == networking.KindHeadscale && net.ParseIP(headscaleAddress) != nil && originIP.Equal(net.ParseIP(headscaleAddress)) {
			originKinds[kind] = true
		}
	}
	for _, kind := range gatewayEnabled {
		if originKinds[kind] {
			return nil
		}
	}
	return errors.New("center: entry node cannot reach the application private service network")
}

func validateDirectPublicNode(ctx context.Context, tx *sql.Tx, nodeID string) (string, error) {
	var publicAddress, enabledJSON string
	var direct int
	if err := tx.QueryRowContext(ctx, `SELECT public_address, CAST(enabled_kinds_json AS TEXT), direct_public FROM agent_network_profiles WHERE agent_id = ?`, nodeID).Scan(&publicAddress, &enabledJSON, &direct); errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("center: application node needs a confirmed public network profile")
	} else if err != nil {
		return "", err
	}
	var enabled []string
	if json.Unmarshal([]byte(enabledJSON), &enabled) != nil || direct != 1 || net.ParseIP(publicAddress) == nil {
		return "", errors.New("center: application node is not approved for direct public ingress")
	}
	for _, kind := range enabled {
		if kind == networking.KindPublic {
			return net.ParseIP(publicAddress).String(), nil
		}
	}
	return "", errors.New("center: application node does not have public networking enabled")
}

func gatewayHasDirectRaw443(ctx context.Context, tx *sql.Tx, gatewayID string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT s.endpoint FROM publications p JOIN services s ON s.id = p.service_id
		WHERE p.gateway_node_id = ? AND p.kind = 'public_direct' AND p.status <> 'stopped' AND s.protocol IN ('tcp', 'udp')`, gatewayID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var endpoint string
		if err := rows.Scan(&endpoint); err != nil {
			return false, err
		}
		_, port, err := net.SplitHostPort(endpoint)
		if err != nil {
			return false, errors.New("center: stored direct public endpoint is invalid")
		}
		if port == "443" {
			return true, nil
		}
	}
	return false, rows.Err()
}

func validPublicationKind(kind string) bool {
	return kind == publicationLAN || kind == publicationHeadscale || kind == publicationPublic || kind == publicationShared443 || kind == publicationCloudflare
}

func validPublicationDNS(kind, provider string) bool {
	switch kind {
	case publicationLAN:
		return provider == "manual"
	case publicationHeadscale:
		return provider == "headscale" || provider == "manual"
	case publicationPublic, publicationShared443:
		return provider == "cloudflare" || provider == "manual"
	case publicationCloudflare:
		return provider == "cloudflare"
	default:
		return false
	}
}

func isGatewayPublication(kind string, web bool) bool {
	return web && (kind == publicationLAN || kind == publicationHeadscale || kind == publicationPublic)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
