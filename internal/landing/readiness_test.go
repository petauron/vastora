package landing

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func readyLink(context.Context, PeerIdentity) LinkResult {
	return LinkResult{State: "direct", Reason: "fresh_disco_direct_response"}
}

func TestInitialReadinessRetriesTransientFailureBeforeSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls, links := 0, 0
		peer := PeerIdentity{ID: "landing", PublicKey: "key", Address: "100.64.0.8"}
		started := time.Now()
		err := waitReady(context.Background(), peer, 7, func(ctx context.Context, expected PeerIdentity) LinkResult {
			links++
			if expected != peer {
				t.Fatal("retry changed peer identity")
			}
			return readyLink(ctx, expected)
		}, func(ctx context.Context, expected PeerIdentity, revision uint64) (BusinessResult, error) {
			calls++
			if expected != peer || revision != 7 {
				t.Fatal("retry changed desired revision")
			}
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) != initialProbeTimeout {
				t.Fatalf("initial business deadline: %v", deadline)
			}
			if calls == 1 {
				return BusinessResult{}, probeFailure("socks_connect", context.DeadlineExceeded)
			}
			// A cold check may take longer than the runtime lease check budget.
			time.Sleep(4 * time.Second)
			return BusinessResult{}, ctx.Err()
		})
		if err != nil || calls != 2 || links != 3 || time.Since(started) != 5*time.Second {
			t.Fatalf("calls=%d links=%d elapsed=%s err=%v", calls, links, time.Since(started), err)
		}
		if CheckTimeout != 3*time.Second || AllowLifetime != 15*time.Second {
			t.Fatal("initial readiness relaxed the runtime safety policy")
		}
	})
}

func TestInitialReadinessBoundsAttemptsAndChecksDirectAfterBusiness(t *testing.T) {
	for _, scenario := range []string{"business fails", "relay before", "relay after"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				business, links := 0, 0
				err := waitReady(context.Background(), PeerIdentity{}, 1, func(ctx context.Context, peer PeerIdentity) LinkResult {
					links++
					if scenario == "relay before" || (scenario == "relay after" && links%2 == 0) {
						return LinkResult{State: "derp", Reason: "derp_detected"}
					}
					return readyLink(ctx, peer)
				}, func(context.Context, PeerIdentity, uint64) (BusinessResult, error) {
					business++
					if scenario == "business fails" {
						return BusinessResult{}, errors.New("failed")
					}
					return BusinessResult{}, nil
				})
				if err == nil {
					t.Fatal("unready peer accepted")
				}
				wantBusiness, wantLinks := initialAttempts, initialAttempts
				if scenario == "relay before" {
					wantBusiness = 0
				}
				if scenario == "relay after" {
					wantLinks *= 2
				}
				if business != wantBusiness || links != wantLinks {
					t.Fatalf("business=%d links=%d", business, links)
				}
			})
		})
	}
}

func TestInitialReadinessHonorsCancellationDuringRetryDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		calls := 0
		go func() { time.Sleep(initialRetryDelay / 2); cancel() }()
		err := waitReady(ctx, PeerIdentity{}, 1, readyLink, func(context.Context, PeerIdentity, uint64) (BusinessResult, error) {
			calls++
			return BusinessResult{}, errors.New("failed")
		})
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("cancellation ignored: calls=%d err=%v", calls, err)
		}
	})
}

func TestInitialReadinessHonorsOverallAndBusinessDeadlines(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := time.Now()
		calls := 0
		err := waitReady(context.Background(), PeerIdentity{}, 1, func(ctx context.Context, peer PeerIdentity) LinkResult {
			// Include link time so the final attempt reaches the overall bound.
			time.Sleep(CheckTimeout)
			return readyLink(ctx, peer)
		}, func(ctx context.Context, _ PeerIdentity, _ uint64) (BusinessResult, error) {
			calls++
			<-ctx.Done()
			return BusinessResult{}, ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != initialReadyTimeout || calls != 3 {
			t.Fatalf("budget ignored: elapsed=%s calls=%d err=%v", time.Since(started), calls, err)
		}
	})
}

func TestInitialReadinessRejectsSuccessReturnedAfterDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		err := readinessAttempt(context.Background(), PeerIdentity{}, 1, readyLink, func(ctx context.Context, _ PeerIdentity, _ uint64) (BusinessResult, error) {
			<-ctx.Done()
			return BusinessResult{}, nil
		})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired readiness accepted: %v", err)
		}
	})
}
