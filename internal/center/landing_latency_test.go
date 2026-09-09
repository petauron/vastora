package center

import (
	"math"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingLatencyFreshnessAndSelection(t *testing.T) {
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	s := &Store{now: func() time.Time { return now }}
	target := landing.LatencyTarget{NodeID: "landing", Revision: 3, Peer: landing.PeerIdentity{ID: "peer", PublicKey: "key", Address: "100.64.0.8"}}
	ms := 28.5
	observation := landing.LatencyObservation{Target: target, State: "direct", LatencyMS: &ms, CheckedAt: now}
	s.recordLandingLatencies("proxy", []landing.LatencyTarget{target}, []landing.LatencyObservation{observation})
	selection := LandingSelection{NodeIDs: []string{"landing"}, Revision: 3}
	views := s.landingLatencyViews(selection)
	if len(views) != 1 || views[0].LatencyMS == nil || *views[0].LatencyMS != ms {
		t.Fatal("latency should be available without an enabled proxy")
	}
	if len(s.landingLatencyViews(LandingSelection{Revision: 4})) != 0 {
		t.Fatal("old selection reused latency")
	}
	now = now.Add(time.Second)
	observation.CheckedAt, observation.State = now, "derp"
	s.recordLandingLatencies("proxy", []landing.LatencyTarget{target}, []landing.LatencyObservation{observation})
	views = s.landingLatencyViews(selection)
	if len(views) != 1 || views[0].State != "unavailable" || views[0].LatencyMS != nil {
		t.Fatal("relay result exposed a direct latency")
	}
	now = now.Add(landingHealthFreshness + time.Second)
	if len(s.landingLatencyViews(selection)) != 0 {
		t.Fatal("stale latency remained visible")
	}
	s.recordLandingLatencies("proxy", nil, []landing.LatencyObservation{observation})
	if len(s.landingLatencies) != 0 {
		t.Fatal("removed target retained telemetry")
	}
}

func TestLandingLatenciesRemainScopedToEachSourceAndServer(t *testing.T) {
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	store := &Store{now: func() time.Time { return now }}
	a := landing.LatencyTarget{NodeID: "a", Revision: 4, Peer: landing.PeerIdentity{ID: "peer-a", PublicKey: "key-a", Address: "100.64.0.8"}}
	b := landing.LatencyTarget{NodeID: "b", Revision: 4, Peer: landing.PeerIdentity{ID: "peer-b", PublicKey: "key-b", Address: "100.64.0.9"}}
	targets := []landing.LatencyTarget{a, b}
	fast, slow := 12.0, 130.0
	first := landing.LatencyObservation{Target: a, State: "direct", LatencyMS: &fast, CheckedAt: now}
	second := landing.LatencyObservation{Target: b, State: "direct", LatencyMS: &slow, CheckedAt: now}
	store.recordLandingLatencies("source-one", targets, []landing.LatencyObservation{first, second})
	store.recordLandingLatencies("source-two", targets, []landing.LatencyObservation{second})
	selection := LandingSelection{NodeIDs: []string{"a", "b"}, Revision: 4}
	views := store.landingLatencyViews(selection)
	if len(views) != 3 || views[0].NodeID != "source-one" || views[0].LandingNodeID != "a" || *views[0].LatencyMS != fast || views[1].LandingNodeID != "b" || *views[1].LatencyMS != slow || views[2].NodeID != "source-two" {
		t.Fatalf("pair samples mixed: %+v", views)
	}
	now = now.Add(time.Second)
	replay := first
	replay.LatencyMS = &slow
	store.recordLandingLatencies("source-one", targets, []landing.LatencyObservation{replay})
	if got := store.landingLatencyViews(selection); *got[0].LatencyMS != fast {
		t.Fatal("replayed sample overwrote current latency")
	}
	invalid := second
	invalid.Target.Peer.PublicKey = "another-key"
	invalid.CheckedAt = now
	store.recordLandingLatencies("source-one", targets, []landing.LatencyObservation{invalid})
	if got := store.landingLatencyViews(selection); len(got) != 3 {
		t.Fatal("one invalid sample discarded unrelated targets")
	}
	nan := math.NaN()
	second.CheckedAt, second.LatencyMS = now, &nan
	store.recordLandingLatencies("source-one", targets, []landing.LatencyObservation{second})
	if got := store.landingLatencyViews(selection); got[1].LatencyMS != nil {
		t.Fatal("invalid numeric latency exposed")
	}
	store.recordLandingLatencies("source-one", []landing.LatencyTarget{b}, nil)
	views = store.landingLatencyViews(selection)
	if len(views) != 2 || views[0].LandingNodeID != "b" || views[1].NodeID != "source-two" {
		t.Fatal("target removal crossed source ownership")
	}
}
