package deployer

import (
	"bufio"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/dockerruntime"
)

func TestPublicEntryProbeContainerUsesBridgeAndExplicitHostPorts(t *testing.T) {
	expiresAt := time.Now().Add(time.Minute).UTC()
	options := publicEntryProbeCreateOptions("vastora:test", netip.MustParseAddr("10.0.0.157"), []int{80, 443}, strings.Repeat("a", 43), expiresAt)
	if options.HostConfig.NetworkMode != dockerruntime.NetworkName {
		t.Fatalf("probe network mode = %q", options.HostConfig.NetworkMode)
	}
	for _, number := range []string{"80/tcp", "443/tcp"} {
		port := dockernetwork.MustParsePort(number)
		bindings := options.HostConfig.PortBindings[port]
		if len(bindings) != 1 || bindings[0].HostIP != netip.MustParseAddr("10.0.0.157") || bindings[0].HostPort != strings.TrimSuffix(number, "/tcp") {
			t.Fatalf("probe binding %s = %#v", number, bindings)
		}
	}
	if strings.Join(options.Config.Cmd, " ") == "" || !strings.Contains(strings.Join(options.Config.Cmd, " "), "public-entry-probe") {
		t.Fatalf("probe command is missing: %#v", options.Config.Cmd)
	}
}

func TestPublicEntryProbeServesOnlyTheMatchingChallenge(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	challenge := strings.Repeat("b", 43)
	go servePublicEntryChallenge(listener, challenge, time.Now().Add(5*time.Second))

	request := func(value string) string {
		connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		_, _ = fmt.Fprintf(connection, "VASTORA-PROBE/1 %s\n", value)
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		response, _ := bufio.NewReader(connection).ReadString('\n')
		return strings.TrimSpace(response)
	}
	if response := request("wrong"); response != "" {
		t.Fatalf("probe answered a wrong challenge: %q", response)
	}
	if response := request(challenge); response != "VASTORA-OK/1 "+challenge {
		t.Fatalf("unexpected challenge response: %q", response)
	}
}
