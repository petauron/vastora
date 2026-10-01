//go:build linux && integration

package agent

import (
	"net"
	"os"
	"os/exec"
	"testing"
)

func TestMeridianHostKernelPreflight(t *testing.T) {
	if os.Getenv("VASTORA_MERIDIAN_PREFLIGHT_CHILD") != "1" {
		command := exec.Command("unshare", "--net", "--", os.Args[0], "-test.run=^TestMeridianHostKernelPreflight$", "-test.v")
		command.Env = append(os.Environ(), "VASTORA_MERIDIAN_PREFLIGHT_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated listener preflight: %v\n%s", err, output)
		}
		return
	}
	run := func(args ...string) {
		t.Helper()
		if output, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip: %v %s", err, output)
		}
	}
	run("link", "set", "lo", "up")
	run("addr", "add", "100.64.0.2/32", "dev", "lo")
	artifact := meridianRecoveryArtifact(1, `{"inbounds":[{"protocol":"dokodemo-door","tag":"api","listen":"127.0.0.1","port":10085},{"protocol":"vless","tag":"entry","listen":"100.64.0.2","port":10443}]}`)
	if err := validateMeridianHostListeners(artifact); err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp4", "100.64.0.2:10443")
	if err != nil {
		t.Fatal(err)
	}
	if !meridianHostPortOwned(os.Getpid(), "tcp", 10443) || meridianHostPortOwned(1, "tcp", 10443) {
		t.Fatal("TCP listener ownership was not isolated")
	}
	_ = tcp.Close()
	if meridianHostPortOwned(os.Getpid(), "tcp", 10443) {
		t.Fatal("closed TCP listener still owned")
	}
	udp, err := net.ListenPacket("udp4", "0.0.0.0:443")
	if err != nil {
		t.Fatal(err)
	}
	if !meridianHostPortOwned(os.Getpid(), "udp", 443) || meridianHostPortOwned(1, "udp", 443) {
		t.Fatal("UDP listener ownership was not isolated")
	}
	_ = udp.Close()
	run("addr", "del", "100.64.0.2/32", "dev", "lo")
	if err := validateMeridianHostListeners(artifact); err == nil {
		t.Fatal("missing private address accepted")
	}
}
