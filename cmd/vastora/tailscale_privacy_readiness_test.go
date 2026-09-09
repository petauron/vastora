package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
)

func TestTailscaleIsolationWaitsForMapWithoutRestartingAgain(t *testing.T) {
	root := t.TempDir()
	hostsPath := filepath.Join(root, "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}, RelayRegionID: 999}
	mapPayload := "null"
	restarts := 0
	environment := tailscaleIsolationEnvironment{
		overridePath: filepath.Join(root, "privacy.conf"), hostsPath: hostsPath, derpCache: filepath.Join(root, "cache.json"),
		run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			switch name + " " + strings.Join(arguments, " ") {
			case "systemctl restart tailscaled.service":
				restarts++
			case "systemctl show --property=Environment --value tailscaled.service":
				return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
			case "tailscale debug derp-map":
				return []byte(mapPayload), nil
			}
			return nil, nil
		},
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); !errors.Is(err, errTailscaleDERPMapNotReady) {
			t.Fatalf("unready map should remain unverified: %v", err)
		}
		marker, err := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
		if err != nil || string(marker) != tailscalePrivacyVerifyingMarker || restarts != 1 {
			t.Fatalf("readiness retry restarted daemon: marker=%q restarts=%d err=%v", marker, restarts, err)
		}
	}
	mapPayload = `{"Regions":{"999":{"RegionID":999,"RegionCode":"vastora","Nodes":[{"HostName":"headscale.example.com","DERPPort":443}]}}}`
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
	if err != nil || string(marker) != tailscalePrivacyAppliedMarker || restarts != 1 {
		t.Fatalf("ready map did not finish existing startup: marker=%q restarts=%d err=%v", marker, restarts, err)
	}
	mapPayload = `{"Regions":{}}`
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); !errors.Is(err, errTailscaleDERPMapNotReady) || restarts != 1 {
		t.Fatalf("transient map loss restarted an applied daemon: restarts=%d err=%v", restarts, err)
	}
	mapPayload = `{"Regions":{"1":{"RegionID":1,"Nodes":[{"HostName":"derp.example.net"}]}}}`
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err == nil || errors.Is(err, errTailscaleDERPMapNotReady) {
		t.Fatalf("unexpected relay was treated as harmless startup: %v", err)
	}
}
