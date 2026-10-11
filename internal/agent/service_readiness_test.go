package agent

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestServiceReadinessAllowsSlowStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), serviceStartupTimeout)
		defer cancel()
		err := waitForServiceReady(ctx, func(context.Context) error {
			if time.Since(start) < 90*time.Second {
				return errors.New("listener not ready during database backup")
			}
			return nil
		})
		if err != nil || time.Since(start) != 90*time.Second {
			t.Fatalf("slow startup: elapsed=%s err=%v", time.Since(start), err)
		}
	})
}

func TestServiceReadinessSharesOneBoundedBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), serviceStartupTimeout)
		defer cancel()
		unhealthy := errors.New("health endpoint returned 503")
		err := waitForServiceReady(ctx, func(context.Context) error {
			if time.Since(start) < 4*time.Minute {
				return errors.New("connection refused")
			}
			return unhealthy
		})
		if time.Since(start) != 5*time.Minute || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, unhealthy) {
			t.Fatalf("startup budget reset or failure lost: elapsed=%s err=%v", time.Since(start), err)
		}
	})
}

func TestServiceReadinessHonorsParentDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		parent, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		ctx, cancel := context.WithTimeout(parent, serviceStartupTimeout)
		defer cancel()
		err := waitForServiceReady(ctx, func(context.Context) error { return errors.New("not ready") })
		if time.Since(start) != 2*time.Second || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("parent deadline ignored: elapsed=%s err=%v", time.Since(start), err)
		}
	})
}

func TestServiceReadinessCannotSucceedAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	err := waitForServiceReady(ctx, func(context.Context) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled probe accepted: %v", err)
	}
	err = waitForServiceReady(ctx, func(context.Context) error { t.Fatal("probe ran after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
