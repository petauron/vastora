package deployer

import (
	"testing"

	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
)

func TestCenterRemoteAccessContainerIsIsolatedOnThePrivateBridge(t *testing.T) {
	options := centerRemoteAccessCreateOptions("cloudflare-tunnel-token-value")
	if options.Name != centerRemoteAccessContainer || options.Config.Image != deployapi.CloudflaredImage {
		t.Fatalf("unexpected Center cloudflared identity: %#v", options)
	}
	if string(options.HostConfig.NetworkMode) != dockerruntime.NetworkName || !options.HostConfig.ReadonlyRootfs {
		t.Fatalf("Center cloudflared is not isolated on the private bridge: %#v", options.HostConfig)
	}
	if len(options.HostConfig.PortBindings) != 0 || len(options.HostConfig.CapDrop) != 1 || options.HostConfig.CapDrop[0] != "ALL" {
		t.Fatalf("Center cloudflared exposes unnecessary host privileges: %#v", options.HostConfig)
	}
	if options.NetworkingConfig.EndpointsConfig[dockerruntime.NetworkName] == nil {
		t.Fatal("Center cloudflared has no private bridge endpoint")
	}
}
