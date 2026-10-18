//go:build linux

package nodesecurity

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlyVerifiedDistributionUFWProfilesArePrepared(t *testing.T) {
	for _, scenario := range []string{"distribution", "custom", "modified", "wrong-owner", "active-rules", "symlink", "root-symlink"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			profiles := filepath.Join(root, "applications.d")
			if err := os.Mkdir(profiles, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(profiles, "openssh-server")
			if err := os.WriteFile(file, []byte("[OpenSSH]\nports=22/tcp\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if scenario == "custom" {
				if err := os.WriteFile(filepath.Join(profiles, "custom"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "active-rules" {
				if err := os.WriteFile(filepath.Join(root, "user.rules"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "symlink" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/etc/passwd", file); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "root-symlink" {
				link := filepath.Join(t.TempDir(), "ufw")
				if err := os.Symlink(root, link); err != nil {
					t.Fatal(err)
				}
				root = link
			}
			h := Host{run: func(_ context.Context, tool string, args ...string) (string, error) {
				if tool == "dpkg-query" {
					if scenario == "wrong-owner" {
						return "other: " + file, nil
					}
					return "openssh-server: " + file, nil
				}
				if tool == "dpkg" && strings.Join(args, " ") == "--verify openssh-server" {
					if scenario == "modified" {
						return "??5?????? c " + file, nil
					}
					return "", nil
				}
				return "", fmt.Errorf("unexpected command %s %v", tool, args)
			}}
			if got := h.distributionUFWProfiles(context.Background(), root); got != (scenario == "distribution") {
				t.Fatalf("accepted=%v", got)
			}
		})
	}
}

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
