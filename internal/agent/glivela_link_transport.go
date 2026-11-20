package agent

import (
	"context"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/nodediagnostics"
)

type glivelaLinkFailure string

func (e glivelaLinkFailure) Error() string { return string(e) }

type glivelaLinkChecker interface {
	SelfIdentity(context.Context, string) (landing.PeerIdentity, error)
	Check(context.Context, landing.PeerIdentity) landing.LinkResult
}

func verifyGlivelaLink(ctx context.Context, checker glivelaLinkChecker, link nodediagnostics.LinkBandwidthTask, server bool) error {
	self, peer := link.SourcePeer, link.LandingPeer
	if server {
		self, peer = peer, self
	}
	actual, err := checker.SelfIdentity(ctx, self.Address)
	if err != nil || actual != self {
		return glivelaLinkFailure("peer_identity_changed")
	}
	result := checker.Check(ctx, peer)
	if result.Reason == "peer_identity_mismatch" || result.Reason == "peer_identity_changed" || result.Reason == "probe_target_mismatch" {
		return glivelaLinkFailure("peer_identity_changed")
	}
	if result.State != "direct" {
		return glivelaLinkFailure("transport_not_direct")
	}
	return nil
}

// Check both authenticated peers before starting, throughout the bounded probe,
// and after it finishes. These are sampled transport observations, not a promise
// that the private network can never change between samples.
func guardGlivelaLink(parent context.Context, checker glivelaLinkChecker, link nodediagnostics.LinkBandwidthTask, server bool, interval time.Duration, run func(context.Context) ([]byte, error)) ([]byte, error) {
	if err := verifyGlivelaLink(parent, checker, link, server); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				if err := verifyGlivelaLink(ctx, checker, link, server); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	out, err := run(ctx)
	close(stop)
	<-done
	if cause := context.Cause(ctx); cause != nil {
		return nil, errors.Join(err, cause)
	}
	if err != nil {
		return nil, err
	}
	if err := verifyGlivelaLink(parent, checker, link, server); err != nil {
		return nil, err
	}
	return out, nil
}
