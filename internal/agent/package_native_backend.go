package agent

import (
	"context"
	"errors"
	"time"

	"github.com/petauron/catalog/catalog"
)

type NativeServicesPackageBackend struct {
	Store          *Store
	website        *daylightHandover
	Manager        SystemdHostApplicationManager
	StateDirectory string
	CheckHealth    func(context.Context, string, int, catalog.Service) error
	prepared       *nativePreparedPackage
	resumeUnits    map[string]bool
}

func (b *NativeServicesPackageBackend) Prepare(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.Operation == "uninstall" {
		return b.Inspect(ctx, task, receipt)
	}
	prepared, err := prepareNativePackage(ctx, b.Manager, b.StateDirectory, task)
	if err != nil {
		return err
	}
	if err := inspectNativeCollisions(ctx, b.Manager, task.ApplicationID, prepared, receipt); err != nil {
		return err
	}
	if task.Operation == "configure" {
		if err := b.verifyServicePortConfiguration(ctx, task, prepared, receipt); err != nil {
			return err
		}
	}
	b.prepared = &prepared
	if b.Store != nil && daylightKey(task.AppKey) && task.Operation != "configure" {
		website, err := b.prepareWebsite(ctx, task, receipt)
		if err != nil {
			return err
		}
		b.website = website
	}
	return nil
}

func (b *NativeServicesPackageBackend) Inspect(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return err
	}
	return inspectNativeOwnership(ctx, b.Manager, task.ApplicationID, plan, receipt)
}

func (b *NativeServicesPackageBackend) Apply(ctx context.Context, task DeploymentTask, receipt *InstanceResources, persist func() error) error {
	if b.prepared == nil {
		return errors.New("agent: native bundle was not prepared")
	}
	if task.Operation == "configure" {
		return errors.New("agent: native service port updates must use the metadata-only executor")
	}
	if b.website != nil {
		return b.website.apply(ctx, task, receipt, persist)
	}
	return applyNativePackage(ctx, b.Manager, task, *b.prepared, receipt, persist)
}

func (b *NativeServicesPackageBackend) Healthy(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return err
	}
	for _, unit := range plan.Units {
		resource := resourceNamed(receipt, "unit", unit.LogicalName)
		if resource == nil {
			return errors.New("agent: native unit receipt missing")
		}
		observed, err := b.Manager.readPackageUnit(ctx, unit.Name)
		if err != nil {
			return err
		}
		if unit.Automatic {
			if err := b.Manager.run(ctx, "systemctl", "is-active", "--quiet", unit.Name); err != nil {
				return errors.New("agent: native service is not active")
			}
			if observed.MainPID <= 0 || observed.InvocationID == "" {
				return errors.New("agent: native service has no running invocation evidence")
			}
			if health := unit.Process.Health; health != nil {
				check := b.CheckHealth
				if check == nil {
					check = waitForServiceEndpoint
				}
				found := false
				for _, service := range task.Manifest.Services {
					if service.Name == health.Service {
						found = true
						port, err := serviceHostPort(task.Config, service)
						if err != nil {
							return err
						}
						if health.Path != "" {
							service.HealthPath = health.Path
						}
						address := task.ServiceAddress
						if address == "" {
							address = "127.0.0.1"
						}
						limit := serviceStartupTimeout
						if health.TimeoutSeconds > 0 {
							limit = time.Duration(health.TimeoutSeconds) * time.Second
						}
						probe, cancel := context.WithTimeout(ctx, limit)
						err = check(probe, address, port, service)
						cancel()
						if err != nil {
							return errors.New("agent: native service endpoint is not healthy")
						}
					}
				}
				if !found {
					return errors.New("agent: native health service is undeclared")
				}
			}
		}
		// A manual worker may be stopped. This is installation readiness only,
		// not application-level traffic/route acceptance.
		resource.MainPID, resource.InvocationID, resource.StartedAt = observed.MainPID, observed.InvocationID, observed.StartedAt
	}
	return nil
}

func (b *NativeServicesPackageBackend) Resume(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return err
	}
	hasUnit := false
	for _, resource := range receipt.Resources {
		hasUnit = hasUnit || resource.Kind == "unit"
	}
	if !hasUnit {
		return nil
	}
	// Restore only the observed pre-backup set, workers before controllers.
	for _, automatic := range []bool{false, true} {
		for _, unit := range plan.Units {
			if unit.Automatic != automatic || !b.resumeUnits[unit.Name] {
				continue
			}
			if err := b.Manager.run(ctx, "systemctl", "start", unit.Name); err != nil {
				return errors.New("agent: native service could not resume after backup")
			}
		}
	}
	return b.Healthy(ctx, task, receipt)
}
