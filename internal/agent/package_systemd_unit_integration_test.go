//go:build integration && linux

package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/petauron/catalog/catalog"
)

func TestPackageSystemdDirectivePathsRealHost(t *testing.T) {
	packageHostedRunner(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root, err := os.MkdirTemp("/var/lib", "vastora-systemd-paths-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	environment := filepath.Join(root, "environment % $ file.env")
	credential := filepath.Join(root, "credential % $ file")
	for path, data := range map[string]string{environment: "FIXTURE_VALUE=fixture-environment\n", credential: "fixture-credential"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	task := packageTestTask(t)
	task.ApplicationID = filepath.Base(root)
	identity := packageIdentity(task.ApplicationID)
	user := "vpath-" + strings.TrimPrefix(identity, "vastora-pkg-")[:12]
	if output, err := exec.CommandContext(ctx, "useradd", "--system", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", user).CombinedOutput(); err != nil {
		t.Fatalf("create isolated fixture service user: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("userdel", user).Run() })
	level, err := exec.Command("systemd-analyze", "log-level").Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("systemd-analyze", "log-level", "debug").Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("systemd-analyze", "log-level", strings.TrimSpace(string(level))).Run() })
	result := "/var/lib/" + identity + "-state/result"
	// The running unprivileged process must actually receive its environment and
	// private 0600 credential copy, not merely pass systemd's permissive parser.
	program := "import os,pathlib,time; assert os.environ['FIXTURE_VALUE']=='fixture-environment'; assert pathlib.Path('/run/" + identity + "/token').read_text()=='fixture-credential'; pathlib.Path(" + strconv.Quote(result) + ").write_text('verified'); time.sleep(300)"
	task.Manifest.Runtime = &catalog.RuntimeSpec{Kind: "systemd", Systemd: &catalog.SystemdRuntime{User: user, Arguments: []catalog.Value{hostFixtureValue("-c"), hostFixtureValue(program)}, StateDirectories: []string{"state"}}}
	unit, err := compilePackageUnit(task, &InstanceResources{}, "/usr/bin/python3", environment, map[string]string{"token": credential}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	name := identity + ".service"
	path := filepath.Join("/etc/systemd/system", name)
	t.Cleanup(func() {
		_ = exec.Command("systemctl", "stop", name).Run()
		_ = os.Remove(path)
		_ = exec.Command("systemctl", "daemon-reload").Run()
		_ = os.RemoveAll(filepath.Dir(result))
	})
	if err := os.WriteFile(path, unit, 0644); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(ctx, "systemd-analyze", "verify", "--man=no", path).CombinedOutput()
	if err != nil || strings.Contains(string(output), "ignoring") {
		t.Fatalf("systemd rejected generated directives: %v\n%s", err, output)
	}
	for _, args := range [][]string{{"daemon-reload"}, {"start", name}} {
		if output, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil {
			journal, _ := exec.Command("journalctl", "-u", name, "-n", "80", "--no-pager").CombinedOutput()
			properties, _ := exec.Command("systemctl", "show", name, "-p", "LoadCredential", "-p", "EnvironmentFiles", "-p", "User", "-p", "Group", "-p", "RuntimeDirectory").CombinedOutput()
			t.Fatalf("systemctl %v: %v\n%s\n%s\n%s", args, err, output, properties, journal)
		}
	}
	for {
		if data, err := os.ReadFile(result); err == nil && string(data) == "verified" {
			return
		}
		select {
		case <-ctx.Done():
			output, _ := exec.Command("journalctl", "-u", name, "-n", "20", "--no-pager").CombinedOutput()
			t.Fatalf("environment or credential was not delivered: %s", output)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
