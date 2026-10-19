package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/petauron/catalog/catalog"
)

func nativePlanFixture() catalog.AppManifest {
	process := func(executable string) catalog.SystemdRuntime {
		return catalog.SystemdRuntime{Artifact: "bundle", Executable: executable, Files: []catalog.ArtifactFile{{Path: executable, Executable: true}}}
	}
	controller, worker := process("controller"), process("worker")
	controller.User = "root"
	worker.StateDirectories = []string{"data"}
	return catalog.AppManifest{Artifacts: []catalog.Artifact{{Name: "bundle"}}, Runtime: &catalog.RuntimeSpec{
		Kind: "systemd", Version: 2, RequiredCapabilities: []string{"native-services", "root"}, Storage: []catalog.Storage{{Name: "data", Persistent: true}},
		NativeServices: &catalog.NativeServicesRuntime{Users: []string{"worker"}, Services: []catalog.NativeService{
			{Name: "controller", Start: "automatic", Process: controller}, {Name: "worker", ManagedUser: "worker", Start: "manual", Process: worker},
		}},
	}}
}

func TestNativePackagePlanIsScopedAndDeterministic(t *testing.T) {
	app := nativePlanFixture()
	first, err := planNativePackage("instance-one", app)
	if err != nil {
		t.Fatal(err)
	}
	again, err := planNativePackage("instance-one", app)
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatal("unstable resource identity", err)
	}
	other, err := planNativePackage("instance-two", app)
	if err != nil {
		t.Fatal(err)
	}
	if first.Users["worker"] == other.Users["worker"] || first.Units[0].Path == other.Units[0].Path || first.State["data"].Path == other.State["data"].Path {
		t.Fatal("cross-application resource collision")
	}
	if len(first.Users["worker"]) > 32 || !strings.HasPrefix(first.Users["worker"], "vp-") {
		t.Fatal("invalid system account")
	}
	if !first.Units[0].Automatic || first.Units[1].Automatic {
		t.Fatal("manual worker would be automatically enabled")
	}
	if first.Units[1].Process.User != first.Users["worker"] || first.State["data"].User != first.Users["worker"] || !first.State["data"].Persistent {
		t.Fatal("worker state ownership lost")
	}
	if first.Units[0].ConfigDirectory == first.Units[1].ConfigDirectory || first.Units[0].RuntimeDirectory == first.Units[1].RuntimeDirectory {
		t.Fatal("service credentials share a directory")
	}
	if len(first.Artifacts["bundle"]) != 2 {
		t.Fatal("shared package allowlist incomplete")
	}
	if app.Runtime.NativeServices.Services[1].Process.User != "" {
		t.Fatal("planner mutated signed manifest")
	}
}

func TestNativePackagePlanRejectsUnvalidatedNames(t *testing.T) {
	for _, name := range []string{"../sshd", "/etc/systemd/system/sshd.service", "x\nUser=root"} {
		app := nativePlanFixture()
		app.Runtime.NativeServices.Services[1].Name = name
		if _, err := planNativePackage("instance", app); err == nil {
			t.Fatal("unsafe logical name accepted")
		}
	}
	if _, err := planNativePackage("", nativePlanFixture()); err == nil {
		t.Fatal("missing application identity accepted")
	}
	if nativeAccountName("a", "bc") == nativeAccountName("ab", "c") {
		t.Fatal("ambiguous account identity")
	}
}
