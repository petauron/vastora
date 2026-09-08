package agent

import (
	"context"
	"errors"
)

type startupRecoveryError struct {
	stage string
	cause error
}

func (e startupRecoveryError) Error() string {
	return "agent: " + e.stage + " recovery: " + e.cause.Error()
}
func (e startupRecoveryError) Unwrap() error { return e.cause }

// Only bounded public state codes cross the control plane; filesystem paths,
// container errors and sensitive runtime context stay in local diagnostics.
func (s *Store) runtimeRecoveryCode() string {
	s.gatewayStartupMu.RLock()
	defer s.gatewayStartupMu.RUnlock()
	if s.gatewayStartupOK {
		return ""
	}
	var recoveryError startupRecoveryError
	if errors.As(s.gatewayStartupErr, &recoveryError) {
		return recoveryError.stage
	}
	return "pending"
}

// RecoverStartupRuntime can run without Center only when no interrupted task
// owns application state. Otherwise RunTasks must reconcile that receipt first.
func (c Client) RecoverStartupRuntime(ctx context.Context, store *Store) (err error) {
	defer func() { store.setGatewayStartupResult(err) }()
	completion, err := store.PendingTaskCompletion(ctx)
	if err != nil {
		return err
	}
	id, _, err := store.UnresolvedApplicationTaskReceipt(ctx)
	if err != nil {
		return err
	}
	if completion != nil || id != "" {
		return startupRecoveryError{"reconciliation", errors.New("runtime recovery is waiting for the interrupted task to reconcile")}
	}
	if err := store.restoreLandingProxy(ctx); err != nil {
		return startupRecoveryError{"landing", err}
	}
	if restorer, ok := c.Executor.(executorRestorer); ok {
		if err := restorer.Restore(ctx, store); err != nil {
			return startupRecoveryError{"application", err}
		}
	}
	if err := restoreGatewayState(ctx, store, c.GatewayDriver); err != nil {
		return startupRecoveryError{"gateway", err}
	}
	coordinator, _ := c.GatewayDriver.(NodeListenerCoordinator)
	if err := RestoreNodeListenerStartup(ctx, store, c.NodeListener, coordinator); err != nil {
		return startupRecoveryError{"listener", err}
	}
	return nil
}
