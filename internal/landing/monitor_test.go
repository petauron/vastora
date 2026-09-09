package landing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMonitorDoesNotRepeatCallerCutoverAndStillStopsOnExit(t *testing.T) {
	gate := fixtureGate(t)
	document, err := json.Marshal(fixtureNFT(t, gate))
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	gate.run = func(_ context.Context, input []byte, _ ...string) ([]byte, error) {
		if input != nil {
			writes++
			return nil, nil
		}
		return document, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stops, reports := 0, 0
	monitor := Monitor{Gate: gate, Links: &LinkChecker{HTTPClient: &http.Client{Transport: latencyTransport{server.URL}}},
		CheckBusiness: func(context.Context, PeerIdentity, uint64) (BusinessResult, error) {
			t.Fatal("business check ran without a direct peer")
			return BusinessResult{}, nil
		},
		StopConnections: func(context.Context) error { stops++; return nil },
		Report: func(status MonitorStatus) {
			reports++
			if stops != 0 || status.State != "blocked" || writes == 0 {
				t.Fatalf("repeated initial cutover or opened gate: stops=%d writes=%d status=%+v", stops, writes, status)
			}
			cancel()
		},
	}
	if err := monitor.Run(ctx); err != context.Canceled || stops != 1 || reports != 1 {
		t.Fatalf("shutdown cleanup changed: stops=%d reports=%d err=%v", stops, reports, err)
	}
}

func TestLandingLeaseRequiresCurrentDirectAndActualBusinessProof(t *testing.T) {
	gate := fixtureGate(t)
	now := time.Now()
	link := func(start, end time.Time) LinkResult {
		return LinkResult{State: "direct", Reason: "fresh_disco_direct_response", StartedAt: start, CheckedAt: end}
	}
	before := link(now.Add(-4*time.Second), now.Add(-3*time.Second))
	after := link(now.Add(-time.Second), now)
	health := BusinessResult{Peer: gate.peer, Revision: gate.revision, TCP: true, UDP: true, UDPRelay: "100.64.0.8:1081", ExitIPv4: "1.1.1.1", StartedAt: now.Add(-3 * time.Second), CheckedAt: now.Add(-time.Second)}
	for _, test := range []struct {
		name   string
		change func(*LinkResult, *BusinessResult, *LinkResult)
	}{
		{"DERP", func(b *LinkResult, _ *BusinessResult, _ *LinkResult) { b.State = "derp" }},
		{"relay after business", func(_ *LinkResult, _ *BusinessResult, a *LinkResult) { a.State = "peer_relay" }},
		{"unknown", func(_ *LinkResult, _ *BusinessResult, a *LinkResult) { a.State = "unknown" }},
		{"ping only", func(_ *LinkResult, h *BusinessResult, _ *LinkResult) { h.TCP, h.UDP = false, false }},
		{"TCP only", func(_ *LinkResult, h *BusinessResult, _ *LinkResult) { h.UDP = false }},
		{"wrong UDP relay", func(_ *LinkResult, h *BusinessResult, _ *LinkResult) { h.UDPRelay = "100.64.0.8:9999" }},
		{"stale revision", func(_ *LinkResult, h *BusinessResult, _ *LinkResult) { h.Revision-- }},
		{"replaced peer", func(_ *LinkResult, h *BusinessResult, _ *LinkResult) { h.Peer.PublicKey = "nodekey:new" }},
		{"no exit evidence", func(_ *LinkResult, h *BusinessResult, _ *LinkResult) { h.ExitIPv4 = "" }},
		{"private exit", func(_ *LinkResult, h *BusinessResult, _ *LinkResult) { h.ExitIPv4 = "100.64.0.8" }},
		{"future proof", func(_ *LinkResult, _ *BusinessResult, a *LinkResult) { a.CheckedAt = now.Add(time.Second) }},
		{"expired proof", func(b *LinkResult, h *BusinessResult, a *LinkResult) {
			b.StartedAt = b.StartedAt.Add(-time.Minute)
			b.CheckedAt = b.CheckedAt.Add(-time.Minute)
			h.StartedAt = h.StartedAt.Add(-time.Minute)
			h.CheckedAt = h.CheckedAt.Add(-time.Minute)
			a.StartedAt = a.StartedAt.Add(-time.Minute)
			a.CheckedAt = a.CheckedAt.Add(-time.Minute)
		}},
		{"business before link", func(_ *LinkResult, h *BusinessResult, _ *LinkResult) { h.StartedAt = now.Add(-10 * time.Second) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, h, a := before, health, after
			if _, ok := leaseDeadline(gate, b, h, a, now); !ok {
				t.Fatal("valid fixture rejected")
			}
			test.change(&b, &h, &a)
			if _, ok := leaseDeadline(gate, b, h, a, now); ok {
				t.Fatal("invalid health evidence allowed traffic")
			}
		})
	}
	until, ok := leaseDeadline(gate, before, health, after, now)
	if !ok || !until.Equal(before.StartedAt.Add(AllowLifetime)) {
		t.Fatal("lease was extended beyond the oldest proof")
	}
}
