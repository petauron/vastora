package hostservice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectRequiresRunningOpenRCAndTools(t *testing.T) {
	root := t.TempDir()
	if _, err := DetectAt(root); err == nil {
		t.Fatal("unsupported host advertised an executor")
	}
	if err := os.MkdirAll(filepath.Join(root, "run/openrc"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openrc-run", "rc-service", "rc-update", "supervise-daemon"} {
		if err := os.MkdirAll(filepath.Join(root, "sbin"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sbin", name), []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if kind, err := DetectAt(root); err != nil || kind != OpenRC {
		t.Fatal(kind, err)
	}
	if err := os.Remove(filepath.Join(root, "sbin/supervise-daemon")); err != nil {
		t.Fatal(err)
	}
	if _, err := DetectAt(root); err == nil {
		t.Fatal("OpenRC without supervisor advertised")
	}
}
func TestOpenRCActionsDoNotInvokeSystemd(t *testing.T) {
	var commands []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	for _, action := range []string{"reload", "enable", "start", "stop", "status"} {
		if err := Action(context.Background(), OpenRC, run, action, "vastora-agent.service"); err != nil {
			t.Fatal(err)
		}
	}
	want := "rc-update add vastora-agent default\nrc-service vastora-agent start\nrc-service vastora-agent stop\nrc-service vastora-agent status"
	if strings.Join(commands, "\n") != want {
		t.Fatal(commands)
	}
	if err := Action(context.Background(), OpenRC, run, "stop", "../../ssh.service"); err == nil {
		t.Fatal("unsafe service name accepted")
	}
}
func TestKernelProcessEvidenceChangesOnPIDReuse(t *testing.T) {
	root := t.TempDir()
	stat := filepath.Join(root, "proc/42/stat")
	boot := filepath.Join(root, "proc/sys/kernel/random/boot_id")
	for _, p := range []string{stat, boot} {
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(boot, []byte("c26b499c-c94f-4b2e-9001-01f055a54866\n"), 0644); err != nil {
		t.Fatal(err)
	}
	write := func(ticks string) {
		fields := strings.Fields("S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 " + ticks)
		if err := os.WriteFile(stat, []byte("42 (worker with spaces) "+strings.Join(fields, " ")), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("100")
	first, err := ProcessInvocation(root, 42)
	if err != nil {
		t.Fatal(err)
	}
	write("200")
	second, err := ProcessInvocation(root, 42)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || second.Started != "200" {
		t.Fatal("PID reuse retained an invocation")
	}
}
