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
	if publication.Status == "stopped" {
		return PublicationView{}, errors.New("center: stopped publication cannot be verified")
	}
	if publication.Status == "failed" && publication.DNSProvider != "manual" {
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
