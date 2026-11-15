package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/sethvargo/go-retry"
)

// A late private address must not strand committed startup resources after the
// first bounded attempt. This loop is armed only by that startup failure and
// stops after recovery; ordinary heartbeat reconnects never start it again.
func retryStartupRuntimeRecovery(ctx context.Context, store *agent.Store, logger *slog.Logger) {
	backoff := retry.WithCappedDuration(time.Minute, retry.NewExponential(10*time.Second))
	err := retry.Do(ctx, backoff, func(ctx context.Context) error {
		// RecoverStartupRuntime retains the existing mutation lock, resource
		// ownership, private-bind and committed-configuration checks. It does
		// not replay tasks or restart normally stopped application workers.
		attempt, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := store.RecoverStartupRuntime(attempt)
		cancel()
		if err == nil {
			return nil
		}
		logger.Error("Gateway startup network recovery retry failed", "event", "gateway.startup_network", "error", controlplane.SafeError(err.Error()))
		return retry.RetryableError(err)
	})
	if err == nil {
		logger.Info("Gateway startup network recovery completed", "event", "gateway.startup_network")
	}
}
