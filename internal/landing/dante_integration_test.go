//go:build linux && integration

package landing

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// Exercises the shipped daemon, not a SOCKS mock. Only the fixture maps
// addresses to loopback; separate kernel tests enforce production egress rules.
func TestNativeDanteTransfersTCPAndUDP(t *testing.T) {
	binaryPath := os.Getenv("VASTORA_DANTE_TEST_BINARY")
	if !filepath.IsAbs(binaryPath) {
		t.Fatal("release-built Dante executable required")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := reservation.Addr().String()
	_, port, _ := net.SplitHostPort(endpoint)
	reservation.Close()
	config, err := testServerPlan().RenderDante("10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	fixture := string(config)
	for _, cidr := range blockedIPv4 {
		fixture = strings.ReplaceAll(fixture, "to: "+cidr+"\n", "to: 192.0.2.0/24\n")
	}
	fixture = strings.NewReplacer("100.64.0.8", "127.0.0.1", "100.64.0.9", "127.0.0.1", "10.0.0.2", "127.0.0.1", "vastora-landing", account.Username, "port = 1080\n", "port = "+port+"\n").Replace(fixture)
	directory := t.TempDir()
	path := filepath.Join(directory, "danted.conf")
	if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binaryPath, "-f", path, "-p", filepath.Join(directory, "pid"))
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL); _ = command.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	var control net.Conn
	for time.Now().Before(deadline) {
		control, err = net.DialTimeout("tcp4", endpoint, 100*time.Millisecond)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal("Dante did not start", err)
	}
	defer control.Close()
	control.SetDeadline(deadline)
	if err := negotiateSOCKS(control); err != nil {
		t.Fatal(err)
	}
	if err := writeSOCKS(control, []byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	var reply [10]byte
	if _, err := io.ReadFull(control, reply[:]); err != nil || reply[1] != 0 || reply[3] != 1 {
		t.Fatalf("UDP association failed: %v %v", reply, err)
	}
	relayPort := int(binary.BigEndian.Uint16(reply[8:]))
	if !bytes.Equal(reply[4:8], []byte{127, 0, 0, 1}) || relayPort < UDPRelayFirst || relayPort > UDPRelayLast {
		t.Fatal("unexpected UDP relay", reply)
	}
	echo, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	echo.SetDeadline(deadline)
	go func() {
		data := make([]byte, 256)
		n, address, err := echo.ReadFrom(data)
		if err == nil {
			_, _ = echo.WriteTo(data[:n], address)
		}
	}()
	udp, err := net.Dial("udp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(relayPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	udp.SetDeadline(deadline)
	packet := []byte{0, 0, 0, 1, 127, 0, 0, 1, 0, 0}
	binary.BigEndian.PutUint16(packet[8:], uint16(echo.LocalAddr().(*net.UDPAddr).Port))
	packet = append(packet, []byte("landing-udp")...)
	if _, err := udp.Write(packet); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 256)
	n, err := udp.Read(buffer)
	if err != nil || !bytes.Equal(buffer[:n], packet) {
		t.Fatal("Dante UDP roundtrip failed", err)
	}

	tcpEcho, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpEcho.Close()
	go func() {
		c, err := tcpEcho.Accept()
		if err == nil {
			defer c.Close()
			c.SetDeadline(deadline)
			_, _ = io.Copy(c, c)
		}
	}()
	dialer, err := proxy.SOCKS5("tcp", endpoint, nil, &net.Dialer{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := dialer.Dial("tcp", tcpEcho.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	tcp.SetDeadline(deadline)
	if _, err := tcp.Write([]byte("landing-tcp")); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, len("landing-tcp"))
	if _, err := io.ReadFull(tcp, answer); err != nil || string(answer) != "landing-tcp" {
		t.Fatal("Dante TCP roundtrip failed", err)
	}
}
