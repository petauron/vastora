package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type nativeMissingAccount struct{}

func TestNativeInstallationInventoryIsImmutableAndScoped(t *testing.T) {
	plan, err := planNativePackage("instance", nativePlanFixture())
	if err != nil {
		t.Fatal(err)
	}
	path := nativeInstallationPath("instance")
	data := []byte(`{"version":1,"services":{}}`)
	digest := sha256.Sum256(data)
	resource := RuntimeResource{Kind: "file", LogicalName: "installation", Path: path, Name: "installation.json", SHA256: hex.EncodeToString(digest[:])}
	expected, err := expectedNativeResource("instance", plan, resource)
	if err != nil || expected.ApplicationOwned || expected.Component != "" {
		t.Fatal("inventory must not be restored as application data", err)
	}
	manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t)}
	if err := writeHostFileAtomic(manager.path(path), data, 0644); err != nil {
		t.Fatal(err)
	}
	receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd", Resources: []RuntimeResource{resource}}
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err != nil {
		t.Fatal(err)
	}
	if err := writeHostFileAtomic(manager.path(path), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err == nil {
		t.Fatal("changed inventory accepted")
	}
	resource.Path = nativeInstallationPath("other-instance")
	if _, err := expectedNativeResource("instance", plan, resource); err == nil {
		t.Fatal("foreign inventory accepted")
	}
}

func (nativeMissingAccount) Error() string { return "account not found" }
func (nativeMissingAccount) ExitCode() int { return 2 }

func TestNativeOwnershipRefusesForeignOrChangedResources(t *testing.T) {
	plan, err := planNativePackage("instance", nativePlanFixture())
	if err != nil {
		t.Fatal(err)
	}
	manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), ReadCommand: func(context.Context, string, ...string) ([]byte, error) { return nil, nativeMissingAccount{} }}
	unit := plan.Units[0]
	data := []byte("# Managed by Vastora\n# Application: instance\n[Service]\nExecStart=/example\n")
	digest := sha256.Sum256(data)
	resource := RuntimeResource{Kind: "unit", LogicalName: unit.LogicalName, Name: unit.Name, Path: unit.Path, Component: unit.LogicalName, SHA256: hex.EncodeToString(digest[:])}
	receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd", Resources: []RuntimeResource{resource}}
	if err := writeHostFileAtomic(manager.path(unit.Path), data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RuntimeResource){
		"foreign path":         func(r *RuntimeResource) { r.Path = "/etc/systemd/system/sshd.service" },
		"foreign logical name": func(r *RuntimeResource) { r.LogicalName = "sshd" },
		"foreign component":    func(r *RuntimeResource) { r.Component = "other" },
		"foreign host name":    func(r *RuntimeResource) { r.Name = "sshd.service" },
		"changed digest":       func(r *RuntimeResource) { r.SHA256 = "wrong" },
	} {
		t.Run(name, func(t *testing.T) {
			copy := resource
			mutate(&copy)
			bad := *receipt
			bad.Resources = []RuntimeResource{copy}
			if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, &bad); err == nil {
				t.Fatal("foreign resource accepted")
			}
		})
	}
	duplicate := *receipt
	duplicate.Resources = append([]RuntimeResource{resource}, resource)
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, &duplicate); err == nil {
		t.Fatal("duplicate resource accepted")
	}
	prepared := nativePreparedPackage{Plan: plan, Files: map[string]nativePreparedFile{unit.Path: {Path: unit.Path, Kind: "unit", LogicalName: unit.LogicalName}}}
	if err := inspectNativeCollisions(context.Background(), manager, "instance", prepared, receipt); err != nil {
		t.Fatal(err)
	}
	empty := *receipt
	empty.Resources = nil
	if err := inspectNativeCollisions(context.Background(), manager, "instance", prepared, &empty); err == nil {
		t.Fatal("unowned unit accepted")
	}
	if err := os.Remove(manager.path(unit.Path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", manager.path(unit.Path)); err != nil {
		t.Fatal(err)
	}
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err == nil {
		t.Fatal("symlink unit accepted")
	}
}

func TestNativeAccountRequiresReceiptAndExactIdentity(t *testing.T) {
	plan, err := planNativePackage("instance", nativePlanFixture())
	if err != nil {
		t.Fatal(err)
	}
	name := plan.Users["worker"]
	raw := fmt.Sprintf("%s:x:1001:1001:Vastora instance:/nonexistent:/usr/sbin/nologin\n", name)
	manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), ReadCommand: func(context.Context, string, ...string) ([]byte, error) { return []byte(raw), nil }}
	receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd"}
	prepared := nativePreparedPackage{Plan: plan, Files: map[string]nativePreparedFile{}}
	if err := inspectNativeCollisions(context.Background(), manager, "instance", prepared, receipt); err == nil {
		t.Fatal("account adopted by name without receipt")
	}
	receipt.Resources = []RuntimeResource{{Kind: "user", LogicalName: "worker", Name: name, ID: "1001:1001"}}
	if err := inspectNativeCollisions(context.Background(), manager, "instance", prepared, receipt); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		fmt.Sprintf("%s:x:0:1001:Vastora instance:/nonexistent:/usr/sbin/nologin\n", name),
		fmt.Sprintf("%s:x:1002:1001:Vastora instance:/nonexistent:/usr/sbin/nologin\n", name),
		fmt.Sprintf("%s:x:1001:1001:Vastora other:/nonexistent:/usr/sbin/nologin\n", name),
		fmt.Sprintf("%s:x:1001:1001:Vastora instance:/root:/bin/bash\n", name),
	} {
		raw = bad
		if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err == nil {
			t.Fatal("changed account accepted")
		}
	}
}

func TestNativeMutableConfigurationPreservedButNotSymlinks(t *testing.T) {
	plan, err := planNativePackage("instance", nativePlanFixture())
	if err != nil {
		t.Fatal(err)
	}
	unit := plan.Units[0]
	path := filepath.Join(unit.ConfigDirectory, "environment")
	manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t)}
	if err := writeHostFileAtomic(manager.path(path), []byte("LOCAL_SETTING=changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd", Resources: []RuntimeResource{{Kind: "file", LogicalName: unit.LogicalName + ":environment", Component: unit.LogicalName, Path: path, Name: "environment", SHA256: "original-initialization-digest"}}}
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manager.path(path)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", manager.path(path)); err != nil {
		t.Fatal(err)
	}
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err == nil {
		t.Fatal("symlink configuration accepted")
	}
}

func TestNativeStateOwnerMustMatchFilesystemAndReceipt(t *testing.T) {
	app := nativePlanFixture()
	if os.Getuid() == 0 {
		app.Runtime.NativeServices.Services[1].ManagedUser = ""
		app.Runtime.NativeServices.Services[1].Process.User = "root"
		app.Runtime.NativeServices.Users = []string{}
	}
	plan, err := planNativePackage("instance", app)
	if err != nil {
		t.Fatal(err)
	}
	state := plan.State["data"]
	manager := SystemdHostApplicationManager{RootDir: packageTestDirectory(t), ReadCommand: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(fmt.Sprintf("%s:x:%d:%d:Vastora instance:/nonexistent:/usr/sbin/nologin\n", state.User, os.Getuid(), os.Getgid())), nil
	}}
	if err := os.MkdirAll(manager.path(state.Path), 0700); err != nil {
		t.Fatal(err)
	}
	r := RuntimeResource{Kind: "directory", LogicalName: "data", Path: state.Path, Name: filepath.Base(state.Path), Persistent: true, ID: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())}
	receipt := &InstanceResources{ApplicationID: "instance", Runtime: "systemd", Resources: []RuntimeResource{r}}
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Resources[0].ID = "99999:99999"
	if err := inspectNativeOwnership(context.Background(), manager, "instance", plan, receipt); err == nil {
		t.Fatal("state UID/GID drift accepted")
	}
}
