package agent

import (
	"context"
	"errors"
	"github.com/petauron/vastora/internal/hostservice"
	"os/exec"
	"strings"
)

func (b *NativeServicesPackageBackend) Logs(ctx context.Context, task DeploymentTask, receipt *InstanceResources) ([]RuntimeLog, error) {
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return nil, err
	}
	plan, err := b.Manager.planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return nil, err
	}
	var result []RuntimeLog
	for _, unit := range plan.Units {
		resource := resourceNamed(receipt, "unit", unit.LogicalName)
		if resource == nil {
			continue
		}
		if b.Manager.InitSystem == hostservice.OpenRC {
			var raw []byte
			var err error
			if b.Manager.ReadCommand != nil {
				raw, err = b.Manager.ReadCommand(ctx, "tail", "-c", "65536", "/var/log/messages")
			} else {
				raw, err = exec.CommandContext(ctx, "tail", "-c", "65536", "/var/log/messages").Output()
			}
			if err != nil {
				return nil, errors.New("agent: service log unavailable")
			}
			if len(raw) > 64<<10 {
				raw = raw[len(raw)-(64<<10):]
			}
			name, _ := hostservice.Name(unit.Name)
			var lines []string
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, " "+name+": ") || strings.Contains(line, " "+name+"[") {
					lines = append(lines, line)
				}
			}
			result = append(result, RuntimeLog{Resource: unit.Name, Content: strings.Join(lines, "\n")})
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
