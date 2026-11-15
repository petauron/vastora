package agent

import (
	"context"
	"fmt"
	"os"
	"reflect"

	"github.com/petauron/vastora/internal/hostservice"
	"strings"
	"testing"
)

func nativeLifecycleFixture(t *testing.T) (*NativeServicesPackageBackend, DeploymentTask, *InstanceResources, map[string]bool) {
	t.Helper()
	p := nativeApplyFixture(t)
	app := nativePlanFixture()
	app.Runtime.NativeServices.Users = []string{}
	app.Runtime.NativeServices.Services[1].ManagedUser = ""
	app.Runtime.NativeServices.Services[1].Process.User = "root"
	app.Runtime.NativeServices.Services[1].Process.StateDirectories = nil
	task := DeploymentTask{ID: "backup-fixture", ApplicationID: "instance", Operation: "install", Manifest: app}
	active := map[string]bool{}
	manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), RunCommand: func(_ context.Context, name string, args ...string) error {
		if name != "systemctl" {
			return fmt.Errorf("unexpected command")
		}
		switch args[0] {
		case "start":
			active[args[1]] = true
		case "stop":
			active[args[1]] = false
		case "is-active":
			if !active[args[len(args)-1]] {
				return fmt.Errorf("inactive")
			}
		}
		return nil
	}, ReadCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemctl" || args[0] != "show" {
			return nil, fmt.Errorf("unexpected inspection")
		}
		if active[args[1]] {
			return []byte("MainPID=123\nInvocationID=fixture\nActiveEnterTimestampMonotonic=100\n"), nil
		}
		return []byte("MainPID=0\nInvocationID=\n"), nil
	}}
	receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd", PackageVersion: "1.0.0"}
	b := &NativeServicesPackageBackend{Manager: manager, StateDirectory: packageTestDirectory(t), prepared: &p}
	if err := b.Apply(context.Background(), task, receipt, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	return b, task, receipt, active
}

func TestNativeBackupIsCompleteAndResumesOnlyPriorSet(t *testing.T) {
	b, task, receipt, active := nativeLifecycleFixture(t)
	if err := b.Healthy(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	if err := b.Backup(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	for _, running := range active {
		if running {
			t.Fatal("writer remained active")
		}
	}
	if len(receipt.Backups) != len(receipt.Resources) {
		t.Fatal("incomplete backup")
	}
	keys := map[string]bool{}
	for _, backup := range receipt.Backups {
		if keys[backup.Resource] {
			t.Fatal("backup resource basename collision")
		}
		keys[backup.Resource] = true
		if _, err := readPackageBackup(b.StateDirectory, task, backup); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Resume(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	for _, unit := range b.prepared.Plan.Units {
		if active[unit.Name] != unit.Automatic {
			t.Fatal("backup activated a previously stopped manual worker")
		}
	}
	// Existing immutable snapshot paths deliberately cause failure. Both units
	// were now running, so recovery must resume both rather than just controllers.
	for _, unit := range b.prepared.Plan.Units {
		active[unit.Name] = true
	}
	if err := b.Backup(context.Background(), task, receipt); err == nil {
		t.Fatal("existing snapshot overwritten")
	}
	for _, unit := range b.prepared.Plan.Units {
		if !active[unit.Name] {
			t.Fatal("backup failure left original service stopped")
		}
	}
}

func TestNativeHealthRequiresInvocationNotJustSuccessfulCommand(t *testing.T) {
	b, task, receipt, _ := nativeLifecycleFixture(t)
	b.Manager.ReadCommand = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("MainPID=0\nInvocationID=\n"), nil
	}
	if err := b.Healthy(context.Background(), task, receipt); err == nil || !strings.Contains(err.Error(), "invocation") {
		t.Fatal("false healthy without process evidence", err)
	}
}

func TestNativeSystemdReceiptMigrationPreservesOwnedResources(t *testing.T) {
	b, task, receipt, active := nativeLifecycleFixture(t)
	b.Manager.InitSystem = hostservice.Systemd
	task.Operation, task.Manifest.Runtime.Kind = "upgrade", "native"
	task.AuthorizedCapabilities = []string{"native-services", "root"}
	receipt.RuntimeVersion, receipt.State = 2, "ready"
	receipt.AuthorizedCapabilities = []string{"root", "native-services"}
	before := *receipt
	candidate, err := b.migrateSystemdReceipt(context.Background(), task, receipt)
	if err != nil || candidate == receipt || candidate.Runtime != "native" {
		t.Fatal("owned systemd v2 receipt did not migrate", err)
	}
	if !reflect.DeepEqual(*receipt, before) || !reflect.DeepEqual(candidate.Resources, receipt.Resources) || candidate.PackageVersion != receipt.PackageVersion {
		t.Fatal("migration changed source receipt or resource identities")
	}
	for _, unit := range b.prepared.Plan.Units {
		if active[unit.Name] != unit.Automatic {
			t.Fatal("inspection changed service state")
		}
	}
	unit := resourceNamed(receipt, "unit", "controller")
	if err := os.WriteFile(b.Manager.path(unit.Path), []byte("foreign unit"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.migrateSystemdReceipt(context.Background(), task, receipt); err == nil || !reflect.DeepEqual(*receipt, before) {
		t.Fatal("migration accepted altered ownership or changed receipt")
	}
}

func TestNativeSystemdReceiptMigrationRejectsOtherContracts(t *testing.T) {
	for _, scenario := range []string{"reverse", "v1", "configure", "openrc", "permissions", "unfinished"} {
		t.Run(scenario, func(t *testing.T) {
			b, task, receipt, _ := nativeLifecycleFixture(t)
			b.Manager.InitSystem = hostservice.Systemd
			task.Operation, task.Manifest.Runtime.Kind = "upgrade", "native"
			receipt.RuntimeVersion, receipt.State = 2, "ready"
			switch scenario {
			case "reverse":
				receipt.Runtime, task.Manifest.Runtime.Kind = "native", "systemd"
			case "v1":
				receipt.RuntimeVersion = 1
			case "configure":
				task.Operation = "configure"
			case "openrc":
				b.Manager.InitSystem = hostservice.OpenRC
			case "permissions":
				task.AuthorizedCapabilities = []string{"root"}
			case "unfinished":
				receipt.State = "review-required"
			}
			if _, err := b.migrateSystemdReceipt(context.Background(), task, receipt); err == nil {
				t.Fatal("unreviewed runtime contract migration accepted")
			}
		})
	}
}

func TestNativeMigrationPrepareFailureLeavesDurableReceipt(t *testing.T) {
	b, _, receipt, _ := nativeLifecycleFixture(t)
	b.Manager.InitSystem = hostservice.Systemd
	task := nativeTaskFixture(t)
	task.ApplicationID, task.Operation, task.Manifest.Runtime.Kind = "instance", "upgrade", "native"
	// Keep the installed fixture's root worker plan and application identity.
	task.Manifest.Runtime.NativeServices.Users = []string{}
	task.Manifest.Runtime.NativeServices.Services[1].ManagedUser = ""
	task.Manifest.Runtime.NativeServices.Services[1].Process.User = "root"
	task.Manifest.Runtime.NativeServices.Services[1].Process.StateDirectories = nil
	packageTaskDigest(t, &task)
	receipt.Version, receipt.RuntimeVersion, receipt.State, receipt.AppKey = 1, 2, "ready", task.AppKey
	receipt.AuthorizedCapabilities = task.AuthorizedCapabilities
	executor := PackageExecutor{StateDirectory: b.StateDirectory, Backend: b}
	if err := executor.save(receipt); err != nil {
		t.Fatal(err)
	}
	// Reject the artifact platform before download or any service mutation.
	b.Manager.HostTarget.OS = "unsupported"
	if _, err := executor.Deploy(context.Background(), task); err == nil || !strings.Contains(err.Error(), "unsupported native bundle platform") {
		t.Fatal("preparation failure was not reached", err)
	}
	saved, err := executor.ReadResources(task.ApplicationID)
	if err != nil || !reflect.DeepEqual(saved, receipt) {
		t.Fatal("failed preparation rewrote durable receipt", err)
	}
}
