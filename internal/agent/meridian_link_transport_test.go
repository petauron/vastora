package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/nodediagnostics"
)

type fakeMeridianLinkChecker struct {
	mu     sync.Mutex
	self   landing.PeerIdentity
	result landing.LinkResult
}

func (f *fakeMeridianLinkChecker) SelfIdentity(context.Context, string) (landing.PeerIdentity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.self, nil
}

func (f *fakeMeridianLinkChecker) Check(context.Context, landing.PeerIdentity) landing.LinkResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.result
}

func testMeridianLinkTarget() nodediagnostics.LinkBandwidthTask {
	return nodediagnostics.LinkBandwidthTask{SourceNodeID: "source", LandingNodeID: "landing", SourceIP: "100.64.0.8", LandingIP: "100.64.0.9", Port: 34567,
		SourcePeer:  landing.PeerIdentity{ID: "source-peer", PublicKey: "nodekey:source", Address: "100.64.0.8"},
		LandingPeer: landing.PeerIdentity{ID: "landing-peer", PublicKey: "nodekey:landing", Address: "100.64.0.9"}}
}

func TestMeridianLinkTransportRejectsUnsafeStart(t *testing.T) {
	link := testMeridianLinkTarget()
	for _, state := range []string{"derp", "peer_relay", "disconnected", "unknown"} {
		t.Run(state, func(t *testing.T) {
			checker := &fakeMeridianLinkChecker{self: link.SourcePeer, result: landing.LinkResult{State: state}}
			_, err := guardMeridianLink(context.Background(), checker, link, false, time.Hour, func(context.Context) ([]byte, error) {
				t.Fatal("unsafe link started a container")
				return nil, nil
			})
			if !errors.Is(err, meridianLinkFailure("transport_not_direct")) {
				t.Fatal(err)
			}
		})
	}
	checker := &fakeMeridianLinkChecker{self: link.SourcePeer, result: landing.LinkResult{State: "direct"}}
	checker.self.PublicKey = "nodekey:reinstalled"
	if err := verifyMeridianLink(context.Background(), checker, link, false); !errors.Is(err, meridianLinkFailure("peer_identity_changed")) {
		t.Fatal(err)
	}
	checker.self = link.LandingPeer
	if err := verifyMeridianLink(context.Background(), checker, link, true); err != nil {
		t.Fatal(err)
	}
}

func TestMeridianLinkTransportCancelsProbeAndRejectsChangedFinish(t *testing.T) {
	for _, during := range []bool{true, false} {
		t.Run(map[bool]string{true: "during", false: "finish"}[during], func(t *testing.T) {
			link := testMeridianLinkTarget()
			checker := &fakeMeridianLinkChecker{self: link.SourcePeer, result: landing.LinkResult{State: "direct"}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			out, err := guardMeridianLink(ctx, checker, link, false, time.Millisecond, func(ctx context.Context) ([]byte, error) {
				checker.mu.Lock()
				checker.result.State = "derp"
				checker.mu.Unlock()
				if during {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []byte("invalid measurement"), nil
			})
			if len(out) != 0 || !errors.Is(err, meridianLinkFailure("transport_not_direct")) {
				t.Fatalf("result=%q err=%v", out, err)
			}
		})
	}
}

func TestMeridianLinkTransportPreservesCleanupFailure(t *testing.T) {
	link := testMeridianLinkTarget()
	checker := &fakeMeridianLinkChecker{self: link.SourcePeer, result: landing.LinkResult{State: "direct"}}
	_, err := guardMeridianLink(context.Background(), checker, link, false, time.Millisecond, func(ctx context.Context) ([]byte, error) {
		checker.mu.Lock()
		checker.result.State = "derp"
		checker.mu.Unlock()
		<-ctx.Done()
		return nil, errIPQualityCleanupUnconfirmed
	})
	if !errors.Is(err, errIPQualityCleanupUnconfirmed) || !errors.Is(err, meridianLinkFailure("transport_not_direct")) {
		t.Fatal(err)
	}
}
