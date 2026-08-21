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
	Health(context.Context) error
}

func applyGatewayDesiredState(ctx context.Context, store *Store, driver GatewayDriver, desired gateway.DesiredState, certificates []gateway.Certificate) error {
	if driver == nil {
		return errors.New("agent: gateway capability is not configured")
	}
	if err := desired.Validate(); err != nil {
		return err
	}
	if err := gateway.ValidateCertificates(certificates); err != nil {
		return err
	}
	current, err := store.GatewayState(ctx)
	if err == nil && desired.Revision <= current.Desired.Revision {
		return nil
	}
	if err != nil && err.Error() != "agent: no applied gateway state" {
		return err
	}
	if err := driver.ApplyConfiguration(ctx, desired.Sorted(), certificates); err != nil {
		return fmt.Errorf("agent: apply gateway revision %d: %w", desired.Revision, err)
	}
	if err := driver.Health(ctx); err != nil {
		return fmt.Errorf("agent: verify gateway revision %d: %w", desired.Revision, err)
	}
	_, err = store.RecordGatewayState(ctx, desired, certificates)
	return err
}

func restoreGatewayState(ctx context.Context, store *Store, driver GatewayDriver) error {
	if driver == nil {
		return nil
	}
	state, err := store.GatewayState(ctx)
	if err != nil {
		if err.Error() == "agent: no applied gateway state" {
			return driver.Health(ctx)
		}
		return err
	}
	if err := driver.ApplyConfiguration(ctx, state.Desired, state.Certificates); err != nil {
		return fmt.Errorf("agent: restore last known good gateway configuration: %w", err)
	}
	return driver.Health(ctx)
}
