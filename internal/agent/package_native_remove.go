package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func readNativeGroup(ctx context.Context, manager SystemdHostApplicationManager, name string) (uint32, bool, error) {
	var raw []byte
	var err error
	if manager.ReadCommand != nil {
		raw, err = manager.ReadCommand(ctx, "getent", "group", name)
	} else {
		raw, err = exec.CommandContext(ctx, "getent", "group", name).Output()
	}
	if err != nil {
		var status interface{ ExitCode() int }
		if errors.As(err, &status) && status.ExitCode() == 2 && len(raw) == 0 {
			return 0, false, nil
		}
		return 0, false, errors.New("agent: native group lookup failed")
	}
	fields := strings.Split(strings.TrimSuffix(string(raw), "\n"), ":")
	if len(fields) != 4 || fields[0] != name || fields[3] != "" {
		return 0, false, errors.New("agent: native group is shared or malformed")
	}
	id, err := strconv.ParseUint(fields[2], 10, 32)
	if err != nil || id == 0 {
		return 0, false, errors.New("agent: invalid native group identity")
	}
	// The group member list omits users for whom this is the primary GID.
	// Check those too before deleting either the service account or its group.
	if manager.ReadCommand != nil {
		raw, err = manager.ReadCommand(ctx, "getent", "passwd")
	} else {
		raw, err = exec.CommandContext(ctx, "getent", "passwd").Output()
	}
	if err != nil || len(raw) > 1<<20 {
		return 0, false, errors.New("agent: native primary group membership could not be inspected")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		entry := strings.Split(line, ":")
		if len(entry) != 7 {
			return 0, false, errors.New("agent: malformed native account membership")
		}
		primary, err := strconv.ParseUint(entry[3], 10, 32)
		if err != nil {
			return 0, false, errors.New("agent: malformed native account group")
		}
		if primary == id && entry[0] != name {
			return 0, false, errors.New("agent: native group is another account's primary group")
		}
	}
	return uint32(id), true, nil
}

func (b *NativeServicesPackageBackend) Remove(ctx context.Context, task DeploymentTask, receipt *InstanceResources) error {
	if task.Operation != "uninstall" {
		return errors.New("agent: native removal requires uninstall")
	}
	if err := b.Inspect(ctx, task, receipt); err != nil {
		return err
	}
	plan, err := planNativePackage(task.ApplicationID, task.Manifest)
	if err != nil {
		return err
	}
	// Validate group ownership before stopping any service or deleting files.
	if task.DeleteData {
		for _, resource := range receipt.Resources {
			if resource.Kind == "user" {
				account, exists, err := readNativeAccount(ctx, b.Manager, resource.Name, task.ApplicationID)
				if err != nil || !exists {
					return errors.New("agent: native account unavailable before removal")
				}
				gid, exists, err := readNativeGroup(ctx, b.Manager, resource.Name)
				if err != nil || !exists || gid != account.GID {
					return errors.New("agent: native group is not exclusively owned")
				}
			}
		}
	}
	// Preserve a consistent immutable snapshot. Backup failure resumes the
	// original running set; after deletion begins no automatic restart is safe.
	if err := b.Backup(ctx, task, receipt); err != nil {
		return err
	}
	if b.Store != nil && daylightKey(task.AppKey) {
		if err := b.Store.removeSharedDaylight(ctx, task.ApplicationID, task.DeleteData); err != nil {
			return err
		}
	}
	for _, unit := range plan.Units {
		if resourceNamed(receipt, "unit", unit.LogicalName) == nil {
			continue
		}
		if err := b.Manager.run(ctx, "systemctl", "disable", unit.Name); err != nil {
			return errors.New("agent: native unit could not be disabled")
		}
	}
	var retained []RuntimeResource
	for _, resource := range receipt.Resources {
		expected, err := expectedNativeResource(task.ApplicationID, plan, resource)
		if err != nil {
			return err
		}
		switch resource.Kind {
		case "user":
			if !task.DeleteData {
				retained = append(retained, resource)
			}
		case "directory":
			if resource.Persistent && !task.DeleteData {
				retained = append(retained, resource)
				continue
			}
			actual := b.Manager.path(resource.Path)
			if err := checkPackageParents(actual); err != nil {
				return err
			}
			info, err := os.Lstat(actual)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("agent: native state changed before removal")
			}
			if err := os.RemoveAll(actual); err != nil {
				return err
			}
		case "file", "unit":
			if expected.ApplicationOwned && !task.DeleteData {
				retained = append(retained, resource)
				continue
			}
			if err := removeHostFile(b.Manager.path(resource.Path)); err != nil {
				return err
			}
		}
	}
	if err := b.Manager.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return errors.New("agent: native removal reload failed")
	}
	if task.DeleteData {
		for _, resource := range receipt.Resources {
			if resource.Kind == "user" {
				account, exists, err := readNativeAccount(ctx, b.Manager, resource.Name, task.ApplicationID)
				if err != nil || !exists || account.identity() != resource.ID {
					return errors.New("agent: native account changed before deletion")
				}
				if err := b.Manager.run(ctx, "userdel", resource.Name); err != nil {
					return errors.New("agent: native account removal failed")
				}
				// Some systems remove the private group with userdel; never assume it.
				gid, exists, err := readNativeGroup(ctx, b.Manager, resource.Name)
				if err != nil {
					return err
				}
				if exists {
					if gid != account.GID {
						return errors.New("agent: native group changed before deletion")
					}
					if err := b.Manager.run(ctx, "groupdel", resource.Name); err != nil {
						return errors.New("agent: native group removal failed")
					}
				}
			}
		}
	}
	receipt.Resources = retained
	return nil
}

var _ PackageBackend = (*NativeServicesPackageBackend)(nil)
