//go:build linux && integration

package landing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNativeServerKernelPolicy(t *testing.T) {
	if target := os.Getenv("VASTORA_LANDING_FIREWALL_DIAL"); target != "" {
		protocol := os.Getenv("VASTORA_LANDING_FIREWALL_PROTOCOL")
		connection, err := net.DialTimeout(protocol, target, 300*time.Millisecond)
		if err != nil {
			t.Fatal("fixture connection was blocked")
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(300 * time.Millisecond))
		if _, err := connection.Write([]byte("ok")); err != nil {
			t.Fatal("fixture exchange was blocked")
		}
		var reply [2]byte
		if _, err := io.ReadFull(connection, reply[:]); err != nil || string(reply[:]) != "ok" {
			t.Fatal("fixture reply was blocked")
		}
		return
	}
	if os.Getenv("VASTORA_LANDING_SERVER_KERNEL_CHILD") != "1" {
		command := exec.Command("unshare", "--net", "--", os.Args[0], "-test.run=^TestNativeServerKernelPolicy$", "-test.v")
		command.Env = append(os.Environ(), "VASTORA_LANDING_SERVER_KERNEL_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated native firewall: %v\n%s", err, output)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run := func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "/usr/sbin/nft", args...)
		command.Stdin = bytes.NewReader(input)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("isolated nft: %w: %s", err, stderr.String())
		}
		return output, nil
	}
	policy := testServerFirewall()
	for range 2 {
		if err := policy.install(ctx, run); err != nil {
			actual, _ := run(ctx, nil, "--json", "list", "ruleset")
			expected, _ := json.Marshal(policy.objects())
			t.Fatalf("install: %v\nactual: %s\nexpected: %s", err, actual, expected)
		}
	}
	// The runner's checkout/temporary ancestors need not be traversable by a
	// dedicated system UID. Copy only this non-sensitive fixture executable
	// into a new public-traversal temporary directory; never chmod the runner.
	directory, err := os.MkdirTemp("/tmp", "vastora-landing-kernel-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "landing.test")
	input, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(executable, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		t.Fatal(err)
	}
	// Actual sockets under the dedicated UID exercise the installed kernel
	// rules, not a second Go implementation of the same filtering policy.
	// All addresses live on loopback in this isolated network namespace.
	for _, args := range [][]string{
		{"link", "set", "lo", "up"},
		{"addr", "add", "1.1.1.1/32", "dev", "lo"},
		{"addr", "add", "169.254.169.254/32", "dev", "lo"},
		{"addr", "add", "100.64.0.9/32", "dev", "lo"},
	} {
		if output, err := exec.CommandContext(ctx, "ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("isolated interface fixture: %v: %s", err, output)
		}
	}
	for _, scenario := range []struct {
		name     string
		protocol string
		address  string
		allowed  bool
	}{
		{"public TCP", "tcp4", "1.1.1.1:0", true},
		{"public UDP", "udp4", "1.1.1.1:0", true},
		{"loopback", "tcp4", "127.0.0.1:0", false},
		{"metadata", "tcp4", "169.254.169.254:0", false},
		{"tailnet destination", "udp4", "100.64.0.9:0", false},
		{"management port", "tcp4", "1.1.1.1:22", false},
		{"IPv6", "tcp6", "[::1]:0", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			endpoint := firewallEchoFixture(t, scenario.protocol, scenario.address)
			dial := func(uid uint32) ([]byte, error) {
				command := exec.CommandContext(ctx, executable, "-test.run=^TestNativeServerKernelPolicy$", "-test.v")
				command.Env = append(os.Environ(), "VASTORA_LANDING_FIREWALL_DIAL="+endpoint, "VASTORA_LANDING_FIREWALL_PROTOCOL="+scenario.protocol)
				command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid}}
				return command.CombinedOutput()
			}
			// Prove the destination is present and unrelated host processes
			// remain unaffected; a timeout to an absent fixture is not proof.
			if output, err := dial(0); err != nil {
				t.Fatalf("unrelated host connection: %v: %s", err, output)
			}
			output, err := dial(policy.UID)
			if scenario.allowed && err != nil || !scenario.allowed && err == nil {
				t.Fatalf("dedicated service boundary, allowed=%v: %v: %s", scenario.allowed, err, output)
			}
			if !scenario.allowed {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || !bytes.Contains(output, []byte("fixture")) || ctx.Err() != nil {
					t.Fatalf("cannot attribute failure to socket filtering: %v: %s", err, output)
				}
			}
		})
	}
}

func firewallEchoFixture(t *testing.T, protocol, address string) string {
	t.Helper()
	if protocol == "udp4" {
		listener, err := net.ListenPacket(protocol, address)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })
		go func() {
			var buffer [32]byte
			for {
				n, source, err := listener.ReadFrom(buffer[:])
				if err != nil {
					return
				}
				_, _ = listener.WriteTo(buffer[:n], source)
			}
		}()
		return listener.LocalAddr().String()
	}
	listener, err := net.Listen(protocol, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(time.Second))
				_, _ = io.Copy(connection, io.LimitReader(connection, 2))
			}()
		}
	}()
	return listener.Addr().String()
}
