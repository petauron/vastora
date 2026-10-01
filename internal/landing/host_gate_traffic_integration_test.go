//go:build linux && integration

package landing

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestHostGateKernelTraffic(t *testing.T) {
	if os.Getenv("VASTORA_HOST_GATE_CLIENT") == "1" {
		hostGateUDPClient()
		return
	}
	if os.Getenv("VASTORA_HOST_GATE_TRAFFIC") != "1" {
		command := exec.Command("unshare", "--net", "--", os.Args[0], "-test.run=^TestHostGateKernelTraffic$", "-test.v")
		command.Env = append(os.Environ(), "VASTORA_HOST_GATE_TRAFFIC=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated traffic test: %v\n%s", err, output)
		}
		return
	}
	for _, args := range [][]string{{"link", "set", "lo", "up"}, {"addr", "add", "100.64.0.8/32", "dev", "lo"}} {
		if output, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip: %v %s", err, output)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gate, err := NewHostGate(PeerIdentity{ID: "traffic", PublicKey: "nodekey:traffic", Address: "100.64.0.8"}, 1001, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Install(ctx); err != nil {
		t.Fatal(err)
	}
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("100.64.0.8"), Port: UDPRelayFirst})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	addresses := make(chan *net.UDPAddr, 4)
	go func() {
		buf := make([]byte, 64)
		for {
			n, addr, err := server.ReadFromUDP(buf)
			if err != nil {
				return
			}
			addresses <- addr
			_, _ = server.WriteToUDP(buf[:n], addr)
		}
	}()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHostGateKernelTraffic$")
	command.Env = append(os.Environ(), "VASTORA_HOST_GATE_CLIENT=1")
	command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 0, Gid: 1001, Groups: []uint32{}}}
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close(); _ = command.Process.Kill(); _ = command.Wait() }()
	scanner := bufio.NewScanner(output)
	expect := func(action, want string) {
		t.Helper()
		if _, err := fmt.Fprintln(input, action); err != nil {
			t.Fatal(err)
		}
		if !scanner.Scan() || scanner.Text() != want {
			t.Fatalf("%s got %q want %s", action, scanner.Text(), want)
		}
	}
	expect("send", "blocked")
	// Agent's different group remains able to probe a closed gate.
	probe, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("100.64.0.8"), Port: UDPRelayFirst})
	if err != nil {
		t.Fatal(err)
	}
	_ = probe.SetDeadline(time.Now().Add(time.Second))
	_, _ = probe.Write([]byte("probe"))
	buf := make([]byte, 32)
	if _, err := probe.Read(buf); err != nil {
		t.Fatal("gate blocked Agent probe:", err)
	}
	_ = probe.Close()
	<-addresses
	if err := gate.renew(ctx, time.Now().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	expect("send", "ok")
	address := <-addresses
	time.Sleep(5 * time.Second)
	// Return packets on an established connection must also stop after expiry.
	if _, err := server.WriteToUDP([]byte("late"), address); err != nil {
		t.Fatal(err)
	}
	expect("read", "blocked")
	expect("send", "blocked")
	if err := gate.Remove(ctx); err != nil {
		t.Fatal(err)
	}
}

func hostGateUDPClient() {
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("100.64.0.8"), Port: UDPRelayFirst})
	if err != nil {
		fmt.Println("client-error")
		return
	}
	defer conn.Close()
	scanner := bufio.NewScanner(os.Stdin)
	buf := make([]byte, 64)
	for scanner.Scan() {
		_ = conn.SetDeadline(time.Now().Add(250 * time.Millisecond))
		if scanner.Text() == "send" {
			_, _ = conn.Write([]byte("echo"))
		}
		if _, err := conn.Read(buf); err != nil {
			fmt.Println("blocked")
		} else {
			fmt.Println("ok")
		}
	}
}
