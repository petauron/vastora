package gatewayruntime

import (
	"net/netip"
	"testing"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/gateway"
)

func TestDockerPortsGivesSharedPublicTCP443ToLayer4(t *testing.T) {
	state := gateway.DesiredState{
		Revision: 1,
		Listeners: []gateway.Listener{
			{Kind: "public", Address: "203.0.113.10", HTTPPort: 80, HTTPSPort: 443},
			{Kind: "headscale", Address: "100.64.0.1", HTTPPort: 80, HTTPSPort: 443},
		},
		SharedHTTPS: &gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, CaddyAddress: CaddyContainer, CaddyPort: 443},
	}
	_, bindings, err := DockerPorts(state)
	if err != nil {
		t.Fatal(err)
	}
	if values := bindings[dockernetwork.MustParsePort("443/tcp")]; len(values) != 0 {
		t.Fatalf("Caddy still owns shared public TCP 443: %#v", values)
	}
	if values := bindings[dockernetwork.MustParsePort("443/udp")]; len(values) != 1 || values[0].HostIP != netip.MustParseAddr("203.0.113.10") {
		t.Fatalf("Caddy lost public HTTP/3: %#v", values)
	}
	if values := bindings[dockernetwork.MustParsePort("10443/tcp")]; len(values) != 1 || values[0].HostIP != netip.MustParseAddr("100.64.0.1") {
		t.Fatalf("private HTTPS binding is wrong: %#v", values)
	}
}
