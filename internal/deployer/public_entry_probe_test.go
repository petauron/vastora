package deployer

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

func TestPublicEntryProbeServesOneTimeChallengeOnEveryPort(t *testing.T) {
	service := NewPublicEntryProbeService()
	service.ports = []int{0, 0}
	service.lifetime = 5 * time.Second
	probe, err := service.StartPublicEntryProbe(context.Background(), deployapi.PublicEntryProbeRequest{BindAddress: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(probe.Ports) != 2 || probe.Ports[0] == probe.Ports[1] {
		t.Fatalf("unexpected probe ports: %#v", probe.Ports)
	}
	wrong, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", probe.Ports[0]), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(wrong, "VASTORA-PROBE/1 wrong-challenge"); err != nil {
		t.Fatal(err)
	}
	_ = wrong.SetReadDeadline(time.Now().Add(time.Second))
	if response, _ := bufio.NewReader(wrong).ReadString('\n'); response != "" {
		t.Fatalf("probe answered the wrong challenge: %q", response)
	}
	_ = wrong.Close()
	for _, port := range probe.Ports {
		connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(connection, "VASTORA-PROBE/1 %s\n", probe.Challenge); err != nil {
			t.Fatal(err)
		}
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		response, err := bufio.NewReader(connection).ReadString('\n')
		_ = connection.Close()
		if err != nil || strings.TrimSpace(response) != "VASTORA-OK/1 "+probe.Challenge {
			t.Fatalf("unexpected challenge response on %d: %q err=%v", port, response, err)
		}
	}
	if err := service.StopPublicEntryProbe(context.Background(), probe.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", probe.Ports[0]), 100*time.Millisecond); err == nil {
		t.Fatal("probe listener stayed open after stop")
	}
}

func TestPublicEntryProbeRejectsConcurrentRuns(t *testing.T) {
	service := NewPublicEntryProbeService()
	service.ports = []int{0, 0}
	service.lifetime = 5 * time.Second
	probe, err := service.StartPublicEntryProbe(context.Background(), deployapi.PublicEntryProbeRequest{BindAddress: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer service.StopPublicEntryProbe(context.Background(), probe.ID)
	if _, err := service.StartPublicEntryProbe(context.Background(), deployapi.PublicEntryProbeRequest{BindAddress: "127.0.0.1"}); err == nil {
		t.Fatal("concurrent public entry probe was accepted")
	}
}
