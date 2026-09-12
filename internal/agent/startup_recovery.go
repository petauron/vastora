package agent

import (
	"context"
	"errors"

	"github.com/petauron/vastora/internal/controlplane"
)

type applicationRecoveryFailure struct {
	application controlplane.RecoveryApplication
	cause       error
}

func (e applicationRecoveryFailure) Error() string { return e.cause.Error() }
func (e applicationRecoveryFailure) Unwrap() error { return e.cause }

func recoveryFailure(installation AppliedInstallation, reason string, err error) error {
	var imageError offlineImageUnavailableError
	if errors.As(err, &imageError) {
		reason = "image_unavailable"
	}
	return applicationRecoveryFailure{controlplane.RecoveryApplication{AppKey: installation.AppKey, ApplicationID: installation.ApplicationID, Reason: reason}, err}
}

type offlineImageUnavailableError struct{ cause error }

func (e offlineImageUnavailableError) Error() string { return e.cause.Error() }
func (e offlineImageUnavailableError) Unwrap() error { return e.cause }

func (s *Store) runtimeRecoveryScope() *controlplane.RecoveryScope {
	s.gatewayStartupMu.RLock()
	defer s.gatewayStartupMu.RUnlock()
	var recovery startupRecoveryError
	if s.gatewayStartupOK || !errors.As(s.gatewayStartupErr, &recovery) {
		return nil
	}
	scope := &controlplane.RecoveryScope{Stage: recovery.stage}
	var visit func(error)
	visit = func(err error) {
		if failure, ok := err.(applicationRecoveryFailure); ok {
			scope.Applications = append(scope.Applications, failure.application)
			return
		}
		if joined, ok := err.(interface{ Unwrap() []error }); ok {
			for _, child := range joined.Unwrap() {
				visit(child)
			}
		} else if child := errors.Unwrap(err); child != nil {
			visit(child)
		}
	}
	visit(recovery.cause)
	// These failures have no typed, provably safe repair target. Keep management
	// heartbeats available, but do not issue empty or unsupported claim scopes.
	if scope.Stage == "gateway" || scope.Stage == "application" && len(scope.Applications) == 0 {
		return nil
	}
	if scope.Validate() != nil {
		return nil
	}
	return scope
}

func (s *Store) runtimeRecoveryApplications() []controlplane.RecoveryApplication {
	if scope := s.runtimeRecoveryScope(); scope != nil {
		return scope.Applications
	}
	return nil
}

// Both the Center's SQL filter and this Agent-side check constrain recovery.
// Existing typed executors still enforce ownership, leases and revisions.
func recoveryTaskAllowed(scope controlplane.RecoveryScope, task DeploymentTask) bool {
	if scope.Validate() != nil {
		return false
	}
	if task.Kind == "application.apply" && scope.Stage == "application" && task.ApplicationID != "" {
		switch task.Operation {
		case "install", "configure", "upgrade", "uninstall":
		default:
			return false
		}
		for _, application := range scope.Applications {
			if task.AppKey == application.AppKey && (application.ApplicationID == "" || application.ApplicationID == task.ApplicationID) {
				return true
			}
		}
	}
	if task.Kind == "landing.proxy.apply" && (scope.Stage == "application" || scope.Stage == "landing") {
		return task.LandingProxyState != nil && task.LandingProxyState.Proxy == nil && task.LandingProxyState.Server == nil
	}
	if scope.Stage == "listener" {
		return task.Kind == "node.listener.apply" && task.NodeListenerState != nil && len(task.NodeListenerState.Listener.Routes) == 0
	}
	return false
}

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
