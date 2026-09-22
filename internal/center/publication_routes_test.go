package center

import (
	"testing"

	"github.com/petauron/vastora/internal/dockerruntime"
)

func TestCanonicalGatewayServiceEndpointUsesDockerDNSForColocatedThreeXUI(t *testing.T) {
	fallback := "100.64.0.1:2053"
	endpoint := canonicalGatewayServiceEndpoint(threeXUIAppKey, "docker", threeXUIRoleMaster, "node-a", "node-a", "panel", 2053, fallback)
	want := dockerruntime.ThreeXUIAlias + ":2053"
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}

	for name, value := range map[string]string{
		"cross-node":   canonicalGatewayServiceEndpoint(threeXUIAppKey, "docker", threeXUIRoleMaster, "node-a", "node-b", "panel", 2053, fallback),
		"host-runtime": canonicalGatewayServiceEndpoint(threeXUIAppKey, "host", threeXUIRoleMaster, "node-a", "node-a", "panel", 2053, fallback),
		"other-app":    canonicalGatewayServiceEndpoint("vastora-official/cpa", "docker", "", "node-a", "node-a", "manager", 8317, fallback),
	} {
		if value != fallback {
			t.Fatalf("%s endpoint = %q, want fallback %q", name, value, fallback)
		}
	}
}

func TestCanonicalGatewayServiceEndpointUsesVastoraXrayForWorker(t *testing.T) {
	fallback := "100.64.0.1:443"
	endpoint := canonicalGatewayServiceEndpoint(threeXUIAppKey, "docker", threeXUIRoleWorker, "node-a", "node-a", "inbound", 443, fallback)
	if want := dockerruntime.LegacyXrayAlias + ":443"; endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
}

func TestCanonicalGatewayServiceEndpointKeepsCenterSubscriptionOriginBeforeApplicationConversion(t *testing.T) {
	endpoint := dockerruntime.CenterAlias + ":8080"
	for _, appKey := range []string{threeXUIAppKey, meridianAppKey} {
		if got := canonicalGatewayServiceEndpoint(appKey, "docker", threeXUIRoleMaster, "node-a", "node-a", meridianSubscriptionServiceName, 8080, endpoint); got != endpoint {
			t.Fatalf("%s subscription endpoint = %q, want %q", appKey, got, endpoint)
		}
	}
}
