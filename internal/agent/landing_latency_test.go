package agent

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingLatencyFastTargetDoesNotWaitForSlowTarget(t *testing.T) {
	store := &Store{}
	store.setLandingLatencyTargets([]landing.LatencyTarget{{NodeID: "slow", Revision: 1}, {NodeID: "fast", Revision: 1}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, cancelled, fast := make(chan struct{}), make(chan struct{}), make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.runLandingLatencyChecks(ctx, time.Millisecond, func(ctx context.Context, target landing.LatencyTarget) {
			if target.NodeID == "slow" {
				close(started)
				<-ctx.Done()
				close(cancelled)
				return
			}
			select {
			case fast <- struct{}{}:
			case <-ctx.Done():
			}
		})
	}()
	wait := func(signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(5 * time.Second):
			t.Fatal("independent latency worker did not progress")
		}
	}
	wait(started)
	wait(fast)
	wait(fast) // The next fast result also arrives while the slow target is blocked.
	store.setLandingLatencyTargets([]landing.LatencyTarget{{NodeID: "fast", Revision: 1}})
	wait(cancelled)
	cancel()
	wait(done)
}

func TestLandingLatencyTargetChangeCancelsOldIdentity(t *testing.T) {
	store := &Store{}
	target := landing.LatencyTarget{NodeID: "exit", Revision: 1, Peer: landing.PeerIdentity{PublicKey: "old"}}
	store.setLandingLatencyTargets([]landing.LatencyTarget{target})
	_, unchanged := store.landingLatencyTargetSnapshot()
	store.setLandingLatencyTargets([]landing.LatencyTarget{target})
	select {
	case <-unchanged:
		t.Fatal("unchanged heartbeat restarted the probe clocks")
	default:
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan string, 2)
	stopped := make(chan string, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.runLandingLatencyChecks(ctx, time.Hour, func(ctx context.Context, target landing.LatencyTarget) {
			started <- target.Peer.PublicKey
			<-ctx.Done()
			stopped <- target.Peer.PublicKey
		})
	}()
	read := func(ch <-chan string, want string) {
		t.Helper()
		select {
		case got := <-ch:
			if got != want {
				t.Fatalf("identity = %s, want %s", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("target replacement did not progress")
		}
	}
	read(started, "old")
	target.Peer.PublicKey = "new"
	store.setLandingLatencyTargets([]landing.LatencyTarget{target})
	read(stopped, "old")
	read(started, "new")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled latency workers did not exit")
	}
}
