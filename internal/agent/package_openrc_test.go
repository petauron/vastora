package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/hostservice"
)

func TestPortableNativePlanUsesConcreteInitPaths(t *testing.T) {
	app := nativePlanFixture()
	app.Runtime.Kind = "native"
	p, err := planNativePackageWithInit("isolated-app", app, hostservice.OpenRC)
	if err != nil {
		t.Fatal(err)
	}
	if p.Runtime != "native" || p.InitSystem != hostservice.OpenRC || p.Units[1].Automatic {
		t.Fatal("lost runtime or manual worker policy")
	}
	for _, unit := range p.Units {
		if !strings.HasPrefix(unit.Path, "/etc/init.d/") || strings.HasSuffix(unit.Path, ".service") {
			t.Fatal("systemd path on OpenRC", unit.Path)
		}
		r := RuntimeResource{Kind: "file", LogicalName: unit.LogicalName + ":process", Component: unit.LogicalName, Name: "process.json", Path: filepath.Join(unit.ConfigDirectory, "process.json")}
		expected, err := expectedNativeResource("isolated-app", p, r)
		if err != nil || expected.ApplicationOwned {
			t.Fatal("launcher must remain immutable", err)
		}
	}
	app.Runtime.Kind = "systemd"
	if _, err := planNativePackageWithInit("isolated-app", app, hostservice.OpenRC); err == nil {
		t.Fatal("systemd-only contract accepted on OpenRC")
	}
}
func TestNativeEnvironmentNeverExecutesShellValues(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "unexpected")
	env := filepath.Join(dir, "environment")
	value := "$(touch " + marker + ") `echo ignored` $HOME"
	raw := "VALUE=" + `"` + value + `"` + "\n"
	if err := os.WriteFile(env, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := readNativeEnvironment(env)
	if err != nil || values["VALUE"] != value {
		t.Fatal(values, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("configuration was evaluated as shell")
	}
	if err := os.WriteFile(env, []byte("VALUE=\"one\"\nVALUE=\"two\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readNativeEnvironment(env); err == nil {
		t.Fatal("ambiguous environment accepted")
	}
}

func TestOpenRCRejectsUnappliedDeclaredLimits(t *testing.T) {
	root := t.TempDir()
	put := func(path, value string) {
		t.Helper()
		path = filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	put("proc/42/cgroup", "0::/owned\n")
	limits := &catalog.ResourceLimits{MemoryBytes: 64 << 20, Pids: 64, CPUMillis: 500}
	put("sys/fs/cgroup/owned/memory.max", "67108864\n")
	put("sys/fs/cgroup/owned/pids.max", "64\n")
	put("sys/fs/cgroup/owned/cpu.max", "50000 100000\n")
	if err := verifyOpenRCLimits(root, 42, limits); err != nil {
		t.Fatal(err)
	}
	put("sys/fs/cgroup/owned/memory.max", "max\n")
	if err := verifyOpenRCLimits(root, 42, limits); err == nil {
		t.Fatal("accepted ignored memory limit")
	}
	if err := verifyOpenRCLimits(root, 42, nil); err != nil {
		t.Fatal("invented undeclared resource requirements", err)
	}
}
