package center

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

const (
	realitySecurityCheckSafe         = "safe"
	realitySecurityCheckAffected     = "affected"
	realitySecurityCheckInconclusive = "inconclusive"

	realitySecurityCheckRemote   = "remote"
	realitySecurityCheckSameHost = "same_host"
)

type RealitySecurityCheckItem struct {
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type RealitySecurityCheckView struct {
	Status    string                     `json:"status"`
	Scope     string                     `json:"scope"`
	Checks    []RealitySecurityCheckItem `json:"checks"`
	CheckedAt time.Time                  `json:"checkedAt"`
}

type realitySecurityCheckTarget struct {
	publicationID       string
	publicationRevision int64
	guardRevision       int64
	serviceID           string
	entryNodeID         string
	publicAddress       string
	expectedSNI         string
	targetHost          string
	targetIP            string
	scope               string
}

type realitySecurityProbe struct {
	kind       string
	serverName string
	expected   bool
	noSNI      bool
}

// RunRealitySecurityCheck performs an administrator-initiated behavior check
// from Center. Its public target, port and expected SNI all come from current
// managed state; callers cannot turn this endpoint into an arbitrary scanner.
func (s *Store) RunRealitySecurityCheck(ctx context.Context, publicationID, adminID string) (RealitySecurityCheckView, error) {
	publicationID = strings.TrimSpace(publicationID)
	adminID = strings.TrimSpace(adminID)
	if publicationID == "" || adminID == "" {
		return RealitySecurityCheckView{}, errors.New("center: publication and administrator are required")
	}
	target, err := s.realitySecurityCheckTarget(ctx, publicationID)
	if err != nil {
		return RealitySecurityCheckView{}, err
	}
	if !s.realitySecurityCheckMu.TryLock() {
		return RealitySecurityCheckView{}, errors.New("center: another REALITY security check is already running")
	}
	defer s.realitySecurityCheckMu.Unlock()

	randomLabel, err := randomDNSLabel(12)
	if err != nil {
		return RealitySecurityCheckView{}, err
	}
	probes := []realitySecurityProbe{
		{kind: "expected_fallback", serverName: target.expectedSNI, expected: true},
		{kind: "openai_sni", serverName: "api.openai.com"},
		{kind: "cloudflare_sni", serverName: "www.cloudflare.com"},
		{kind: "random_sni", serverName: randomLabel + ".invalid"},
		{kind: "no_sni", noSNI: true},
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	checks := make([]RealitySecurityCheckItem, len(probes))
	var wait sync.WaitGroup
	for index := range probes {
		wait.Add(1)
		go func() {
			defer wait.Done()
			probe := probes[index]
			serverName := probe.serverName
			if probe.noSNI {
				serverName = ""
			}
			probeErr := s.dialRealitySecurityProbe(probeCtx, target.publicAddress, serverName)
			checks[index] = classifyRealitySecurityProbe(probe, probeErr)
			if checks[index].Status == "failed" {
				cancel()
			}
		}()
	}
	wait.Wait()
	if err := ctx.Err(); err != nil {
		return RealitySecurityCheckView{}, err
	}

	status := aggregateRealitySecurityCheckStatus(checks)
	result := RealitySecurityCheckView{Status: status, Scope: target.scope, Checks: checks, CheckedAt: s.now().UTC()}
	if err := s.storeRealitySecurityCheck(ctx, target, adminID, result); err != nil {
		return RealitySecurityCheckView{}, err
	}
	return result, nil
}

func (s *Store) realitySecurityCheckTarget(ctx context.Context, publicationID string) (realitySecurityCheckTarget, error) {
	publication, err := s.Publication(ctx, publicationID)
	if err != nil {
		return realitySecurityCheckTarget{}, err
	}
	if publication.Kind != publicationShared443 || publication.Ingress.Owner != ingressApplicationNode {
		return realitySecurityCheckTarget{}, errors.New("center: security check requires a node-direct shared 443 publication")
	}
	if publication.Status != "ready" || publication.ActionRequired {
		return realitySecurityCheckTarget{}, errors.New("center: security check requires a ready REALITY publication")
	}

	var appKey, appProtocol, appNodeID, serviceStatus string
	var targetHost, targetIP, serverName, guardStatus string
	var guardRevision int64
	if err := s.db.QueryRowContext(ctx, `SELECT application.app_key, service.app_protocol, application.node_id, service.status,
		guard.target_host, guard.target_ip, guard.server_name, guard.revision, guard.status
		FROM publications publication
		JOIN services service ON service.id = publication.service_id
		JOIN applications application ON application.id = service.application_id
		JOIN three_x_ui_reality_guards guard ON guard.service_id = service.id
		WHERE publication.id = ?`, publicationID).Scan(
		&appKey, &appProtocol, &appNodeID, &serviceStatus,
		&targetHost, &targetIP, &serverName, &guardRevision, &guardStatus,
	); errors.Is(err, sql.ErrNoRows) {
		return realitySecurityCheckTarget{}, errors.New("center: managed REALITY guard was not found")
	} else if err != nil {
		return realitySecurityCheckTarget{}, err
	}
	if appKey != threeXUIAppKey || appProtocol != "vless/tcp/reality" || serviceStatus != "ready" || appNodeID != publication.EntryNodeID {
		return realitySecurityCheckTarget{}, errors.New("center: security check requires a ready managed 3x-ui REALITY service on its owning node")
	}
	targetHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(targetHost), "."))
	serverName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(serverName), "."))
	if guardStatus != "ready" || guardRevision < 1 || !validRealityTargetHostname(targetHost) || !validRealityTargetHostname(serverName) || serverName != publication.SNIHostname || !isPublicPublicationVerificationIP(net.ParseIP(targetIP)) {
		return realitySecurityCheckTarget{}, errors.New("center: managed REALITY fallback guard is not ready for a security check")
	}
	profile, err := networkProfile(ctx, s.db, publication.EntryNodeID)
	if err != nil {
		return realitySecurityCheckTarget{}, err
	}
	if profile == nil || !profile.DirectPublic || !networkKindEnabled(profile.EnabledKinds, networking.KindPublic) {
		return realitySecurityCheckTarget{}, errors.New("center: REALITY node does not have an approved public entry")
	}
	publicIP := net.ParseIP(strings.TrimSpace(profile.PublicAddress))
	if publicIP == nil || publicIP.To4() == nil || !isPublicPublicationVerificationIP(publicIP) || publication.DNSRecord == nil || !publicIP.Equal(net.ParseIP(publication.DNSRecord.Value)) {
		return realitySecurityCheckTarget{}, errors.New("center: REALITY node public address is not ready for a security check")
	}
	candidates, err := networkCandidates(ctx, s.db, publication.EntryNodeID)
	if err != nil {
		return realitySecurityCheckTarget{}, err
	}
	coLocated, err := s.networkCandidatesAreCoLocated(candidates)
	if err != nil {
		return realitySecurityCheckTarget{}, err
	}
	scope := realitySecurityCheckRemote
	if coLocated {
		scope = realitySecurityCheckSameHost
	}
	return realitySecurityCheckTarget{
		publicationID:       publication.ID,
		publicationRevision: publication.DesiredRevision,
		guardRevision:       guardRevision,
		serviceID:           publication.ServiceID,
		entryNodeID:         publication.EntryNodeID,
		publicAddress:       publicIP.String(),
		expectedSNI:         serverName,
		targetHost:          targetHost,
		targetIP:            strings.TrimSpace(targetIP),
		scope:               scope,
	}, nil
}

func networkKindEnabled(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func defaultRealitySecurityProbe(ctx context.Context, address, serverName string) error {
	connection, err := (&tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 5 * time.Second},
		Config: &tls.Config{
			MinVersion: tls.VersionTLS13,
			ServerName: serverName,
			NextProtos: []string{"h2", "http/1.1"},
		},
	}).DialContext(ctx, "tcp", net.JoinHostPort(address, "443"))
	if err != nil {
		return err
	}
	_ = connection.Close()
	return nil
}

func classifyRealitySecurityProbe(probe realitySecurityProbe, err error) RealitySecurityCheckItem {
	item := RealitySecurityCheckItem{Kind: probe.kind}
	if err == nil {
		switch {
		case probe.expected:
			item.Status, item.Reason = "passed", "expected_fallback_verified"
		case probe.noSNI:
			item.Status, item.Reason = "passed", "local_tls_termination"
		default:
			item.Status, item.Reason = "failed", "unauthorized_destination_reached"
		}
		return item
	}
	if realitySecurityProbeTimedOut(err) {
		item.Status, item.Reason = "inconclusive", "probe_timeout"
		return item
	}
	if errors.Is(err, context.Canceled) {
		item.Status, item.Reason = "inconclusive", "probe_interrupted"
		return item
	}
	if probe.expected {
		item.Status, item.Reason = "inconclusive", "expected_fallback_unavailable"
	} else {
		item.Status, item.Reason = "passed", "unauthorized_destination_rejected"
	}
	return item
}

func realitySecurityProbeTimedOut(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

func (s *Store) storeRealitySecurityCheck(ctx context.Context, target realitySecurityCheckTarget, adminID string, result RealitySecurityCheckView) error {
	if !validRealitySecurityCheck(result) {
		return errors.New("center: REALITY security check result is invalid")
	}
	checksJSON, err := json.Marshal(result.Checks)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var publicationRevision, guardRevision int64
	var publicationStatus, publicationKind, ingressOwner, entryNodeID, publicAddress, serviceStatus, guardStatus, serverName, targetHost, targetIP string
	var actionRequired int
	if err := tx.QueryRowContext(ctx, `SELECT publication.desired_revision, publication.status, publication.kind, publication.ingress_owner,
		publication.entry_node_id, publication.action_required, COALESCE(profile.public_address, ''), service.status,
		guard.revision, guard.status, guard.server_name, guard.target_host, guard.target_ip
		FROM publications publication
		JOIN services service ON service.id = publication.service_id
		JOIN three_x_ui_reality_guards guard ON guard.service_id = service.id
		LEFT JOIN agent_network_profiles profile ON profile.agent_id = publication.entry_node_id
		WHERE publication.id = ? AND publication.service_id = ?`, target.publicationID, target.serviceID).Scan(
		&publicationRevision, &publicationStatus, &publicationKind, &ingressOwner,
		&entryNodeID, &actionRequired, &publicAddress, &serviceStatus, &guardRevision, &guardStatus, &serverName, &targetHost, &targetIP,
	); err != nil {
		return err
	}
	if publicationRevision != target.publicationRevision || guardRevision != target.guardRevision || publicationStatus != "ready" || actionRequired != 0 || publicationKind != publicationShared443 || ingressOwner != ingressApplicationNode || entryNodeID != target.entryNodeID || publicAddress != target.publicAddress || serviceStatus != "ready" || guardStatus != "ready" || serverName != target.expectedSNI || targetHost != target.targetHost || targetIP != target.targetIP {
		return errors.New("center: REALITY publication changed during the security check; run it again")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_security_checks(
		publication_id, publication_revision, guard_revision, status, scope, checks_json, requested_by, checked_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(publication_id) DO UPDATE SET
		publication_revision = excluded.publication_revision,
		guard_revision = excluded.guard_revision,
		status = excluded.status,
		scope = excluded.scope,
		checks_json = excluded.checks_json,
		requested_by = excluded.requested_by,
		checked_at = excluded.checked_at`,
		target.publicationID, target.publicationRevision, target.guardRevision, result.Status, result.Scope, checksJSON, adminID, result.CheckedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	event := "failed"
	if result.Status == realitySecurityCheckSafe {
		event = "succeeded"
	}
	message := fmt.Sprintf("REALITY security check %s (%s); requested by administrator %s", result.Status, result.Scope, adminID)
	if err := s.recordTaskEvent(ctx, tx, "reality-security-check:"+target.publicationID, target.entryNodeID, "security.reality.check", target.publicationRevision, event, message); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) realitySecurityCheck(ctx context.Context, publicationID string) (*RealitySecurityCheckView, error) {
	var status, scope, checkedAt string
	var checksJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT security.status, security.scope, security.checks_json, security.checked_at
		FROM reality_security_checks security
		JOIN publications publication ON publication.id = security.publication_id
		JOIN three_x_ui_reality_guards guard ON guard.service_id = publication.service_id
		WHERE security.publication_id = ?
		AND security.publication_revision = publication.desired_revision
		AND security.guard_revision = guard.revision
		AND publication.status = 'ready' AND publication.action_required = 0
		AND guard.status = 'ready'`, publicationID).Scan(&status, &scope, &checksJSON, &checkedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeRealitySecurityCheck(status, scope, checksJSON, checkedAt)
}

func (s *Store) realitySecurityChecks(ctx context.Context) (map[string]*RealitySecurityCheckView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT security.publication_id, security.status, security.scope, security.checks_json, security.checked_at
		FROM reality_security_checks security
		JOIN publications publication ON publication.id = security.publication_id
		JOIN three_x_ui_reality_guards guard ON guard.service_id = publication.service_id
		WHERE security.publication_revision = publication.desired_revision
		AND security.guard_revision = guard.revision
		AND publication.status = 'ready' AND publication.action_required = 0
		AND guard.status = 'ready'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := map[string]*RealitySecurityCheckView{}
	for rows.Next() {
		var publicationID, status, scope, checkedAt string
		var checksJSON []byte
		if err := rows.Scan(&publicationID, &status, &scope, &checksJSON, &checkedAt); err != nil {
			return nil, err
		}
		value, err := decodeRealitySecurityCheck(status, scope, checksJSON, checkedAt)
		if err != nil {
			return nil, err
		}
		values[publicationID] = value
	}
	return values, rows.Err()
}

func decodeRealitySecurityCheck(status, scope string, checksJSON []byte, checkedAt string) (*RealitySecurityCheckView, error) {
	value := &RealitySecurityCheckView{Status: status, Scope: scope}
	if json.Unmarshal(checksJSON, &value.Checks) != nil || !validRealitySecurityCheck(*value) {
		return nil, errors.New("center: stored REALITY security check is invalid")
	}
	var err error
	value.CheckedAt, err = time.Parse(time.RFC3339Nano, checkedAt)
	if err != nil {
		return nil, errors.New("center: stored REALITY security check time is invalid")
	}
	return value, nil
}

func aggregateRealitySecurityCheckStatus(checks []RealitySecurityCheckItem) string {
	status := realitySecurityCheckSafe
	for _, check := range checks {
		if check.Status == "failed" {
			return realitySecurityCheckAffected
		}
		if check.Status == "inconclusive" {
			status = realitySecurityCheckInconclusive
		}
	}
	return status
}

func validRealitySecurityCheck(value RealitySecurityCheckView) bool {
	if value.Scope != realitySecurityCheckRemote && value.Scope != realitySecurityCheckSameHost {
		return false
	}
	expectedKinds := [...]string{"expected_fallback", "openai_sni", "cloudflare_sni", "random_sni", "no_sni"}
	if len(value.Checks) != len(expectedKinds) || value.Status != aggregateRealitySecurityCheckStatus(value.Checks) {
		return false
	}
	for index, check := range value.Checks {
		if check.Kind != expectedKinds[index] || !validRealitySecurityCheckItem(check) {
			return false
		}
	}
	return true
}

func validRealitySecurityCheckItem(value RealitySecurityCheckItem) bool {
	if value.Status == "inconclusive" {
		return value.Reason == "probe_timeout" || value.Reason == "probe_interrupted" || value.Kind == "expected_fallback" && value.Reason == "expected_fallback_unavailable"
	}
	switch value.Kind {
	case "expected_fallback":
		return value.Status == "passed" && value.Reason == "expected_fallback_verified"
	case "openai_sni", "cloudflare_sni", "random_sni":
		return value.Status == "passed" && value.Reason == "unauthorized_destination_rejected" || value.Status == "failed" && value.Reason == "unauthorized_destination_reached"
	case "no_sni":
		return value.Status == "passed" && (value.Reason == "unauthorized_destination_rejected" || value.Reason == "local_tls_termination")
	default:
		return false
	}
}
