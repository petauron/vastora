package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/networking"
)

const meridianSubscriptionProtocol = "meridian/subscription"

func meridianCutoverPublicationIssue(ctx context.Context, queryer networkQueryer) (string, error) {
	var status, lastError string
	err := queryer.QueryRowContext(ctx, `SELECT publication.status,publication.last_error
		FROM publications publication
		JOIN services service ON service.id=publication.service_id
		JOIN meridian_cutover cutover ON cutover.legacy_controller_application_id=service.application_id
		WHERE cutover.id=1 AND cutover.state='publish' AND cutover.subscription_authority='meridian'
		AND service.name=? AND service.source='system' AND publication.status IN ('failed','degraded')
		ORDER BY CASE publication.status WHEN 'failed' THEN 0 ELSE 1 END,publication.updated_at,publication.id LIMIT 1`, meridianSubscriptionServiceName).Scan(&status, &lastError)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	lastError = strings.TrimSpace(lastError)
	if lastError == "" {
		lastError = "the public subscription entry did not become ready"
	}
	return fmt.Sprintf("Meridian subscription publication is %s: %s", status, lastError), nil
}

// retryFailedMeridianSubscriptionPublication requeues only the public entry
// that protects the imported subscription snapshot during cutover. It is
// deliberately called from the explicit operator resume action: uncertain or
// still-active gateway work is never replayed automatically.
func (s *Store) retryFailedMeridianSubscriptionPublication(ctx context.Context, tx *sql.Tx, now time.Time) error {
	type failedPublication struct {
		id, owner, gatewayID string
	}
	rows, err := tx.QueryContext(ctx, `SELECT publication.id,publication.ingress_owner,publication.entry_node_id
		FROM publications publication
		JOIN services service ON service.id=publication.service_id
		JOIN meridian_cutover cutover ON cutover.legacy_controller_application_id=service.application_id
		WHERE cutover.id=1 AND cutover.state='publish' AND cutover.subscription_authority='meridian'
		AND service.name=? AND service.source='system' AND publication.status IN ('failed','degraded')
		ORDER BY publication.id`, meridianSubscriptionServiceName)
	if err != nil {
		return err
	}
	values := []failedPublication{}
	for rows.Next() {
		var value failedPublication
		if err := rows.Scan(&value.id, &value.owner, &value.gatewayID); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(values) == 0 {
		return nil
	}

	gateways, tunnels := map[string]bool{}, map[string]bool{}
	for _, value := range values {
		var state string
		switch value.owner {
		case ingressSiteGateway:
			var routeExists int
			if err := tx.QueryRowContext(ctx, `SELECT status,EXISTS(SELECT 1 FROM routes WHERE publication_id=?) FROM gateway_states WHERE gateway_node_id=?`, value.id, value.gatewayID).Scan(&state, &routeExists); err != nil {
				return errors.New("center: failed Meridian subscription gateway state is unavailable")
			}
			if routeExists != 1 {
				return errors.New("center: failed Meridian subscription route is unavailable")
			}
			gateways[value.gatewayID] = true
		case ingressTunnelConnector:
			if err := tx.QueryRowContext(ctx, `SELECT status FROM cloudflare_tunnels WHERE agent_id=?`, value.gatewayID).Scan(&state); err != nil {
				return errors.New("center: failed Meridian subscription Tunnel state is unavailable")
			}
			tunnels[value.gatewayID] = true
		default:
			return errors.New("center: failed Meridian subscription entry has an unsupported ingress owner")
		}
		if state != "ready" && state != "failed" {
			return errors.New("center: Meridian subscription publication is still active; wait for its current task or recover it explicitly")
		}
	}

	stamp := now.UTC().Format(time.RFC3339Nano)
	for _, value := range values {
		if value.owner != ingressSiteGateway {
			continue
		}
		updated, err := tx.ExecContext(ctx, `UPDATE publications SET desired_revision=desired_revision+1,status='pending',last_error='',updated_at=?
			WHERE id=? AND status IN ('failed','degraded')`, stamp, value.id)
		if err != nil {
			return err
		}
		if changed, _ := updated.RowsAffected(); changed != 1 {
			return errors.New("center: Meridian subscription publication changed before retry")
		}
	}
	for gatewayID := range gateways {
		if err := s.queueGatewayState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	for gatewayID := range tunnels {
		if err := s.queueTunnelState(ctx, tx, gatewayID, now); err != nil {
			return err
		}
	}
	return nil
}

// ensureMeridianSubscriptionServiceInTx gives the Center-owned subscription
// endpoint a single durable service identity. The service is attached only to
// the Meridian installation on the co-located Center/Gateway node: the Docker
// alias is intentionally not routable from an arbitrary remote Gateway.
func (s *Store) ensureMeridianSubscriptionServiceInTx(ctx context.Context, tx *sql.Tx, applicationID string, now time.Time) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT service.id,service.application_id
		FROM services service JOIN applications application ON application.id=service.application_id
		WHERE application.app_key=? AND service.name=? AND service.status<>'stopped'
		ORDER BY service.created_at,service.id LIMIT 2`, meridianAppKey, meridianSubscriptionServiceName)
	if err != nil {
		return "", err
	}
	type activeService struct{ id, applicationID string }
	active := []activeService{}
	for rows.Next() {
		var value activeService
		if err := rows.Scan(&value.id, &value.applicationID); err != nil {
			rows.Close()
			return "", err
		}
		active = append(active, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if len(active) > 1 {
		return "", errors.New("center: multiple active Meridian subscription services require cleanup")
	}
	if len(active) == 1 {
		if active[0].applicationID == applicationID {
			return active[0].id, nil
		}
	}
	var cutoverState, authority string
	if err := tx.QueryRowContext(ctx, `SELECT state,subscription_authority FROM meridian_cutover WHERE id=1`).Scan(&cutoverState, &authority); err != nil {
		return "", err
	}
	if authority != "meridian" || cutoverState != "not_required" && cutoverState != "complete" {
		return "", nil
	}

	var nodeID, siteID, appKey, status string
	if err := tx.QueryRowContext(ctx, `SELECT node_id,site_id,app_key,status FROM applications WHERE id=?`, applicationID).Scan(&nodeID, &siteID, &appKey, &status); err != nil {
		return "", err
	}
	if appKey != meridianAppKey || status != "running" {
		return "", nil
	}
	binding, configured, err := readSetupGatewayBinding(ctx, tx)
	if err != nil {
		return "", err
	}
	if !configured {
		return "", nil
	}
	var colocated int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_network_candidates
		WHERE agent_id=? AND address=? AND kind IN (?,?))`, nodeID, binding.BindAddress, networking.KindLAN, networking.KindPublic).Scan(&colocated); err != nil {
		return "", err
	}
	if colocated != 1 {
		return "", nil
	}
	if len(active) == 1 {
		var activePublications int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE service_id=? AND status<>'stopped'`, active[0].id).Scan(&activePublications); err != nil {
			return "", err
		}
		if activePublications != 0 {
			return "", errors.New("center: move or remove the active Meridian subscription entry before changing its host")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status='stopped',updated_at=? WHERE id=? AND status<>'stopped'`, now.UTC().Format(time.RFC3339Nano), active[0].id); err != nil {
			return "", fmt.Errorf("center: retire previous Meridian subscription service: %w", err)
		}
	}

	serviceID := ""
	err = tx.QueryRowContext(ctx, `SELECT id FROM services WHERE application_id=? AND name=?`, applicationID, meridianSubscriptionServiceName).Scan(&serviceID)
	if errors.Is(err, sql.ErrNoRows) {
		serviceID, err = randomToken(18)
		if err != nil {
			return "", err
		}
		stamp := now.UTC().Format(time.RFC3339Nano)
		endpoint := net.JoinHostPort(dockerruntime.CenterAlias, "8080")
		if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,management,status,last_error,created_at,updated_at)
			VALUES(?,?,?,?, 'http',8080,8080,?,'system',?,0,'ready','',?,?)`, serviceID, applicationID, siteID, meridianSubscriptionServiceName, endpoint, meridianSubscriptionProtocol, stamp, stamp); err != nil {
			return "", fmt.Errorf("center: create Meridian subscription service: %w", err)
		}
		return serviceID, nil
	}
	if err != nil {
		return "", err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	endpoint := net.JoinHostPort(dockerruntime.CenterAlias, "8080")
	if _, err := tx.ExecContext(ctx, `UPDATE services SET site_id=?,protocol='http',container_port=8080,host_port=8080,endpoint=?,source='system',app_protocol=?,management=0,status='ready',last_error='',updated_at=? WHERE id=?`, siteID, endpoint, meridianSubscriptionProtocol, stamp, serviceID); err != nil {
		return "", fmt.Errorf("center: restore Meridian subscription service: %w", err)
	}
	return serviceID, nil
}

func (s *Store) ensureMeridianSubscriptionService(ctx context.Context, applicationID string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	serviceID, err := s.ensureMeridianSubscriptionServiceInTx(ctx, tx, applicationID, s.now().UTC())
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return serviceID, nil
}
