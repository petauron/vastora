package landing

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	initialReadyTimeout = 30 * time.Second
	initialProbeTimeout = 8 * time.Second
	initialAttempts     = 3
	initialRetryDelay   = time.Second
)

// WaitReady only prepares a first enable. It neither changes routes nor grants
// traffic permission. Monitor always requires new evidence under CheckTimeout;
// a slower initial check can never renew a runtime firewall lease.
func WaitReady(ctx context.Context, peer PeerIdentity, revision uint64) error {
	return waitReady(ctx, peer, revision, NewLinkChecker().Check, (Probe{}).check)
}

func WaitTCPReady(ctx context.Context, peer PeerIdentity, revision uint64) error {
	return waitReady(ctx, peer, revision, NewLinkChecker().Check, (Probe{TCPOnly: true}).check)
}

func waitReady(ctx context.Context, peer PeerIdentity, revision uint64,
	checkLink func(context.Context, PeerIdentity) LinkResult,
	checkBusiness func(context.Context, PeerIdentity, uint64) (BusinessResult, error),
) error {
	ctx, cancel := context.WithTimeout(ctx, initialReadyTimeout)
	defer cancel()
	for attempt := 1; attempt <= initialAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		started := time.Now()
		err := readinessAttempt(ctx, peer, revision, checkLink, checkBusiness)
		if err == nil {
			slog.InfoContext(ctx, "Landing readiness confirmed", "event", "landing.ready", "revision", revision,
				"attempt", attempt, "elapsed_ms", time.Since(started).Milliseconds())
			return nil
		}
		logProbeFailure(ctx, revision, attempt, started, err)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < initialAttempts {
			timer := time.NewTimer(initialRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return errors.New("landing: initial readiness check failed")
}

func readinessAttempt(ctx context.Context, peer PeerIdentity, revision uint64,
	checkLink func(context.Context, PeerIdentity) LinkResult,
	checkBusiness func(context.Context, PeerIdentity, uint64) (BusinessResult, error),
) error {
	if link := checkLink(ctx, peer); link.State != "direct" {
		return &probeError{stage: "direct_before", reason: link.Reason}
	}
	businessCtx, cancel := context.WithTimeout(ctx, initialProbeTimeout)
	_, err := checkBusiness(businessCtx, peer, revision)
	if err == nil {
		err = businessCtx.Err()
	}
	cancel()
	if err != nil {
		return err
	}
	if link := checkLink(ctx, peer); link.State != "direct" {
		return &probeError{stage: "direct_after", reason: link.Reason}
	}
	return ctx.Err()
}
