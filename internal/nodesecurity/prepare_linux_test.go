//go:build linux

package nodesecurity

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPackageLockGuardUsesPOSIXLocks(t *testing.T) {
	if path := os.Getenv("VASTORA_SECURITY_LOCK_FIXTURE"); path != "" {
		release, err := holdPackageLocks([]string{path})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		fmt.Println("LOCKED")
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	path := filepath.Join(t.TempDir(), "dpkg.lock")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^TestPackageLockGuardUsesPOSIXLocks$")
	cmd.Env = append(os.Environ(), "VASTORA_SECURITY_LOCK_FIXTURE="+path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "LOCKED\n" {
		t.Fatalf("lock fixture did not start: %v", err)
	}
	if release, err := holdPackageLocks([]string{path}); err == nil {
		release()
		t.Fatal("active dpkg-style lock was ignored")
	}
	_ = stdin.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	release, err := holdPackageLocks([]string{path})
	if err != nil {
		t.Fatal("released lock remained blocked", err)
	}
	release()
}
