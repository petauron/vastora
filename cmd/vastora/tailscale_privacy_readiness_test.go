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
	controlPath := filepath.Join(root, "agent-control.json")
	if err := os.WriteFile(controlPath, []byte(`{"ipv6Address":"2001:db8::20"}`), 0600); err != nil {
		t.Fatal(err)
	}
	mapPayload := "null"
	restarts := 0
	probes := 0
	environment := tailscaleIsolationEnvironment{
		controlIPv6Path: controlPath,
		overridePath:    filepath.Join(root, "privacy.conf"), hostsPath: hostsPath, derpCache: filepath.Join(root, "cache.json"),
		run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			if name == "curl" {
				request := strings.Join(arguments, " ")
				if !strings.Contains(request, "--resolve headscale.example.com:443:[2001:db8::20]") || strings.Contains(request, "203.0.113.10") || strings.Contains(request, "--insecure") || arguments[len(arguments)-1] != desired.ControlURL+"/" {
					t.Fatal("IPv6 selection changed origin or TLS verification", request)
				}
				probes++
			}
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
	if probes != 1 {
		t.Fatal("IPv6 control preflight missing or repeated", probes)
	}
	hosts, err := os.ReadFile(hostsPath)
	if err != nil || !strings.Contains(string(hosts), "2001:db8::20 headscale.example.com") || strings.Contains(string(hosts), "203.0.113.10") {
		t.Fatal("selected IPv6 not retained for reconnect", err)
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

func TestTailscaleControlIPv6PreflightFailurePreservesHostState(t *testing.T) {
	root := t.TempDir()
	hosts := filepath.Join(root, "hosts")
	original := "127.0.0.1 localhost\n"
	if err := os.WriteFile(hosts, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	control := filepath.Join(root, "control.json")
	if err := os.WriteFile(control, []byte(`{"ipv6Address":"2001:db8::20"}`), 0600); err != nil {
		t.Fatal(err)
	}
	environment := tailscaleIsolationEnvironment{controlIPv6Path: control, overridePath: filepath.Join(root, "privacy.conf"), hostsPath: hosts, derpCache: filepath.Join(root, "cache.json"), run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "curl" {
			t.Fatal("failed endpoint verification reached a host mutation", name)
		}
		return []byte("synthetic TLS verification failure"), errors.New("verification failed")
	}}
	desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}, RelayRegionID: 999}
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err == nil {
		t.Fatal("failed preflight accepted")
	}
	if got, err := os.ReadFile(hosts); err != nil || string(got) != original {
		t.Fatal("failed verification changed hosts", err)
	}
	if _, err := os.Stat(tailscalePrivacyAppliedPath(environment.overridePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed verification wrote an applied marker", err)
	}
}
