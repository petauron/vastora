package center

import (
	"context"
	"encoding/json"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMeridianHealthDiagnosticsChangesOnlyAndRecovery(t *testing.T) {
	s, projection, _, id := openMeridianHealthCompletionFixture(t)
	ctx := context.Background()
	now := s.now()
	s.now = func() time.Time { return now }
	write := func(healthy bool) {
		t.Helper()
		result := meridianHealthResult(projection, now, healthy)
		for i := range result.Peers {
			p := &result.Peers[i].Status
			p.StartedAt = now.Add(-time.Millisecond)
			p.DurationMS = 1
			if healthy {
				p.Stage = "startup"
				p.Reason = "startup_ready"
				p.HTTPStatus = 200
			} else {
				p.Stage = "tls_handshake"
				p.Reason = "timeout"
				p.LinkState = "direct"
			}
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := recordMeridianRouteHealth(ctx, tx, sharedSnapshotEndpointID, projection, result, now); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	write(true)
	first := now
	for range 5 {
		now = now.Add(time.Second)
		write(true)
	}
	view, err := s.MeridianRouteHealth(ctx, id)
	if err != nil || len(view.History) != 1 || view.Current.Legs[0].LastHealthyAt.Before(first) {
		t.Fatalf("normal heartbeats appended history: %#v %v", view, err)
	}
	now = now.Add(time.Second)
	write(false)
	write(false)
	view, err = s.MeridianRouteHealth(ctx, id)
	if err != nil || len(view.History) != 2 || view.Current.Legs[0].Stage != "tls_handshake" || view.Current.Legs[0].Reason != "timeout" || view.Current.Legs[0].LastHealthyAt.IsZero() {
		t.Fatalf("failure lost diagnosis or last success: %#v %v", view, err)
	}
	now = now.Add(time.Second)
	write(true)
	view, err = s.MeridianRouteHealth(ctx, id)
	if err != nil || len(view.History) != 3 || view.Current.Status != "ready" {
		t.Fatalf("recovery was not recorded: %#v %v", view, err)
	}
}

func TestMeridianRouteHealthReadRequiresSessionAndDoesNotLaunchTasks(t *testing.T) {
	s, _, _, id := openMeridianHealthCompletionFixture(t)
	ctx := context.Background()
	session, _, err := s.CreateFirstAdmin(ctx, "admin", "example-password-for-test-only")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(s, "", false).Handler()
	var before int
	if err := s.db.QueryRow(`SELECT count(*) FROM application_commands`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, authenticated := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/meridian/routes/"+id+"/health", nil)
		if authenticated {
			request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("retired business API served: %d", response.Code)
		}
	}
	var after int
	if err := s.db.QueryRow(`SELECT count(*) FROM application_commands`).Scan(&after); err != nil || after != before {
		t.Fatal("reading diagnosis launched a task")
	}
}

func TestMeridianHealthDiagnosticsSanitizeAndFingerprintDoNotMutate(t *testing.T) {
	now := time.Now().UTC()
	r := meridianruntime.Result{Peers: []meridianruntime.PeerObservation{{EgressID: "exit-example", Identity: landing.PeerIdentity{ID: "secret-peer", PublicKey: "secret-key"}, Status: landing.MonitorStatus{State: "blocked", Stage: "https://secret.example/password", Reason: "secret-token", ExitIP: "1.1.1.1", DurationMS: 1 << 40, CheckedAt: now, StartedAt: now.Add(time.Second)}}}}
	d := routeHealthDiagnostic(r, "exit-example", "", "blocked", now)
	fingerprint := healthFingerprint(d)
	if d.Legs[0].CheckedAt != now || d.Legs[0].DurationMS != 60000 || !d.Legs[0].StartedAt.IsZero() {
		t.Fatal("fingerprint mutated evidence or clocks were invented")
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "1.1.1.1") {
		t.Fatal("diagnosis exposed remote strings, identity or address")
	}
	d.Legs[0].DurationMS = 5
	d.ReceivedAt = now.Add(time.Second)
	if healthFingerprint(d) != fingerprint {
		t.Fatal("probe duration or receipt clock causes event flooding")
	}
}

func TestMeridianStartupDiagnosticsDoNotExpireOrInventReceipts(t *testing.T) {
	s, projection, _, id := openMeridianHealthCompletionFixture(t)
	ctx := context.Background()
	now := s.now()
	s.now = func() time.Time { return now }
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := meridianHealthResult(projection, now, true)
	if err := recordMeridianRouteHealth(ctx, tx, sharedSnapshotEndpointID, projection, result, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	received := now
	now = now.Add(20 * time.Second)
	read, err := s.MeridianRouteHealth(ctx, id)
	if err != nil || read.Current.Status != "ready" || !read.Current.ReceivedAt.Equal(received) || !read.Current.ObservedAt.Equal(received) || len(read.History) != 1 {
		t.Fatalf("read-time expiry invented a receipt or wrote history: %#v %v", read, err)
	}
	if err := s.maintainRouteHealthDiagnostics(ctx, false); err != nil {
		t.Fatal(err)
	}
	view, err := s.MeridianRouteHealth(ctx, id)
	if err != nil || view.Current.Status != "ready" || !view.Current.ReceivedAt.Equal(received) || !view.Current.ObservedAt.Equal(received) || len(view.History) != 1 {
		t.Fatalf("expiry overwrote receipt clock or omitted event: %#v %v", view, err)
	}
	var state string
	var healthy int
	if err := s.db.QueryRow(`SELECT status,runtime_healthy FROM meridian_route_grants WHERE id=?`, id).Scan(&state, &healthy); err != nil || state != "ready" || healthy != 1 {
		t.Fatal("diagnostic worker modified transport authority")
	}
	if err := s.maintainRouteHealthDiagnostics(ctx, false); err != nil {
		t.Fatal(err)
	}
	view, _ = s.MeridianRouteHealth(ctx, id)
	if len(view.History) != 1 {
		t.Fatal("expiry was recorded repeatedly")
	}
}

func TestMeridianHealthDiagnosticsRetentionAndCascade(t *testing.T) {
	s, _, _, id := openMeridianHealthCompletionFixture(t)
	ctx := context.Background()
	now := s.now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := range 205 {
		d := MeridianHealthDiagnostics{Status: "blocked", Reason: "transport_unverified", ReceivedAt: now, ObservedAt: now, Legs: []MeridianHealthLeg{{Role: "primary", Stage: "http_response", Reason: "unexpected_http_status", HTTPStatus: 400 + i%2}}}
		if err := recordRouteHealthDiagnostics(ctx, tx, id, d); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM meridian_route_health_events WHERE route_id=?`, id).Scan(&count); err != nil || count != 200 {
		t.Fatalf("unbounded route history: %d %v", count, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM meridian_route_grants WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM meridian_route_health_events WHERE route_id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted route retained orphan history")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return now.Add(8 * 24 * time.Hour) }
	if err := s.maintainRouteHealthDiagnostics(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM meridian_route_health_events WHERE route_id=?`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("history older than seven days was retained")
	}
}
