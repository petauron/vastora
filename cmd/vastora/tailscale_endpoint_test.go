package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/tailscalehost"
)

func TestTailscaleEndpointReconcileIsIdempotentAndRemovesDisabledState(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "etc", "vastora", "tailscaled.json")
	overridePath := filepath.Join(root, "systemd", "tailscaled.service.d", "91-vastora-endpoint.conf")
	commands := []string{}
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		command := name + " " + strings.Join(arguments, " ")
		commands = append(commands, command)
		switch command {
		case "tailscale version":
			return []byte("1.102.3\n"), nil
		case "tailscale status --json":
			return []byte(`{"BackendState":"Running"}`), nil
		case "systemctl show --property=Environment --value tailscaled.service":
			return []byte("PORT=41641 FLAGS=--config=" + configPath + "\n"), nil
		case "ss -H -lun sport = :41641":
			return []byte("0.0.0.0:41641\n"), nil
		default:
			return nil, nil
		}
	}
	environment := tailscaleEndpointEnvironment{configPath: configPath, overridePath: overridePath, run: run}
	if err := reconcileTailscaleEndpoint(context.Background(), []string{"203.0.113.10:41641"}, environment); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTailscaleEndpoint(context.Background(), []string{"203.0.113.10:41641"}, environment); err != nil {
		t.Fatal(err)
	}
	config, _ := os.ReadFile(configPath)
	override, _ := os.ReadFile(overridePath)
	if string(config) != "{\"version\":\"alpha0\",\"locked\":false,\"staticEndpoints\":[\"203.0.113.10:41641\"]}\n" || !strings.Contains(string(override), "PORT=41641") || !strings.Contains(string(override), configPath) {
		t.Fatalf("unexpected managed endpoint files: config=%q override=%q", config, override)
	}
	if strings.Count(strings.Join(commands, "\n"), "systemctl restart tailscaled.service") != 1 {
		t.Fatalf("endpoint reconciliation was not idempotent: %v", commands)
	}
	if err := reconcileTailscaleEndpoint(context.Background(), nil, environment); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{configPath, overridePath} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("disabled endpoint state remained at %s: %v", path, err)
		}
	}
}

func TestTailscaleEndpointReconcileRepairsRuntimeDriftWithCurrentFiles(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "etc", "vastora", "tailscaled.json")
	overridePath := filepath.Join(root, "systemd", "tailscaled.service.d", "91-vastora-endpoint.conf")
	config, err := tailscalehost.RenderConfig([]string{"203.0.113.30:41641"})
	if err != nil {
		t.Fatal(err)
	}
	override, err := renderTailscaleEndpointOverride(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyHostFile(configPath, config, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyHostFile(overridePath, override, 0o644); err != nil {
		t.Fatal(err)
	}
	commands := []string{}
	statusChecks := 0
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		command := name + " " + strings.Join(arguments, " ")
		commands = append(commands, command)
		switch command {
		case "tailscale version":
			return []byte("1.102.3\n"), nil
		case "tailscale status --json":
			statusChecks++
			if statusChecks == 1 {
				return []byte(`{"BackendState":"Stopped"}`), nil
			}
			return []byte(`{"BackendState":"Running"}`), nil
		case "systemctl show --property=Environment --value tailscaled.service":
			return []byte("PORT=41641 FLAGS=--config=" + configPath + "\n"), nil
		case "ss -H -lun sport = :41641":
			return []byte("0.0.0.0:41641\n"), nil
		default:
			return nil, nil
		}
	}
	environment := tailscaleEndpointEnvironment{configPath: configPath, overridePath: overridePath, run: run}
	if err := reconcileTailscaleEndpoint(context.Background(), []string{"203.0.113.30:41641"}, environment); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.Join(commands, "\n"), "systemctl restart tailscaled.service") != 1 || statusChecks != 2 {
		t.Fatalf("runtime drift was not repaired: %v", commands)
	}
}

func TestTailscaleEndpointReconcileRollsBackFailedRestart(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "etc", "vastora", "tailscaled.json")
	overridePath := filepath.Join(root, "systemd", "tailscaled.service.d", "91-vastora-endpoint.conf")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(overridePath), 0o755); err != nil {
		t.Fatal(err)
	}
	oldConfig := []byte("old config\n")
	oldOverride := []byte("old override\n")
	if err := os.WriteFile(configPath, oldConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overridePath, oldOverride, 0o640); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		command := name + " " + strings.Join(arguments, " ")
		if command == "tailscale version" {
			return []byte("1.102.3\n"), nil
		}
		if command == "systemctl restart tailscaled.service" {
			restarts++
			if restarts == 1 {
				return []byte("bad config"), errors.New("restart failed")
			}
		}
		return nil, nil
	}
	environment := tailscaleEndpointEnvironment{configPath: configPath, overridePath: overridePath, run: run}
	if err := reconcileTailscaleEndpoint(context.Background(), []string{"203.0.113.20:41641"}, environment); err == nil {
		t.Fatal("failed restart was accepted")
	}
	config, _ := os.ReadFile(configPath)
	override, _ := os.ReadFile(overridePath)
	configInfo, _ := os.Stat(configPath)
	overrideInfo, _ := os.Stat(overridePath)
	if string(config) != string(oldConfig) || string(override) != string(oldOverride) || configInfo.Mode().Perm() != 0o600 || overrideInfo.Mode().Perm() != 0o640 || restarts != 2 {
		t.Fatalf("working state was not restored: config=%q override=%q modes=%o/%o restarts=%d", config, override, configInfo.Mode().Perm(), overrideInfo.Mode().Perm(), restarts)
	}
}
