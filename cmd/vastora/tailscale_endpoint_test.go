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
		case "tailscale version --json", "tailscale version --json --daemon":
			return []byte(`{"short":"1.102.3","long":"1.102.3","daemonLong":"1.102.3"}`), nil
		case "tailscaled --version":
			return []byte("1.102.3\n  long version: 1.102.3\n"), nil
		case "tailscaled --help":
			return []byte("  -config string\n"), nil
		case "tailscale status --json":
			return []byte(`{"BackendState":"Running","Version":"1.102.3"}`), nil
		case "systemctl show --property=Environment --value tailscaled.service":
			return []byte("TS_NO_LOGS_NO_SUPPORT=true PORT=41641 FLAGS=--config=" + configPath + "\n"), nil
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
		case "tailscale version --json", "tailscale version --json --daemon":
			return []byte(`{"short":"1.104.0","long":"1.104.0","daemonLong":"1.104.0"}`), nil
		case "tailscaled --version":
			return []byte("1.104.0\n  long version: 1.104.0\n"), nil
		case "tailscaled --help":
			return []byte("  -config string\n"), nil
		case "tailscale status --json":
			statusChecks++
			if statusChecks == 1 {
				return []byte(`{"BackendState":"Stopped"}`), nil
			}
			return []byte(`{"BackendState":"Running","Version":"1.104.0"}`), nil
		case "systemctl show --property=Environment --value tailscaled.service":
			return []byte("TS_NO_LOGS_NO_SUPPORT=true PORT=41641 FLAGS=--config=" + configPath + "\n"), nil
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
		if command == "tailscale version --json" || command == "tailscale version --json --daemon" {
			return []byte(`{"short":"1.102.3","long":"1.102.3","daemonLong":"1.102.3"}`), nil
		}
		if command == "tailscaled --version" {
			return []byte("1.102.3\n  long version: 1.102.3\n"), nil
		}
		if command == "tailscaled --help" {
			return []byte("  -config string\n"), nil
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

func TestTailscaleEndpointReconcileCanRestartAnUnavailableDaemon(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "tailscaled.json")
	overridePath := filepath.Join(root, "endpoint.conf")
	config, err := tailscalehost.RenderConfig([]string{"203.0.113.40:41641"})
	if err != nil {
		t.Fatal(err)
	}
	override, err := renderTailscaleEndpointOverride(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{configPath: config, overridePath: override} {
		if err := applyHostFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restarts := 0
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		switch name + " " + strings.Join(arguments, " ") {
		case "tailscale version --json":
			return []byte(`{"short":"1.104.0","long":"1.104.0"}`), nil
		case "tailscale version --json --daemon":
			if restarts == 0 {
				return nil, errors.New("tailscaled socket unavailable")
			}
			return []byte(`{"short":"1.104.0","long":"1.104.0","daemonLong":"1.104.0"}`), nil
		case "tailscaled --version":
			return []byte("1.104.0\n  long version: 1.104.0\n"), nil
		case "tailscaled --help":
			return []byte("  -config string\n"), nil
		case "systemctl daemon-reload", "systemctl is-active --quiet tailscaled.service":
			return nil, nil
		case "systemctl restart tailscaled.service":
			restarts++
			return nil, nil
		case "tailscale status --json":
			return []byte(`{"BackendState":"Running","Version":"1.104.0"}`), nil
		case "systemctl show --property=Environment --value tailscaled.service":
			return []byte("TS_NO_LOGS_NO_SUPPORT=true PORT=41641 FLAGS=--config=" + configPath), nil
		case "ss -H -lun sport = :41641":
			return []byte("0.0.0.0:41641\n"), nil
		default:
			t.Fatalf("unexpected host mutation or command: %s %v", name, arguments)
			return nil, nil
		}
	}
	environment := tailscaleEndpointEnvironment{configPath: configPath, overridePath: overridePath, run: run}
	if err := reconcileTailscaleEndpoint(context.Background(), []string{"203.0.113.40:41641"}, environment); err != nil || restarts != 1 {
		t.Fatalf("stopped daemon was not repaired: restarts=%d err=%v", restarts, err)
	}
}

func TestTailscaleEndpointCompatibilityFailurePreservesFilesAndIdentity(t *testing.T) {
	for _, failure := range []string{"missing_config", "privacy_not_loaded", "daemon_below_minimum"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			configPath := filepath.Join(root, "tailscaled.json")
			overridePath := filepath.Join(root, "endpoint.conf")
			identityPath := filepath.Join(root, "tailscaled.state")
			for path, content := range map[string]string{configPath: "previous config", overridePath: "previous override", identityPath: "existing node identity"} {
				if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			restarts := 0
			run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
				switch name + " " + strings.Join(arguments, " ") {
				case "tailscale version --json":
					return []byte(`{"short":"1.104.0","long":"1.104.0"}`), nil
				case "tailscale version --json --daemon":
					if failure == "daemon_below_minimum" {
						return []byte(`{"short":"1.104.0","long":"1.104.0","daemonLong":"1.102.2"}`), nil
					}
					return []byte(`{"short":"1.104.0","long":"1.104.0","daemonLong":"1.104.0"}`), nil
				case "tailscaled --version":
					return []byte("1.104.0\n  long version: 1.104.0\n"), nil
				case "tailscaled --help":
					if failure == "missing_config" {
						return []byte("Usage: tailscaled\n"), nil
					}
					return []byte("  -config string\n"), nil
				case "systemctl daemon-reload", "systemctl is-active --quiet tailscaled.service":
					return nil, nil
				case "systemctl restart tailscaled.service":
					restarts++
					return nil, nil
				case "tailscale status --json":
					return []byte(`{"BackendState":"Running","Version":"1.104.0"}`), nil
				case "systemctl show --property=Environment --value tailscaled.service":
					return []byte("TS_NO_LOGS_NO_SUPPORT=false\n"), nil
				default:
					t.Fatalf("unexpected host mutation or command: %s %v", name, arguments)
					return nil, nil
				}
			}
			environment := tailscaleEndpointEnvironment{configPath: configPath, overridePath: overridePath, run: run}
			if err := reconcileTailscaleEndpoint(context.Background(), []string{"203.0.113.40:41641"}, environment); err == nil {
				t.Fatal("incompatible daemon was accepted")
			}
			wantRestarts := 2
			if failure == "missing_config" {
				wantRestarts = 0
			}
			if restarts != wantRestarts {
				t.Fatalf("unexpected restarts: got=%d want=%d", restarts, wantRestarts)
			}
			for path, want := range map[string]string{configPath: "previous config", overridePath: "previous override", identityPath: "existing node identity"} {
				content, err := os.ReadFile(path)
				if err != nil || string(content) != want {
					t.Fatalf("previous state was not preserved at %s: content=%q err=%v", path, content, err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("previous permissions were not preserved at %s: %v", path, err)
				}
			}
		})
	}
}
