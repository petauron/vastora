package landing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConcurrentPeerMonitorsReleaseConnectionsAcrossGroupReplacement(t *testing.T) {
	base, open, accepted := unixChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("peers") == "false" {
			w.Write([]byte(`{"BackendState":"Running","Self":{"ID":"self","PublicKey":"key","TailscaleIPs":["100.64.0.1"]}}`))
			return
		}
		w.Write([]byte(`{"BackendState":"Stopped"}`))
	}))
	dial := base.HTTPClient.Transport.(*http.Transport).DialContext
	base.Close()
	baseline := runtime.NumGoroutine()
	for batch := range 10 {
		ctx, cancel := context.WithCancel(context.Background())
		var group sync.WaitGroup
		defer func() { cancel(); group.Wait() }()
		reports := make(chan struct{}, 8)
		checkers := make([]*LinkChecker, 0, 8)
		for index := range 8 {
			checker := NewLinkChecker()
			defer checker.Close()
			checker.HTTPClient.Transport.(*http.Transport).DialContext = dial
			if _, err := checker.SelfIdentity(ctx, "100.64.0.1"); err != nil {
				cancel()
				t.Fatal(err)
			}
			checkers = append(checkers, checker)
			gate, err := NewBridgeGate(PeerIdentity{ID: fmt.Sprintf("peer-%d-%d", batch, index), PublicKey: fmt.Sprintf("nodekey:peer%d", index), Address: fmt.Sprintf("100.64.0.%d", index+10)}, "br-owned", uint64(batch+1))
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			document, err := json.Marshal(fixtureNFT(t, gate))
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			gate.run = func(_ context.Context, input []byte, _ ...string) ([]byte, error) {
				if input != nil {
					return nil, nil
				}
				return document, nil
			}
			var reportOnce sync.Once
			monitor := Monitor{Gate: gate, Links: checker, CheckBusiness: func(context.Context, PeerIdentity, uint64) (BusinessResult, error) {
				return BusinessResult{}, errors.New("unexpected business probe")
			}, Report: func(MonitorStatus) {
				reportOnce.Do(func() { reports <- struct{}{} })
			}}
			group.Add(1)
			go func() { defer group.Done(); _ = monitor.Run(ctx) }()
		}
		for range 8 {
			select {
			case <-reports:
			case <-time.After(2 * time.Second):
				cancel()
				group.Wait()
				t.Fatal("peer monitor did not report")
			}
		}
		if count := open.Load(); count != 8 {
			cancel()
			group.Wait()
			t.Fatalf("group has %d connections, want 8", count)
		}
		cancel()
		group.Wait()
		awaitNoConnections(t, open)
		for _, checker := range checkers {
			if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err == nil {
				t.Fatal("retired group retained usable checker")
			}
		}
		deadline := time.Now().Add(time.Second)
		for runtime.NumGoroutine() > baseline+6 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if count := runtime.NumGoroutine(); count > baseline+6 {
			t.Fatalf("goroutines accumulated: baseline=%d now=%d", baseline, count)
		}
	}
	if count := accepted.Load(); count != 80 {
		t.Fatalf("unexpected reconnects or retained pools: %d", count)
	}
}

func TestMonitorOwnsUnixConnectionsAcrossReplacementAndSetupFailure(t *testing.T) {
	for _, mode := range []string{"cancel", "invalid", "setup-failure"} {
		t.Run(mode, func(t *testing.T) {
			base, open, accepted := unixChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("peers") == "false" {
					w.Write([]byte(`{"BackendState":"Running","Self":{"ID":"self","PublicKey":"key","TailscaleIPs":["100.64.0.1"]}}`))
					return
				}
				w.Write([]byte(`{"BackendState":"Stopped"}`))
			}))
			dial := base.HTTPClient.Transport.(*http.Transport).DialContext
			base.Close()
			for range 20 {
				checker := NewLinkChecker()
				checker.HTTPClient.Transport.(*http.Transport).DialContext = dial
				// Warm an idle connection before Run so even validation failures
				// must release a real Unix FD, not just an unused transport.
				if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err != nil {
					t.Fatal(err)
				}
				gate := fixtureGate(t)
				document, err := json.Marshal(fixtureNFT(t, gate))
				if err != nil {
					t.Fatal(err)
				}
				gate.run = func(_ context.Context, input []byte, _ ...string) ([]byte, error) {
					if mode == "setup-failure" {
						return nil, errors.New("simulated nft failure")
					}
					if input != nil {
						return nil, nil
					}
					return document, nil
				}
				ctx, cancel := context.WithCancel(context.Background())
				monitor := Monitor{Gate: gate, Links: checker,
					CheckBusiness: func(context.Context, PeerIdentity, uint64) (BusinessResult, error) {
						return BusinessResult{}, errors.New("unexpected probe")
					},
					Report: func(MonitorStatus) { cancel() },
				}
				if mode == "invalid" {
					monitor.Gate = nil
				}
				err = monitor.Run(ctx)
				cancel()
				if err == nil {
					t.Fatal("expected cancellation or initialization failure")
				}
				awaitNoConnections(t, open)
				if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err == nil {
					t.Fatal("terminated monitor left its checker usable")
				}
			}
			if accepted.Load() != 20 {
				t.Fatalf("connections accumulated across replacements: %d", accepted.Load())
			}
		})
	}
}

func TestMonitorBlocksWithoutProxyLifecycleOnExit(t *testing.T) {
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
	reports := 0
	monitor := Monitor{Gate: gate, Links: &LinkChecker{HTTPClient: &http.Client{Transport: latencyTransport{server.URL}}},
		CheckBusiness: func(context.Context, PeerIdentity, uint64) (BusinessResult, error) {
			t.Fatal("business check ran without a direct peer")
			return BusinessResult{}, nil
		},
		Report: func(status MonitorStatus) {
			reports++
			if status.State != "blocked" || writes == 0 {
				t.Fatalf("unconfirmed route was not blocked: writes=%d status=%+v", writes, status)
			}
			cancel()
		},
	}
	if err := monitor.Run(ctx); err != context.Canceled || reports != 1 {
		t.Fatalf("shutdown cleanup changed: reports=%d err=%v", reports, err)
	}
}

func TestRecoveredMonitorKeepsGateClosedOnUnconfirmedInitialRenewal(t *testing.T) {
	gate := fixtureGate(t)
	document, err := json.Marshal(fixtureNFT(t, gate))
	if err != nil {
		t.Fatal(err)
	}
	gate.run = func(_ context.Context, input []byte, _ ...string) ([]byte, error) {
		if input != nil && strings.Contains(string(input), `"element"`) {
			return nil, errors.New("simulated lost renewal response")
		}
		if input != nil {
			return nil, nil
		}
		return document, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/localapi/v0/ping" {
			seconds := 0.001
			_ = json.NewEncoder(w).Encode(pingResult{IP: gate.peer.Address, NodeIP: gate.peer.Address, Endpoint: "203.0.113.8:41641", LatencySeconds: &seconds})
			return
		}
		_ = json.NewEncoder(w).Encode(localStatus{BackendState: "Running", Peer: map[string]*localPeer{"peer": {ID: gate.peer.ID, PublicKey: gate.peer.PublicKey, TailscaleIPs: []string{gate.peer.Address}}}})
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	reports := 0
	monitor := Monitor{Gate: gate, Links: &LinkChecker{HTTPClient: &http.Client{Transport: latencyTransport{server.URL}}},
		CheckBusiness: func(_ context.Context, peer PeerIdentity, revision uint64) (BusinessResult, error) {
			started := time.Now()
			return BusinessResult{Peer: peer, Revision: revision, TCP: true, UDP: true, UDPRelay: peer.Address + ":1081", ExitIPv4: "1.1.1.1", StartedAt: started, CheckedAt: time.Now()}, nil
		},
		Report: func(status MonitorStatus) {
			reports++
			if status.State != "blocked" {
				t.Fatalf("startup renewal failure opened the gate: status=%+v", status)
			}
			cancel()
		},
	}
	if err := monitor.Run(ctx); err != context.Canceled || reports != 1 {
		t.Fatalf("recovered monitor cleanup changed: reports=%d err=%v", reports, err)
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

func TestTCPOnlyLeaseAcceptsMeasuredFourSecondBusinessProof(t *testing.T) {
	gate := fixtureGate(t)
	now := time.Now()
	before := LinkResult{State: "direct", Reason: "fresh_disco_direct_response", StartedAt: now.Add(-9 * time.Second), CheckedAt: now.Add(-8 * time.Second)}
	business := BusinessResult{Peer: gate.peer, Revision: gate.revision, TCP: true, ExitIPv4: "1.1.1.1", StartedAt: now.Add(-8 * time.Second), CheckedAt: now.Add(-4 * time.Second)}
	after := LinkResult{State: "direct", Reason: "fresh_disco_direct_response", StartedAt: now.Add(-2 * time.Second), CheckedAt: now.Add(-time.Second)}
	if until, ok := leaseDeadlineForTransport(gate, before, business, after, now, true); !ok || !until.Equal(before.StartedAt.Add(AllowLifetime)) {
		t.Fatal("fresh TCP-only proof rejected or lease extended")
	}
	if _, ok := leaseDeadlineForTransport(gate, before, business, after, now, false); ok {
		t.Fatal("UDP-capable route accepted TCP-only proof")
	}
	business.CheckedAt = business.StartedAt.Add(TCPCheckTimeout + time.Nanosecond)
	if _, ok := leaseDeadlineForTransport(gate, before, business, after, now, true); ok {
		t.Fatal("TCP-only proof beyond its time budget opened the gate")
	}
}
