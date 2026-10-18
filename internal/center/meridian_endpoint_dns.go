package center

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Glivela's entry form has no manual DNS selector. Use the configured provider
// rather than silently creating an unresolved manual publication in its zone.
func meridianEndpointDNSProvider(ctx context.Context, tx *sql.Tx, hostname string) (string, error) {
	var zone string
	err := tx.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations
		WHERE kind='cloudflare' AND status='configured'`).Scan(&zone)
	if errors.Is(err, sql.ErrNoRows) {
		return "manual", nil
	}
	if err != nil {
		return "", err
	}
	if hostname != zone && !strings.HasSuffix(hostname, "."+zone) {
		return "", errors.New("center: Glivela publication hostname must belong to the configured Cloudflare Zone")
	}
	return "cloudflare", nil
}

func (s *Store) reconcileMeridianEndpointDNS(ctx context.Context, serviceID string) error {
	var id, nodeID, provider string
	var revision int64
	if err := s.db.QueryRowContext(ctx, `SELECT id,entry_node_id,dns_provider,desired_revision
		FROM publications WHERE service_id=? AND kind=? AND status<>'stopped'`, serviceID, publicationShared443).
		Scan(&id, &nodeID, &provider, &revision); err != nil {
		return err
	}
	if provider != "cloudflare" {
		return nil
	}
	// Reuse token renewal, record ownership, conflict checks, compensation and
	// failed-publication recording. An API failure never switches to manual DNS
	// or marks the public entry ready; normal verification can retry it.
	_, err := s.reconcilePublicationDNS(ctx, id, nodeID, provider, revision)
	return err
}
