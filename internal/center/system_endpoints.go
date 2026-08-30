package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

type SystemEndpointAliasView struct {
	Kind                string     `json:"kind"`
	Endpoint            string     `json:"endpoint"`
	TransitionID        string     `json:"transitionId"`
	LifecycleState      string     `json:"lifecycleState"`
	RetireAfter         time.Time  `json:"retireAfter"`
	CertificateNotAfter *time.Time `json:"certificateNotAfter,omitempty"`
	LastError           string     `json:"lastError,omitempty"`
}

type systemEndpointAlias struct {
	Kind                string
	Endpoint            string
	CertificateSecretID string
	CertificateNotAfter time.Time
	TransitionID        string
	LifecycleState      string
	RetireAfter         time.Time
	DNSAccountID        string
	DNSZoneID           string
	DNSRecordID         string
	DNSRecordType       string
	DNSRecordContent    string
	LastError           string
}

type systemEndpointAliasQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readSystemEndpointAliases(ctx context.Context, queryer systemEndpointAliasQuerier, kind string) ([]systemEndpointAlias, error) {
	return querySystemEndpointAliases(ctx, queryer, kind, false)
}

func readActiveSystemEndpointAliases(ctx context.Context, queryer systemEndpointAliasQuerier, kind string) ([]systemEndpointAlias, error) {
	return querySystemEndpointAliases(ctx, queryer, kind, true)
}

func querySystemEndpointAliases(ctx context.Context, queryer systemEndpointAliasQuerier, kind string, activeOnly bool) ([]systemEndpointAlias, error) {
	query := `SELECT kind, endpoint, COALESCE(certificate_secret_id, ''), certificate_not_after,
		transition_id, lifecycle_state, retire_after, dns_account_id, dns_zone_id, dns_record_id, dns_record_type, dns_record_content, last_error
		FROM system_endpoint_aliases WHERE kind = ? ORDER BY created_at, endpoint`
	if activeOnly {
		query = `SELECT kind, endpoint, COALESCE(certificate_secret_id, ''), certificate_not_after,
			transition_id, lifecycle_state, retire_after, dns_account_id, dns_zone_id, dns_record_id, dns_record_type, dns_record_content, last_error
			FROM system_endpoint_aliases WHERE kind = ? AND lifecycle_state = 'active' ORDER BY created_at, endpoint`
	}
	rows, err := queryer.QueryContext(ctx, query, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []systemEndpointAlias{}
	for rows.Next() {
		var value systemEndpointAlias
		var notAfter, retireAfter string
		if err := rows.Scan(&value.Kind, &value.Endpoint, &value.CertificateSecretID, &notAfter,
			&value.TransitionID, &value.LifecycleState, &retireAfter, &value.DNSAccountID, &value.DNSZoneID,
			&value.DNSRecordID, &value.DNSRecordType, &value.DNSRecordContent, &value.LastError); err != nil {
			return nil, err
		}
		if notAfter != "" {
			value.CertificateNotAfter, err = time.Parse(time.RFC3339Nano, notAfter)
			if err != nil {
				return nil, errors.New("center: stored system endpoint certificate expiry is invalid")
			}
		}
		if retireAfter != "" {
			value.RetireAfter, err = time.Parse(time.RFC3339Nano, retireAfter)
			if err != nil {
				return nil, errors.New("center: stored system endpoint retirement deadline is invalid")
			}
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ListSystemEndpointAliases(ctx context.Context) ([]SystemEndpointAliasView, error) {
	values := []SystemEndpointAliasView{}
	for _, kind := range []string{"center", "headscale"} {
		aliases, err := readSystemEndpointAliases(ctx, s.db, kind)
		if err != nil {
			return nil, fmt.Errorf("center: list %s endpoint aliases: %w", kind, err)
		}
		for _, alias := range aliases {
			value := SystemEndpointAliasView{
				Kind: alias.Kind, Endpoint: alias.Endpoint, TransitionID: alias.TransitionID,
				LifecycleState: alias.LifecycleState, RetireAfter: alias.RetireAfter, LastError: alias.LastError,
			}
			if !alias.CertificateNotAfter.IsZero() {
				notAfter := alias.CertificateNotAfter
				value.CertificateNotAfter = &notAfter
			}
			values = append(values, value)
		}
	}
	return values, nil
}

func (s *Store) deploymentEndpointAliases(ctx context.Context) ([]deployapi.CenterEndpointAlias, []string, error) {
	centerValues, err := readActiveSystemEndpointAliases(ctx, s.db, "center")
	if err != nil {
		return nil, nil, fmt.Errorf("center: read Center endpoint aliases: %w", err)
	}
	centerAliases := make([]deployapi.CenterEndpointAlias, 0, len(centerValues))
	for _, value := range centerValues {
		hostname, err := gatewayEndpointHostname(value.Endpoint)
		if err != nil {
			return nil, nil, err
		}
		certificate, err := s.loadSystemCenterCertificate(ctx, s.db, value.CertificateSecretID, hostname)
		if err != nil {
			return nil, nil, fmt.Errorf("center: load Center endpoint alias certificate: %w", err)
		}
		centerAliases = append(centerAliases, deployapi.CenterEndpointAlias{
			URL:               value.Endpoint,
			CertificatePEM:    certificate.CertificatePEM,
			CertificateKeyPEM: certificate.PrivateKeyPEM,
		})
	}
	headscaleValues, err := readActiveSystemEndpointAliases(ctx, s.db, "headscale")
	if err != nil {
		return nil, nil, fmt.Errorf("center: read Headscale endpoint aliases: %w", err)
	}
	headscaleAliases := make([]string, 0, len(headscaleValues))
	for _, value := range headscaleValues {
		headscaleAliases = append(headscaleAliases, value.Endpoint)
	}
	return centerAliases, headscaleAliases, nil
}
