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

func TestTailscaleIsolationInspectionFailurePreservesCurrentState(t *testing.T) {
	for _, marker := range []string{tailscalePrivacyAppliedMarker, tailscalePrivacyVerifyingMarker} {
		for _, command := range []string{
			"systemctl is-active --quiet tailscaled.service",
			"systemctl show --property=Environment --value tailscaled.service",
			"tailscale debug derp-map",
			"invalid DERP map",
		} {
			t.Run(strings.TrimSpace(marker)+"/"+command, func(t *testing.T) {
				root := t.TempDir()
				desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}, RelayRegionID: 999}
				hosts, err := replaceTailscaleHostsSection("127.0.0.1 localhost\n", tailscaleControlHostnames(desired), desired.ControlAddresses)
				if err != nil {
					t.Fatal(err)
				}
				environment := tailscaleIsolationEnvironment{overridePath: filepath.Join(root, "privacy.conf"), hostsPath: filepath.Join(root, "hosts"), derpCache: filepath.Join(root, "cache.json")}
				files := map[string]string{
					environment.overridePath:                              tailscalePrivacyOverride,
					environment.hostsPath:                                 hosts,
					tailscalePrivacyAppliedPath(environment.overridePath): marker,
					filepath.Join(root, "tailscaled.state"):               "synthetic-node-identity",
				}
				for path, value := range files {
					if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				inspectionErr := errors.New("temporary inspection failure")
				fail := true
				environment.run = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
					current := name + " " + strings.Join(arguments, " ")
					if fail && current == command {
						return nil, inspectionErr
					}
					switch current {
					case "systemctl is-active --quiet tailscaled.service":
						return nil, nil
					case "systemctl show --property=ActiveState --value tailscaled.service":
						return []byte("active\n"), nil
					case "systemctl show --property=Environment --value tailscaled.service":
						return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
					case "tailscale debug derp-map":
						if fail && command == "invalid DERP map" {
							return []byte("{"), nil
						}
						return []byte(`{"Regions":{"999":{"RegionID":999,"RegionCode":"vastora","Nodes":[{"HostName":"headscale.example.com"}]}}}`), nil
					default:
						t.Fatalf("inspection failure attempted a mutation or control probe: %s", current)
						return nil, errors.New("unexpected command")
					}
				}
				err = reconcileTailscaleIsolation(context.Background(), desired, false, environment)
				if err == nil || errors.Is(err, errTailscaleIsolationDrift) || command != "invalid DERP map" && !errors.Is(err, inspectionErr) {
					t.Fatalf("inspection failure was not preserved: %v", err)
				}
				for path, want := range files {
					got, err := os.ReadFile(path)
					if err != nil || string(got) != want {
						t.Fatalf("inspection changed %s: %q, err=%v", filepath.Base(path), got, err)
					}
				}
				fail = false
				if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
					t.Fatalf("healthy inspection could not finish without restart: %v", err)
				}
				got, err := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
				if err != nil || string(got) != tailscalePrivacyAppliedMarker {
					t.Fatalf("healthy inspection marker = %q, err=%v", got, err)
				}
			})
		}
	}
}

func TestTailscaleIsolationResumesInspectionAfterSuccessfulRestart(t *testing.T) {
	root := t.TempDir()
	hostsPath := filepath.Join(root, "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}, RelayRegionID: 999}
	inspectionErr := errors.New("LocalAPI temporarily unavailable")
	fail := true
	restarts := 0
	environment := tailscaleIsolationEnvironment{
		overridePath: filepath.Join(root, "privacy.conf"), hostsPath: hostsPath, derpCache: filepath.Join(root, "missing-cache"),
		run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			switch name + " " + strings.Join(arguments, " ") {
			case "systemctl restart tailscaled.service":
				restarts++
			case "systemctl show --property=Environment --value tailscaled.service":
				return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
			case "tailscale debug derp-map":
				if fail {
					return nil, inspectionErr
				}
				return []byte(`{"Regions":{"999":{"RegionID":999,"RegionCode":"vastora","Nodes":[{"HostName":"headscale.example.com"}]}}}`), nil
			}
			return nil, nil
		},
	}
	for attempt := 0; attempt < 3; attempt++ {
		if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); !errors.Is(err, inspectionErr) {
			t.Fatalf("inspection error = %v", err)
		}
		marker, err := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
		if err != nil || string(marker) != tailscalePrivacyVerifyingMarker || restarts != 1 {
			t.Fatalf("verification retry restarted service: marker=%q restarts=%d err=%v", marker, restarts, err)
		}
	}
	fail = false
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil || restarts != 1 {
		t.Fatalf("recovered inspection restarted service: restarts=%d err=%v", restarts, err)
	}
}

func TestTailscaleServiceActiveRequiresConfirmedInactiveState(t *testing.T) {
	inspectionErr := errors.New("systemctl inspection failed")
	for _, state := range []string{"inactive", "failed", "active", "activating", "unknown", ""} {
		t.Run(state, func(t *testing.T) {
			run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
				switch name + " " + strings.Join(arguments, " ") {
				case "systemctl is-active --quiet tailscaled.service":
					return nil, inspectionErr
				case "systemctl show --property=ActiveState --value tailscaled.service":
					return []byte(state + "\n"), nil
				default:
					t.Fatal("unexpected command")
					return nil, nil
				}
			}
			active, err := tailscaleServiceActive(context.Background(), run)
			if active {
				t.Fatal("failed active inspection was accepted")
			}
			if state == "inactive" || state == "failed" {
				if err != nil {
					t.Fatalf("confirmed inactive state was rejected: %v", err)
				}
			} else if !errors.Is(err, inspectionErr) {
				t.Fatalf("unconfirmed state did not preserve inspection error: %v", err)
			}
		})
	}
}
