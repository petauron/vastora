package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

const meridianHealthDiagnosticsSchema = `
ALTER TABLE meridian_route_grants ADD COLUMN health_diagnostics_json BLOB NOT NULL DEFAULT '{}'
 CHECK(json_valid(health_diagnostics_json) AND json_type(health_diagnostics_json)='object' AND length(health_diagnostics_json)<=16384);
CREATE TABLE meridian_route_health_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 route_id TEXT NOT NULL REFERENCES meridian_route_grants(id) ON DELETE CASCADE,
 recorded_at TEXT NOT NULL,
 fingerprint TEXT NOT NULL CHECK(length(fingerprint)=64),
 diagnostics_json BLOB NOT NULL CHECK(json_valid(diagnostics_json) AND length(diagnostics_json)<=16384)
);
CREATE INDEX meridian_route_health_events_route ON meridian_route_health_events(route_id,id DESC);
CREATE INDEX meridian_route_health_events_recorded ON meridian_route_health_events(recorded_at);
CREATE INDEX meridian_route_health_expiry ON meridian_route_grants(health_expires_unix_ms) WHERE status='ready' AND enabled=1;`

type MeridianHealthLeg struct {
	Role          string    `json:"role"`
	State         string    `json:"state"`
	LinkState     string    `json:"linkState"`
	Stage         string    `json:"stage"`
	Reason        string    `json:"reason"`
	DurationMS    int64     `json:"durationMs"`
	HTTPStatus    int       `json:"httpStatus,omitempty"`
	ExitFamily    int       `json:"exitFamily,omitempty"`
	StartedAt     time.Time `json:"startedAt,omitzero"`
	CheckedAt     time.Time `json:"checkedAt,omitzero"`
	LastHealthyAt time.Time `json:"lastHealthyAt,omitzero"`
	AllowedUntil  time.Time `json:"allowedUntil,omitzero"`
}

type MeridianHealthDiagnostics struct {
	Status     string              `json:"status"`
	Reason     string              `json:"reason"`
	ReceivedAt time.Time           `json:"receivedAt,omitzero"`
	ObservedAt time.Time           `json:"observedAt,omitzero"`
	Legs       []MeridianHealthLeg `json:"legs"`
}

type MeridianRouteHealthView struct {
	Current   MeridianHealthDiagnostics   `json:"current"`
	History   []MeridianHealthDiagnostics `json:"history"`
	CheckedAt time.Time                   `json:"checkedAt"`
}

// Never persist arbitrary Agent strings, URLs, response bodies, peer keys or
// exit IPs. The only remote text retained is drawn from this fixed vocabulary.
func healthDiagnosticCode(value string) string {
	switch value {
	case "healthy", "blocked", "direct", "unknown", "derp", "peer_relay", "disconnected",
		"private_link_before", "private_link_after", "business_check", "socks_connect", "socks_handshake",
		"tls_handshake", "http_response", "http_body", "exit_address", "gate", "verification", "complete",
		"check_failed", "timeout", "canceled", "connection_refused", "connection_reset", "connection_closed",
		"certificate_verification", "unexpected_http_status", "direct_and_business_ready", "renewal_failed",
		"evidence_invalid_or_expired", "fresh_disco_direct_response", "invalid_peer_identity", "local_status_unavailable",
		"private_network_disconnected", "local_target_is_not_remote", "peer_identity_mismatch", "peer_disconnected",
		"probe_failed", "probe_expired", "peer_identity_changed", "probe_target_mismatch", "peer_relay_detected",
		"derp_detected", "direct_endpoint_unavailable", "monitor_starting", "monitor_stopped":
		return value
	default:
		return "unreported"
	}
}

func routeHealthDiagnostic(result meridianruntime.Result, primary, secondary, status string, now time.Time) MeridianHealthDiagnostics {
	d := MeridianHealthDiagnostics{Status: status, Reason: "verified", ReceivedAt: now.UTC(), ObservedAt: now.UTC(), Legs: []MeridianHealthLeg{}}
	if status != "ready" {
		d.Reason = "transport_unverified"
	}
	for i, id := range []string{primary, secondary} {
		if id == "" {
			continue
		}
		role := "primary"
		if i == 1 {
			role = "ipv4"
		}
		for _, p := range result.Peers {
			if p.EgressID != id {
				continue
			}
			s := p.Status
			leg := MeridianHealthLeg{Role: role, State: healthDiagnosticCode(s.State), LinkState: healthDiagnosticCode(s.LinkState), Stage: healthDiagnosticCode(s.Stage), Reason: healthDiagnosticCode(s.Reason), DurationMS: max(0, min(s.DurationMS, 60000)), StartedAt: s.StartedAt, CheckedAt: s.CheckedAt, LastHealthyAt: s.LastHealthyAt, AllowedUntil: s.AllowedUntil}
			if s.HTTPStatus >= 100 && s.HTTPStatus <= 599 {
				leg.HTTPStatus = s.HTTPStatus
			}
			if ip, err := netip.ParseAddr(s.ExitIP); err == nil && landing.PublicIP(ip) {
				if ip.Is4() {
					leg.ExitFamily = 4
				} else {
					leg.ExitFamily = 6
				}
			}
			// Additional diagnostic clocks do not influence authorization. Reject
			// future/contradictory clocks instead of presenting invented timing.
			if leg.StartedAt.After(leg.CheckedAt) || leg.StartedAt.After(now) {
				leg.StartedAt = time.Time{}
			}
			if leg.LastHealthyAt.After(now) {
				leg.LastHealthyAt = time.Time{}
			}
			if s.State == "healthy" && !s.CheckedAt.After(now) && s.AllowedUntil.After(now) {
				leg.LastHealthyAt = s.CheckedAt
			}
			d.Legs = append(d.Legs, leg)
		}
	}
	return d
}

func healthFingerprint(d MeridianHealthDiagnostics) string {
	// Durations and clocks change every probe; only semantic changes are events.
	d.ReceivedAt = time.Time{}
	d.ObservedAt = time.Time{}
	d.Legs = append([]MeridianHealthLeg(nil), d.Legs...)
	for i := range d.Legs {
		d.Legs[i].DurationMS = 0
		d.Legs[i].StartedAt = time.Time{}
		d.Legs[i].CheckedAt = time.Time{}
		d.Legs[i].LastHealthyAt = time.Time{}
		d.Legs[i].AllowedUntil = time.Time{}
	}
	raw, _ := json.Marshal(d)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func recordRouteHealthDiagnostics(ctx context.Context, tx *sql.Tx, id string, d MeridianHealthDiagnostics) error {
	var previousRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT health_diagnostics_json FROM meridian_route_grants WHERE id=?`, id).Scan(&previousRaw); err != nil {
		return err
	}
	var previous MeridianHealthDiagnostics
	if err := json.Unmarshal(previousRaw, &previous); err != nil {
		return err
	}
	for i := range d.Legs {
		for _, old := range previous.Legs {
			if old.Role == d.Legs[i].Role && old.LastHealthyAt.After(d.Legs[i].LastHealthyAt) {
				d.Legs[i].LastHealthyAt = old.LastHealthyAt
			}
		}
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE meridian_route_grants SET health_diagnostics_json=? WHERE id=?`, raw, id); err != nil {
		return err
	}
	if healthFingerprint(previous) == healthFingerprint(d) {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO meridian_route_health_events(route_id,recorded_at,fingerprint,diagnostics_json) VALUES(?,?,?,?)`, id, d.ObservedAt.Format(time.RFC3339Nano), healthFingerprint(d), raw); err != nil {
		return err
	}
	// Hard caps hold even during a flapping incident between retention passes.
	if _, err = tx.ExecContext(ctx, `DELETE FROM meridian_route_health_events WHERE id IN (SELECT id FROM meridian_route_health_events WHERE route_id=? ORDER BY id DESC LIMIT -1 OFFSET 200)`, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM meridian_route_health_events WHERE id IN (SELECT id FROM meridian_route_health_events ORDER BY id DESC LIMIT -1 OFFSET 10000)`)
	return err
}

func (s *Store) MeridianRouteHealth(ctx context.Context, id string) (MeridianRouteHealthView, error) {
	view := MeridianRouteHealthView{History: []MeridianHealthDiagnostics{}, CheckedAt: s.now().UTC()}
	var raw []byte
	var enabled int
	var healthy int
	var desired, applied int64
	var status string
	var deadline int64
	if err := s.db.QueryRowContext(ctx, `SELECT health_diagnostics_json,enabled,status,health_expires_unix_ms,runtime_healthy,desired_revision,applied_revision FROM meridian_route_grants WHERE id=? AND status<>'revoked'`, id).Scan(&raw, &enabled, &status, &deadline, &healthy, &desired, &applied); err != nil {
		return view, err
	}
	if err := json.Unmarshal(raw, &view.Current); err != nil {
		return view, err
	}
	if view.Current.Status == "" {
		view.Current.Status = "unreported"
		view.Current.Reason = "unreported"
		view.Current.Legs = []MeridianHealthLeg{}
	}
	if enabled == 0 {
		view.Current.Status = "disabled"
		view.Current.Reason = "disabled"
	} else if status == "ready" && deadline <= view.CheckedAt.UnixMilli() {
		if view.Current.Reason != "evidence_expired" {
			// A read can observe expiry before the maintenance worker records
			// it. Keep the actual heartbeat receipt clock unchanged.
			view.Current.ObservedAt = view.CheckedAt
		}
		view.Current.Status = "blocked"
		view.Current.Reason = "evidence_expired"
	}
	if enabled != 0 && status != "ready" && view.Current.Status == "ready" {
		view.Current.Status = status
		view.Current.Reason = "configuration_pending"
	}
	if enabled != 0 && status == "ready" && (healthy != 1 || desired != applied) {
		view.Current.Status, view.Current.Reason = "blocked", "configuration_pending"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT diagnostics_json FROM meridian_route_health_events WHERE route_id=? AND recorded_at>=? ORDER BY id DESC LIMIT 20`, id, view.CheckedAt.Add(-7*24*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return view, err
		}
		var event MeridianHealthDiagnostics
		if err := json.Unmarshal(data, &event); err != nil {
			return view, err
		}
		view.History = append(view.History, event)
	}
	return view, rows.Err()
}

func (s *Store) maintainRouteHealthDiagnostics(ctx context.Context, prune bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	rows, err := tx.QueryContext(ctx, `SELECT id,health_diagnostics_json FROM meridian_route_grants WHERE status='ready' AND enabled=1 AND health_expires_unix_ms<=? AND json_extract(health_diagnostics_json,'$.status')='ready' LIMIT 200`, now.UnixMilli())
	if err != nil {
		return err
	}
	type expired struct {
		id string
		d  MeridianHealthDiagnostics
	}
	values := []expired{}
	for rows.Next() {
		var value expired
		var raw []byte
		if err := rows.Scan(&value.id, &raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(raw, &value.d); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, value := range values {
		value.d.Status, value.d.Reason, value.d.ObservedAt = "blocked", "evidence_expired", now
		if err := recordRouteHealthDiagnostics(ctx, tx, value.id, value.d); err != nil {
			return err
		}
	}
	if prune {
		if _, err := tx.ExecContext(ctx, `DELETE FROM meridian_route_health_events WHERE recorded_at<?`, now.Add(-7*24*time.Hour).Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RunRouteHealthDiagnostics(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		prune := s.now().Sub(lastPrune) >= time.Hour
		if err := s.maintainRouteHealthDiagnostics(ctx, prune); err != nil {
			if !errors.Is(err, context.Canceled) && report != nil {
				report(err)
			}
		} else if prune {
			lastPrune = s.now()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
