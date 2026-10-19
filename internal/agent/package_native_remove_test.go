package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestNativeUninstallRetainsConfigAndCanExplicitlyDeleteIt(t *testing.T) {
	b, task, receipt, active := nativeLifecycleFixture(t)
	task.ID = "remove-retain"
	task.Operation = "uninstall"
	if err := b.Remove(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Resources) != 2 {
		t.Fatal("expected both service configurations retained")
	}
	for _, resource := range receipt.Resources {
		if resource.Kind != "file" {
			t.Fatal("unexpected retained executable")
		}
		if _, err := os.Stat(b.Manager.path(resource.Path)); err != nil {
			t.Fatal(err)
		}
	}
	for _, unit := range b.prepared.Plan.Units {
		if active[unit.Name] {
			t.Fatal("service left running")
		}
		if _, err := os.Lstat(b.Manager.path(unit.Path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unit not removed", err)
		}
	}
	retained := append([]RuntimeResource(nil), receipt.Resources...)
	receipt.State = "retained"
	task.ID = "remove-data"
	task.DeleteData = true
	if err := b.Remove(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.Resources) != 0 {
		t.Fatal("explicit deletion left resources")
	}
	for _, resource := range retained {
		if _, err := os.Lstat(b.Manager.path(resource.Path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("configuration not deleted", err)
		}
	}
	if len(receipt.Backups) != 6 {
		t.Fatal("rollback snapshots were not retained")
	}
}

func TestNativeUninstallRefusesTamperedUnitBeforeStopping(t *testing.T) {
	b, task, receipt, active := nativeLifecycleFixture(t)
	task.ID = "remove-tampered"
	task.Operation = "uninstall"
	path := b.prepared.Plan.Units[0].Path
	if err := os.WriteFile(b.Manager.path(path), []byte("foreign service"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(context.Background(), task, receipt); err == nil {
		t.Fatal("tampered unit removed")
	}
	if !active[b.prepared.Plan.Units[0].Name] {
		t.Fatal("preflight failure stopped service")
	}
}

func TestNativeAccountDeletionRequiresExclusiveGroup(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint(shared), func(t *testing.T) {
			app := nativePlanFixture()
			plan, err := planNativePackage("instance", app)
			if err != nil {
				t.Fatal(err)
			}
			name := plan.Users["worker"]
			userExists, groupExists := true, true
			var commands []string
			manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), ReadCommand: func(_ context.Context, command string, args ...string) ([]byte, error) {
				if command == "getent" && len(args) == 1 && args[0] == "passwd" {
					if userExists {
						return []byte(fmt.Sprintf("%s:x:1001:1001:Vastora instance:/nonexistent:/usr/sbin/nologin\n", name)), nil
					}
					return nil, nil
				}
				if command != "getent" || len(args) != 2 || args[1] != name {
					return nil, fmt.Errorf("unexpected lookup")
				}
				if args[0] == "passwd" && userExists {
					return []byte(fmt.Sprintf("%s:x:1001:1001:Vastora instance:/nonexistent:/usr/sbin/nologin\n", name)), nil
				}
				if args[0] == "group" && groupExists {
					members := ""
					if shared {
						members = "someone"
					}
					return []byte(fmt.Sprintf("%s:x:1001:%s\n", name, members)), nil
				}
				return nil, nativeMissingAccount{}
			}, RunCommand: func(_ context.Context, command string, args ...string) error {
				commands = append(commands, command)
				if command == "userdel" || command == "groupdel" {
					if len(args) != 1 || args[0] != name {
						return fmt.Errorf("unsafe deletion arguments")
					}
					if command == "userdel" {
						userExists = false
					} else {
						groupExists = false
					}
				}
				return nil
			}}
			b := NativeServicesPackageBackend{Manager: manager, StateDirectory: packageTestDirectory(t)}
			receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd", Resources: []RuntimeResource{{Kind: "user", LogicalName: "worker", Name: name, ID: "1001:1001"}}}
			task := DeploymentTask{ID: "account-remove", ApplicationID: "instance", Operation: "uninstall", Manifest: app, DeleteData: true}
			err = b.Remove(context.Background(), task, receipt)
			if shared {
				if err == nil || len(commands) != 0 || !userExists || !groupExists {
					t.Fatal("shared group was modified", err)
				}
				return
			}
			if err != nil || userExists || groupExists || len(receipt.Resources) != 0 {
				t.Fatal("owned account removal failed", err)
			}
		})
	}
}
