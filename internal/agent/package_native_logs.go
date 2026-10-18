package agent

import "context"

func (b *NativeServicesPackageBackend) Logs(ctx context.Context, task DeploymentTask, receipt *InstanceResources) ([]RuntimeLog, error) {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return nil, err
	}
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return nil, err
	}
	var result []RuntimeLog
	for _, unit := range plan.Units {
		resource := resourceNamed(receipt, "unit", unit.LogicalName)
		if resource == nil {
			continue
		}
		// Reuse the existing bounded journal reader, not another shell or log
		// transport. The public maintenance layer applies credential redaction.
		copy := *resource
		copy.LogicalName = "service"
		view := *receipt
		view.Resources = []RuntimeResource{copy}
		reader := SystemdPackageBackend{Manager: b.Manager}
		logs, err := reader.Logs(ctx, task, &view)
		if err != nil {
			return nil, err
		}
		result = append(result, logs...)
	}
	return result, nil
}

var _ packageMaintenanceBackend = (*NativeServicesPackageBackend)(nil)
