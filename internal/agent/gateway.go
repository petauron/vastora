package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/petauron/vastora/internal/gateway"
)

type GatewayDriver interface {
	ApplyRoute(context.Context, gateway.Route) error
	DeleteRoute(context.Context, string) error
	ListRoutes(context.Context) ([]gateway.Route, error)
	GetRouteStatus(context.Context, string) (string, error)
	ApplyConfiguration(context.Context, gateway.DesiredState, []gateway.Certificate) error
	CurrentConfiguration() (gateway.DesiredState, []gateway.Certificate)
	Health(context.Context) error
}

func applyGatewayDesiredState(ctx context.Context, store *Store, driver GatewayDriver, desired gateway.DesiredState, certificates []gateway.Certificate) error {
	store.gatewayMutationMu.Lock()
	defer store.gatewayMutationMu.Unlock()
	if driver == nil {
		return errors.New("agent: gateway capability is not configured")
	}
	if err := desired.Validate(); err != nil {
		return err
	}
	if err := gateway.ValidateCertificatesForState(desired, certificates); err != nil {
		return err
	}
	current, err := store.GatewayState(ctx)
	hasCurrent := err == nil
	if hasCurrent && desired.Revision < current.Desired.Revision {
		return nil
	}
	if hasCurrent && desired.Revision == current.Desired.Revision {
		hash, hashErr := gateway.ConfigurationHash(desired, certificates)
		if hashErr != nil {
			return hashErr
		}
		if hash != current.ConfigHash {
			return fmt.Errorf("agent: gateway revision %d conflicts with the persisted configuration", desired.Revision)
		}
		return nil
	}
	if err != nil && !errors.Is(err, errNoAppliedGatewayState) {
		return err
	}
	if err := driver.ApplyConfiguration(ctx, desired.Sorted(), certificates); err != nil {
		return fmt.Errorf("agent: apply gateway revision %d: %w", desired.Revision, err)
	}
	if err := driver.Health(ctx); err != nil {
		return rollbackGatewayMutation(ctx, driver, current, hasCurrent, fmt.Errorf("agent: verify gateway revision %d: %w", desired.Revision, err))
	}
	if _, err = store.RecordGatewayState(ctx, desired, certificates); err != nil {
		return rollbackGatewayMutation(ctx, driver, current, hasCurrent, err)
	}
	return nil
}

func restoreGatewayState(ctx context.Context, store *Store, driver GatewayDriver) error {
	store.gatewayMutationMu.Lock()
	defer store.gatewayMutationMu.Unlock()
	var restoreErr error
	defer func() { store.setGatewayStartupResult(restoreErr) }()
	if driver == nil {
		return nil
	}
	state, err := store.GatewayState(ctx)
	if err != nil {
		if errors.Is(err, errNoAppliedGatewayState) {
			restoreErr = driver.Health(ctx)
			return restoreErr
		}
		restoreErr = err
		return restoreErr
	}
	if err := driver.ApplyConfiguration(ctx, state.Desired, state.Certificates); err != nil {
		restoreErr = fmt.Errorf("agent: restore last known good gateway configuration: %w", err)
		return restoreErr
	}
	restoreErr = driver.Health(ctx)
	return restoreErr
}

func (c Client) PrepareGatewayStartup(ctx context.Context, store *Store) error {
	return restoreGatewayState(ctx, store, c.GatewayDriver)
}

func (s *Store) setGatewayStartupResult(err error) {
	s.gatewayStartupMu.Lock()
	s.gatewayStartupErr = err
	s.gatewayStartupOK = err == nil
	s.gatewayStartupMu.Unlock()
}

func (s *Store) requireGatewayStartup() error {
	s.gatewayStartupMu.RLock()
	defer s.gatewayStartupMu.RUnlock()
	if s.gatewayStartupOK {
		return nil
	}
	if s.gatewayStartupErr != nil {
		return fmt.Errorf("agent: gateway startup restore failed closed: %w", s.gatewayStartupErr)
	}
	return errors.New("agent: gateway startup restore has not completed")
}

func gatewayRuntimeStatus(ctx context.Context, store *Store, driver GatewayDriver) (bool, int64, string) {
	if driver == nil {
		return false, 0, ""
	}
	live, certificates := driver.CurrentConfiguration()
	var liveHash string
	if live.Revision > 0 {
		hash, err := gateway.ConfigurationHash(live, certificates)
		if err != nil {
			return false, live.Revision, ""
		}
		liveHash = hash
	}
	if err := driver.Health(ctx); err != nil {
		return false, live.Revision, liveHash
	}
	persisted, err := store.GatewayState(ctx)
	if errors.Is(err, errNoAppliedGatewayState) {
		return live.Revision == 0, live.Revision, liveHash
	}
	if err != nil {
		return false, live.Revision, liveHash
	}
	return persisted.Desired.Revision == live.Revision && persisted.ConfigHash == liveHash, live.Revision, liveHash
}

func rollbackGatewayMutation(ctx context.Context, driver GatewayDriver, previous GatewayAppliedState, available bool, cause error) error {
	if !available {
		return cause
	}
	if err := driver.ApplyConfiguration(ctx, previous.Desired, previous.Certificates); err != nil {
		return errors.Join(cause, fmt.Errorf("agent: restore gateway revision %d: %w", previous.Desired.Revision, err))
	}
	if err := driver.Health(ctx); err != nil {
		return errors.Join(cause, fmt.Errorf("agent: verify restored gateway revision %d: %w", previous.Desired.Revision, err))
	}
	return cause
}
