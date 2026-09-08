package center

import (
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingLatencyFreshnessAndSelection(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	s := &Store{now: func() time.Time { return now }}
	target := landing.LatencyTarget{Revision: 3, Peer: landing.PeerIdentity{ID: "peer", PublicKey: "key", Address: "100.64.0.8"}}
	ms := 28.5
	observation := &landing.LatencyObservation{Target: target, State: "direct", LatencyMS: &ms, CheckedAt: now}
	s.recordLandingLatency("proxy", &target, observation)
	selection := LandingSelection{NodeID: "landing", Revision: 3}
	views := s.landingLatencyViews(selection)
	if len(views) != 1 || views[0].LatencyMS == nil || *views[0].LatencyMS != ms {
		t.Fatal("latency should be available without an enabled proxy")
	}
	if len(s.landingLatencyViews(LandingSelection{Revision: 4})) != 0 {
		t.Fatal("old selection reused latency")
	}
	now = now.Add(time.Second)
	observation.CheckedAt, observation.State = now, "derp"
	s.recordLandingLatency("proxy", &target, observation)
	views = s.landingLatencyViews(selection)
	if len(views) != 1 || views[0].State != "unavailable" || views[0].LatencyMS != nil {
		t.Fatal("relay result exposed a direct latency")
	}
	now = now.Add(landingHealthFreshness + time.Second)
	if len(s.landingLatencyViews(selection)) != 0 {
		t.Fatal("stale latency remained visible")
	}
	s.recordLandingLatency("proxy", nil, observation)
	if len(s.landingLatencies) != 0 {
		t.Fatal("removed target retained telemetry")
	}
}
