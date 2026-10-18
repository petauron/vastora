package agent

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func nativeApplyFixture(t *testing.T) nativePreparedPackage {
	t.Helper()
	app := nativePlanFixture()
	app.Runtime.NativeServices.Users = []string{}
	app.Runtime.NativeServices.Services[1].ManagedUser = ""
	app.Runtime.NativeServices.Services[1].Process.User = "root"
	app.Runtime.NativeServices.Services[1].Process.StateDirectories = nil
	plan, err := planNativePackage("instance", app)
	if err != nil {
		t.Fatal(err)
	}
	p := nativePreparedPackage{Plan: plan, Files: map[string]nativePreparedFile{}}
	for _, unit := range plan.Units {
		p.Files[unit.Path] = nativePreparedFile{Path: unit.Path, Kind: "unit", LogicalName: unit.LogicalName, Component: unit.LogicalName, Mode: 0644, Data: []byte("# Managed by Vastora\n# Application: instance\n[Service]\nExecStart=/example\n")}
		path := unit.ConfigDirectory + "/environment"
		p.Files[path] = nativePreparedFile{Path: path, Kind: "file", LogicalName: unit.LogicalName + ":environment", Component: unit.LogicalName, Mode: 0600, ApplicationOwned: true, Data: []byte("INITIAL=1\n")}
	}
	return p
}

func TestNativeApplyPreservesConfigurationAndManualStart(t *testing.T) {
	p := nativeApplyFixture(t)
	var calls []string
	manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), RunCommand: func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}}
	receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd"}
	task := DeploymentTask{ApplicationID: "instance", Operation: "install"}
	saved := 0
	if err := applyNativePackage(context.Background(), manager, task, p, receipt, func() error { saved++; return nil }); err != nil {
		t.Fatal(err)
	}
	if saved != len(p.Files) {
		t.Fatal("not all resources persisted")
	}
	for _, call := range calls {
		if strings.Contains(call, "start "+p.Plan.Units[1].Name) || strings.Contains(call, "enable "+p.Plan.Units[1].Name) {
			t.Fatal("manual worker activated")
		}
	}
	path := p.Plan.Units[0].ConfigDirectory + "/environment"
	custom := []byte("APPLICATION_SETTING=preserved\n")
	if err := os.WriteFile(manager.path(path), custom, 0600); err != nil {
		t.Fatal(err)
	}
	task.Operation = "upgrade"
	calls = nil
	if err := applyNativePackage(context.Background(), manager, task, p, receipt, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manager.path(path))
	if err != nil || string(raw) != string(custom) {
		t.Fatal("application configuration overwritten", err)
	}
	if len(calls) < 2 || calls[0] != "systemctl stop "+p.Plan.Units[0].Name || calls[1] != "systemctl stop "+p.Plan.Units[1].Name {
		t.Fatal("writers not stopped in order", calls)
	}
	// Reinstallation after a data-preserving uninstall is not first-time
	// initialization. Retained application configuration must survive too.
	for _, unit := range p.Plan.Units {
		if err := os.Remove(manager.path(unit.Path)); err != nil {
			t.Fatal(err)
		}
	}
	receipt.Resources = slices.DeleteFunc(receipt.Resources, func(r RuntimeResource) bool { return r.Kind == "unit" })
	receipt.State = "retained"
	task.Operation = "install"
	if err := applyNativePackage(context.Background(), manager, task, p, receipt, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(manager.path(path))
	if err != nil || string(raw) != string(custom) {
		t.Fatal("retained config reinitialized", err)
	}
	task.Operation = "upgrade"
	if err := os.Remove(manager.path(path)); err != nil {
		t.Fatal(err)
	}
	calls = nil
	if err := applyNativePackage(context.Background(), manager, task, p, receipt, func() error { return nil }); err == nil {
		t.Fatal("missing configuration recreated on upgrade")
	}
	if len(calls) != 0 {
		t.Fatal("failed preflight stopped services")
	}
}

func TestNativeApplyPersistenceFailureDoesNotStartServices(t *testing.T) {
	p := nativeApplyFixture(t)
	var calls []string
	manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), RunCommand: func(_ context.Context, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}}
	receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd"}
	err := applyNativePackage(context.Background(), manager, DeploymentTask{ApplicationID: "instance", Operation: "install"}, p, receipt, func() error { return errors.New("receipt disk failure") })
	if err == nil || len(calls) != 0 || len(receipt.Resources) != 1 {
		t.Fatal("partial installation incorrectly continued", calls, err)
	}
}
