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
	if err := ctx.Err(); err != nil {
		return err
	}
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
		return uncertainTaskOutcome(fmt.Errorf("agent: apply gateway revision %d: %w", desired.Revision, err))
	}
	if err := ctx.Err(); err != nil {
		return uncertainTaskOutcome(err)
	}
	if err := driver.Health(ctx); err != nil {
		return uncertainTaskOutcome(fmt.Errorf("agent: verify gateway revision %d: %w", desired.Revision, err))
	}
	if _, err = store.RecordGatewayState(ctx, desired, certificates); err != nil {
		return uncertainTaskOutcome(err)
	}
	return nil
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
