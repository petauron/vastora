//go:build linux && integration

package landing

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Use the same digest-pinned 3x-ui image as the catalog, not a separately
// downloaded latest Xray. The container has no network, mounts or credentials.
// This validates real core configuration parsing; it is not an egress drill.
func TestLandingRoutesAcceptedByCatalogXray(t *testing.T) {
	image := os.Getenv("VASTORA_LANDING_XRAY_IMAGE")
	if !strings.HasPrefix(image, "ghcr.io/mhsanaei/3x-ui:") || !strings.Contains(image, "@sha256:") {
		t.Fatal("catalog digest-pinned 3x-ui image required")
	}
	// Keep the fixture independent of external geodata files. Production rules
	// remain untouched by PrepareRouteChange.
	raw := strings.ReplaceAll(routeFixture, "geoip:private", "10.0.0.0/8")
	raw = strings.Replace(raw, `"api":{"tag":"api"}`, `"api":{"tag":"api","services":["HandlerService","StatsService"]}`, 1)
	first := fixtureRouteChange(t, raw)
	second, err := PrepareRouteReplacement(first, first.After, 8, []string{"business"}, PeerIdentity{ID: "exit-b", PublicKey: "key-b", Address: "100.64.0.9"})
	if err != nil {
		t.Fatal(err)
	}
	restored, _, err := second.NextWrite(second.After, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		raw   json.RawMessage
		valid bool
	}{
		{"original", first.Before, true},
		{"enabled", first.After, true},
		{"switched", second.After, true},
		{"restored", restored, true},
		{"invalid_core_protocol", json.RawMessage(strings.Replace(string(first.After), `"protocol":"socks"`, `"protocol":"not-a-protocol"`, 1)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "docker", "run", "--rm", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--entrypoint=/app/bin/xray-linux-amd64", "-i", image, "run", "-test", "-format", "json", "-config", "stdin:")
			command.Stdin = bytes.NewReader(tc.raw)
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("Xray configuration validation timed out")
			}
			if tc.valid {
				if err != nil || !bytes.Contains(output, []byte("Configuration OK.")) {
					t.Fatalf("catalog Xray rejected %s: %v\n%s", tc.name, err, output)
				}
			} else if err == nil || !bytes.Contains(output, []byte("not-a-protocol")) {
				t.Fatalf("invalid configuration did not reach the real validator: %v\n%s", err, output)
			}
		})
	}
}
