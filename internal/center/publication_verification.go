package center

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// VerifyPublication performs an on-demand DNS and reachability check from the
// Center. A check that has not passed remains pending rather than being marked
// failed, because DNS and certificate propagation are expected to be eventual.
func (s *Store) VerifyPublication(ctx context.Context, id string) (PublicationView, error) {
	publication, err := s.Publication(ctx, id)
	if err != nil {
		return PublicationView{}, err
	}
	return s.verifyPublicationRevision(ctx, id, publication.DesiredRevision)
}

// verifyPublicationRevision fences background verification to the publication
// revision that scheduled it. Interactive checks snapshot the same revision
// before doing any DNS or network I/O.
func (s *Store) verifyPublicationRevision(ctx context.Context, id string, expectedRevision int64) (PublicationView, error) {
	publication, err := s.Publication(ctx, id)
	if err != nil {
		return PublicationView{}, err
	}
	if expectedRevision > 0 && publication.DesiredRevision != expectedRevision {
		return publication, nil
	}
	if publication.Status == "stopped" {
		return PublicationView{}, errors.New("center: stopped publication cannot be verified")
	}
	if err := s.ensureServicePublicationChangeAllowed(ctx, s.db, publication.ServiceID); err != nil {
		return s.recordPublicationVerification(ctx, id, expectedRevision, err.Error())
	}
	if publication.Status == "failed" && publication.DNSProvider != "manual" && (publication.Kind != publicationCloudflare || publication.DNSRecordID == "") {
		succeeded, reconcileErr := s.reconcilePublicationDNS(ctx, publication.ID, publication.GatewayNodeID, publication.DNSProvider, publication.DesiredRevision)
		if reconcileErr != nil {
			return PublicationView{}, reconcileErr
		}
		if !succeeded {
			return s.Publication(ctx, id)
		}
		publication, err = s.Publication(ctx, id)
		if err != nil {
			return PublicationView{}, err
		}
		if expectedRevision > 0 && publication.DesiredRevision != expectedRevision {
			return publication, nil
		}
	}
	if publication.Kind == publicationCloudflare {
		var tunnelStatus string
		var tunnelAppliedRevision int64
		if err := s.db.QueryRowContext(ctx, `SELECT status, applied_revision FROM cloudflare_tunnels WHERE agent_id = ?`, publication.GatewayNodeID).Scan(&tunnelStatus, &tunnelAppliedRevision); err != nil {
			return PublicationView{}, err
		}
		if tunnelStatus != "ready" || tunnelAppliedRevision < publication.DesiredRevision {
			if tunnelStatus == "failed" || publication.Status == "failed" {
				return publication, nil
			}
			return s.recordPublicationVerification(ctx, id, expectedRevision, "Cloudflare Tunnel connector is not ready")
		}
	}
	var protocol, endpoint, routeStatus string
	err = s.db.QueryRowContext(ctx, `SELECT s.protocol, s.endpoint,
		COALESCE((SELECT r.status FROM routes r WHERE r.publication_id = p.id LIMIT 1), '')
		FROM publications p JOIN services s ON s.id = p.service_id WHERE p.id = ?`, id).Scan(&protocol, &endpoint, &routeStatus)
	if err != nil {
		return PublicationView{}, err
	}
	if routeStatus != "" && routeStatus != "ready" {
		return s.recordPublicationVerification(ctx, id, expectedRevision, "gateway configuration is not ready")
	}
	verificationAddress, targetErr := publicationVerificationAddress(ctx, publication)
	if targetErr != nil {
		return s.recordPublicationVerification(ctx, id, expectedRevision, targetErr.Error())
	}

	if protocol == "tcp" || protocol == "udp" {
		_, port, splitErr := net.SplitHostPort(endpoint)
		if splitErr != nil {
			return PublicationView{}, errors.New("center: stored service endpoint is invalid")
		}
		if protocol == "udp" {
			return s.recordPublicationVerification(ctx, id, expectedRevision, "UDP reachability cannot be proven automatically; verify it from an external client")
		}
		if publication.Kind == publicationShared443 {
			port = "443"
		}
		connection, dialErr := (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(verificationAddress, port))
		if dialErr != nil {
			return s.recordPublicationVerification(ctx, id, expectedRevision, "public port is not reachable")
		}
		_ = connection.Close()
		return s.markPublicationReady(ctx, id, expectedRevision)
	}

	scheme := "http"
	if publication.TLSEnabled {
		scheme = "https"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+publication.Hostname+"/", nil)
	if err != nil {
		return PublicationView{}, err
	}
	client, closeClient := publicationVerificationHTTPClient(verificationAddress)
	defer closeClient()
	response, requestErr := client.Do(request)
	if requestErr != nil {
		return s.recordPublicationVerification(ctx, id, expectedRevision, "service health check has not passed: "+requestErr.Error())
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	_ = response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return s.recordPublicationVerification(ctx, id, expectedRevision, "service health check redirect was not accepted")
	}
	if response.StatusCode >= 500 {
		return s.recordPublicationVerification(ctx, id, expectedRevision, "service returned "+response.Status)
	}
	return s.markPublicationReady(ctx, id, expectedRevision)
}

func privatePublicationVerificationAddress(publication PublicationView) (string, bool, error) {
	if publication.Kind != publicationLAN && publication.Kind != publicationHeadscale {
		return "", false, nil
	}
	if publication.DNSRecord == nil || net.ParseIP(publication.DNSRecord.Value) == nil {
		return "", true, errors.New("gateway entry address is not ready")
	}
	return net.ParseIP(publication.DNSRecord.Value).String(), true, nil
}

func publicationVerificationAddress(ctx context.Context, publication PublicationView) (string, error) {
	privateAddress, privateEntry, err := privatePublicationVerificationAddress(publication)
	if err != nil {
		return "", err
	}
	if privateEntry {
		return privateAddress, nil
	}
	addresses, lookupErr := net.DefaultResolver.LookupIPAddr(ctx, publication.Hostname)
	if lookupErr != nil || len(addresses) == 0 {
		return "", errors.New("DNS record has not propagated")
	}
	return publicPublicationVerificationAddress(publication, addresses)
}

func publicPublicationVerificationAddress(publication PublicationView, addresses []net.IPAddr) (string, error) {
	if publication.Kind != publicationCloudflare {
		if publication.DNSRecord == nil {
			return "", errors.New("selected public entry address is not ready")
		}
		expected := net.ParseIP(strings.TrimSpace(publication.DNSRecord.Value))
		if !isPublicPublicationVerificationIP(expected) {
			return "", errors.New("selected public entry address is not publicly routable")
		}
		for _, address := range addresses {
			if address.IP.Equal(expected) {
				return expected.String(), nil
			}
		}
		return "", errors.New("DNS does not resolve to the selected entry address")
	}
	for _, address := range addresses {
		if isPublicPublicationVerificationIP(address.IP) {
			return address.IP.String(), nil
		}
	}
	return "", errors.New("DNS does not resolve to a publicly routable address")
}

func isPublicPublicationVerificationIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// Go's IsPrivate deliberately excludes RFC 6598 shared address space, but
	// a CGNAT address is not a valid direct public verification target.
	if ipv4 := ip.To4(); ipv4 != nil && ipv4[0] == 100 && ipv4[1]&0xc0 == 0x40 {
		return false
	}
	return true
}

func publicationVerificationHTTPClient(address string) (*http.Client, func()) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(target)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(address, port))
	}
	client := &http.Client{
		Timeout:   12 * time.Second,
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return client, transport.CloseIdleConnections
}

func (s *Store) recordPublicationVerification(ctx context.Context, id string, expectedRevision int64, message string) (PublicationView, error) {
	message = strings.TrimSpace(message)
	if len(message) > 1024 {
		message = message[:1024]
	}
	query := `UPDATE publications SET status = 'pending', last_error = ?, updated_at = ? WHERE id = ? AND status <> 'stopped'`
	arguments := []any{message, s.now().UTC().Format(time.RFC3339Nano), id}
	if expectedRevision > 0 {
		query += ` AND desired_revision = ?`
		arguments = append(arguments, expectedRevision)
	}
	if _, err := s.db.ExecContext(ctx, query, arguments...); err != nil {
		return PublicationView{}, err
	}
	return s.Publication(ctx, id)
}

func (s *Store) markPublicationReady(ctx context.Context, id string, expectedRevision int64) (PublicationView, error) {
	query := `UPDATE publications SET applied_revision = desired_revision, status = 'ready', last_error = '', updated_at = ?
		WHERE id = ? AND status <> 'stopped' AND EXISTS (
			SELECT 1 FROM services s JOIN applications a ON a.id = s.application_id
			WHERE s.id = publications.service_id AND s.status IN ('ready', 'publishing') AND a.status = 'running'
			AND NOT EXISTS (SELECT 1 FROM deployments d WHERE d.application_id = a.id AND (d.state IN ('pending', 'running') OR d.reconciliation_required = 1))
			AND NOT EXISTS (SELECT 1 FROM application_commands c WHERE c.application_id = a.id AND (c.state IN ('pending', 'running') OR c.reconciliation_required = 1))
		)`
	arguments := []any{s.now().UTC().Format(time.RFC3339Nano), id}
	if expectedRevision > 0 {
		query += ` AND desired_revision = ?`
		arguments = append(arguments, expectedRevision)
	}
	if _, err := s.db.ExecContext(ctx, query, arguments...); err != nil {
		return PublicationView{}, err
	}
	return s.Publication(ctx, id)
}
