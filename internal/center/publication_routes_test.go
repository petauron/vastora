package center

import (
	"testing"

	"github.com/petauron/vastora/internal/dockerruntime"
)

func TestCanonicalGatewayServiceEndpointUsesDockerDNSForColocatedThreeXUI(t *testing.T) {
	fallback := "100.64.0.1:2053"
	endpoint := canonicalGatewayServiceEndpoint(threeXUIAppKey, "docker", "node-a", "node-a", 2053, fallback)
	want := dockerruntime.ThreeXUIAlias + ":2053"
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}

	for name, value := range map[string]string{
		"cross-node":   canonicalGatewayServiceEndpoint(threeXUIAppKey, "docker", "node-a", "node-b", 2053, fallback),
		"host-runtime": canonicalGatewayServiceEndpoint(threeXUIAppKey, "host", "node-a", "node-a", 2053, fallback),
		"other-app":    canonicalGatewayServiceEndpoint("vastora-official/cpa", "docker", "node-a", "node-a", 8317, fallback),
	} {
		if value != fallback {
			t.Fatalf("%s endpoint = %q, want fallback %q", name, value, fallback)
		}
	}
}
