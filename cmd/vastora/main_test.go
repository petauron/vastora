package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
)

func TestLocalCenterStatusReportsVersionAndHealth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			t.Fatalf("unexpected health path %q", request.URL.Path)
		}
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	installDir := t.TempDir()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	for name, content := range map[string]string{
		".env":         "VASTORA_CENTER_BOOTSTRAP_PORT=" + port + "\n",
		"compose.yaml": "name: vastora\n",
		"release.env":  "VASTORA_VERSION=0.1.0-test\n",
	} {
		if err := os.WriteFile(filepath.Join(installDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := reportLocalStatus(installDir, filepath.Join(t.TempDir(), "agent"), &output, server.Client()); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Center: running", "Version: 0.1.0-test", "Local address: " + server.URL} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("status is missing %q:\n%s", expected, output.String())
		}
	}
}

func TestLocalAgentUninstallUsesSimpleMenuAndDestructiveConfirmation(t *testing.T) {
	var output bytes.Buffer
	deleteData, cancelled, err := chooseLocalAgentUninstall(strings.NewReader("2\nDELETE\n"), &output)
	if err != nil || !deleteData || cancelled {
		t.Fatalf("delete-data selection = delete=%v cancelled=%v err=%v", deleteData, cancelled, err)
	}
	if !strings.Contains(output.String(), "keep application data") || !strings.Contains(output.String(), "Type DELETE") {
		t.Fatalf("uninstall menu was not explicit: %q", output.String())
	}
	deleteData, cancelled, err = chooseLocalAgentUninstall(strings.NewReader("2\nno\n"), &output)
	if err != nil || deleteData || !cancelled {
		t.Fatalf("mismatched destructive confirmation was accepted: delete=%v cancelled=%v err=%v", deleteData, cancelled, err)
	}
}

func TestAgentUninstallRemovesOnlyVastoraManagedTailscale(t *testing.T) {
	for _, ownership := range []string{"external", "managed"} {
		t.Run(ownership, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "agent")
			if err := os.MkdirAll(dataDir, 0o700); err != nil {
				t.Fatal(err)
			}
			state := "HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=" + ownership + "\nTAILSCALE_ENROLLED=1\n"
			if err := os.WriteFile(filepath.Join(dataDir, agent.HostInstallStateName), []byte(state), 0o600); err != nil {
				t.Fatal(err)
			}
			unitPath := filepath.Join(root, "vastora-agent.service")
			if err := os.WriteFile(unitPath, []byte("Description=Vastora Agent\nExecStart=/usr/local/bin/vastora agent serve --data-dir /var/lib/vastora/agent\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			binaryPath := filepath.Join(root, "vastora")
			if err := os.WriteFile(binaryPath, []byte("managed"), 0o755); err != nil {
				t.Fatal(err)
			}
			tailscalePaths := []string{filepath.Join(root, "tailscale.list"), filepath.Join(root, "tailscale.key"), filepath.Join(root, "tailscale-state")}
			for _, path := range tailscalePaths {
				if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			tailscalePrivacyPath := filepath.Join(root, "tailscaled.service.d", "90-vastora-privacy.conf")
			tailscaleEndpointPaths := []string{
				filepath.Join(root, "etc", "vastora", "tailscaled.json"),
				filepath.Join(root, "tailscaled.service.d", "91-vastora-endpoint.conf"),
			}
			tailscaleHostsPath := filepath.Join(root, "hosts")
			if err := os.MkdirAll(filepath.Dir(tailscalePrivacyPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tailscalePrivacyPath, []byte("[Service]\nEnvironment=TS_NO_LOGS_NO_SUPPORT=true\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(tailscalePrivacyAppliedPath(tailscalePrivacyPath), []byte(tailscalePrivacyAppliedMarker), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, path := range tailscaleEndpointPaths {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("managed endpoint"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(tailscaleHostsPath, []byte("127.0.0.1 localhost\n"+tailscaleHostsBeginMarker+"\n203.0.113.10 headscale.example.com\n"+tailscaleHostsEndMarker+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			commands := []string{}
			environment := agentUninstallEnvironment{
				dataDir: dataDir, unitPath: unitPath, binaryPaths: []string{binaryPath}, tailscalePaths: tailscalePaths, tailscalePrivacyPath: tailscalePrivacyPath, tailscaleEndpointPaths: tailscaleEndpointPaths, tailscaleHostsPath: tailscaleHostsPath,
				purgeRuntime: func(context.Context, bool) error { return nil },
				run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
					commands = append(commands, name+" "+strings.Join(arguments, " "))
					return nil, nil
				},
			}
			if err := uninstallAgentHostWithEnvironment(context.Background(), true, true, false, environment); err != nil {
				t.Fatal(err)
			}
			if err := uninstallAgentHostWithEnvironment(context.Background(), true, true, false, environment); err != nil {
				t.Fatalf("repeated uninstall was not idempotent: %v", err)
			}
			joined := strings.Join(commands, "\n")
			if !strings.Contains(joined, "tailscale logout") {
				t.Fatalf("Headscale identity was not disconnected: %s", joined)
			}
			for _, path := range tailscalePaths {
				_, err := os.Stat(path)
				if ownership == "managed" && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("managed Tailscale state remained at %s", path)
				}
				if ownership == "external" && err != nil {
					t.Fatalf("external Tailscale state was removed at %s: %v", path, err)
				}
			}
			for _, path := range tailscaleEndpointPaths {
				_, err := os.Stat(path)
				if ownership == "managed" && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("managed Tailscale endpoint state remained at %s", path)
				}
				if ownership == "external" && err != nil {
					t.Fatalf("external Tailscale endpoint state was removed at %s: %v", path, err)
				}
			}
			if got := strings.Contains(joined, "apt-get purge"); got != (ownership == "managed") {
				t.Fatalf("Tailscale package cleanup for %s = %v; commands:\n%s", ownership, got, joined)
			}
			for _, path := range []string{tailscalePrivacyPath, tailscalePrivacyAppliedPath(tailscalePrivacyPath)} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Vastora Tailscale privacy state remained at %s: %v", path, err)
				}
			}
			hosts, err := os.ReadFile(tailscaleHostsPath)
			if err != nil || string(hosts) != "127.0.0.1 localhost\n" {
				t.Fatalf("Vastora resolver pin was not removed cleanly: %q err=%v", hosts, err)
			}
		})
	}
}

func TestNodeRolesAndCapabilitiesAreIndependent(t *testing.T) {
	roles, err := parseNodeRoles("worker,gateway")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := parseNodeCapabilities("docker")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 || !containsValue(roles, "gateway") || !capabilities.Docker || capabilities.Gateway {
		t.Fatalf("roles and capabilities were not parsed independently: roles=%v capabilities=%#v", roles, capabilities)
	}
}

func TestUnimplementedCapabilitiesCannotBeAdvertised(t *testing.T) {
	for _, capability := range []string{"metrics", "logs"} {
		if _, err := parseNodeCapabilities(capability); err == nil {
			t.Fatalf("unimplemented capability %q was accepted", capability)
		}
	}
}

func TestCenterBootstrapDoesNotRequireSuggestedAgentURL(t *testing.T) {
	missingCatalog := filepath.Join(t.TempDir(), "missing-catalog.json")
	err := runCenter([]string{"serve", "--data-dir", t.TempDir(), "--official-catalog", missingCatalog})
	if err == nil || !strings.Contains(err.Error(), "read official catalog") {
		t.Fatalf("Center did not reach startup without --agent-connect-url: %v", err)
	}
}

func TestSystemdAgentUnitUsesPersistentServiceConfiguration(t *testing.T) {
	unit := systemdAgentUnit("/usr/local/bin/vastora", "/var/lib/vastora/agent", "worker,gateway", "docker,gateway,tunnel")
	for _, expected := range []string{
		"Description=Vastora Agent",
		`ExecStart="/usr/local/bin/vastora" agent serve --data-dir "/var/lib/vastora/agent" --roles "worker,gateway" --capabilities "docker,gateway,tunnel"`,
		"Restart=always",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("systemd unit is missing %q:\n%s", expected, unit)
		}
	}
}

func TestTailscaleIsolationIsIdempotentAndPreservesIdentity(t *testing.T) {
	root := t.TempDir()
	overridePath := filepath.Join(root, "tailscaled.service.d", "90-vastora-privacy.conf")
	hostsPath := filepath.Join(root, "hosts")
	cachePath := filepath.Join(root, "tailscale", "derpmap.cached.json")
	statePath := filepath.Join(root, "tailscale", "tailscaled.state")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte(`{"Regions":{"2":{"Nodes":[{"HostName":"derp2.tailscale.com"}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("node-identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	commands := []string{}
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(arguments, " "))
		if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
			return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
		}
		return nil, nil
	}
	desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}, ControlAliases: []string{"https://headscale.old.example.com"}}
	environment := tailscaleIsolationEnvironment{overridePath: overridePath, hostsPath: hostsPath, derpCache: cachePath, run: run}
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
		t.Fatal(err)
	}
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(overridePath)
	if err != nil || string(raw) != tailscalePrivacyOverride {
		t.Fatalf("Tailscale privacy override = %q, err=%v", raw, err)
	}
	applied, err := os.ReadFile(tailscalePrivacyAppliedPath(overridePath))
	if err != nil || string(applied) != tailscalePrivacyAppliedMarker {
		t.Fatalf("Tailscale privacy marker = %q, err=%v", applied, err)
	}
	if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("official DERP cache was not removed: %v", err)
	}
	identity, err := os.ReadFile(statePath)
	if err != nil || string(identity) != "node-identity" {
		t.Fatalf("Tailscale identity was changed: %q err=%v", identity, err)
	}
	hosts, err := os.ReadFile(hostsPath)
	if err != nil || !strings.Contains(string(hosts), "203.0.113.10 headscale.example.com headscale.old.example.com") {
		t.Fatalf("Headscale resolver pin = %q, err=%v", hosts, err)
	}
	joined := strings.Join(commands, "\n")
	if strings.Count(joined, "systemctl daemon-reload") != 1 || strings.Count(joined, "systemctl restart tailscaled.service") != 1 {
		t.Fatalf("isolation reconciliation was not idempotent:\n%s", joined)
	}
}

func TestTailscaleIsolationRetriesAfterRestartFailure(t *testing.T) {
	root := t.TempDir()
	overridePath := filepath.Join(root, "tailscaled.service.d", "90-vastora-privacy.conf")
	hostsPath := filepath.Join(root, "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
			return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
		}
		if name == "systemctl" && strings.Join(arguments, " ") == "restart tailscaled.service" {
			restarts++
			if restarts == 1 {
				return []byte("temporary restart failure"), errors.New("restart failed")
			}
		}
		return nil, nil
	}
	desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}}
	environment := tailscaleIsolationEnvironment{overridePath: overridePath, hostsPath: hostsPath, derpCache: filepath.Join(root, "missing-cache"), run: run}
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err == nil {
		t.Fatal("failed Tailscale restart was accepted")
	}
	pending, err := os.ReadFile(tailscalePrivacyAppliedPath(overridePath))
	if err != nil || string(pending) != tailscalePrivacyPendingMarker {
		t.Fatalf("failed reconciliation marker = %q, err=%v", pending, err)
	}
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
		t.Fatal(err)
	}
	if restarts != 2 {
		t.Fatalf("Tailscale restart attempts = %d, want 2", restarts)
	}
}

func TestTailscaleIsolationInvalidatesExistingMarkerBeforeDERPCacheMutation(t *testing.T) {
	root := t.TempDir()
	overridePath := filepath.Join(root, "tailscaled.service.d", "90-vastora-privacy.conf")
	hostsPath := filepath.Join(root, "hosts")
	cachePath := filepath.Join(root, "derpmap.cached.json")
	desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}}
	hosts, err := replaceTailscaleHostsSection("127.0.0.1 localhost\n", []string{"headscale.example.com"}, desired.ControlAddresses)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(overridePath), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, value := range map[string]string{
		overridePath: tailscalePrivacyOverride,
		tailscalePrivacyAppliedPath(overridePath): tailscalePrivacyAppliedMarker,
		hostsPath: hosts,
		cachePath: `{"Regions":{"2":{"Nodes":[{"HostName":"derp2.tailscale.com"}]}}}`,
	} {
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restarts := 0
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		command := name + " " + strings.Join(arguments, " ")
		if command == "systemctl restart tailscaled.service" {
			restarts++
			if restarts == 1 {
				return []byte("temporary restart failure"), errors.New("restart failed")
			}
		}
		if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
			return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
		}
		return nil, nil
	}
	environment := tailscaleIsolationEnvironment{overridePath: overridePath, hostsPath: hostsPath, derpCache: cachePath, run: run}
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err == nil {
		t.Fatal("failed restart after DERP cache removal was accepted")
	}
	pending, err := os.ReadFile(tailscalePrivacyAppliedPath(overridePath))
	if err != nil || string(pending) != tailscalePrivacyPendingMarker {
		t.Fatalf("interrupted reconciliation marker = %q, err=%v", pending, err)
	}
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
		t.Fatal(err)
	}
	if restarts != 2 {
		t.Fatalf("restart attempts = %d, want 2", restarts)
	}
}

func TestTailscaleIsolationRetriesSystemdAndRuntimeVerificationFailures(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*int, *int, *bool) func(context.Context, string, ...string) ([]byte, error)
		wantReload int
		wantStart  int
	}{
		{
			name: "daemon reload",
			configure: func(reloads, starts *int, _ *bool) func(context.Context, string, ...string) ([]byte, error) {
				return func(_ context.Context, name string, arguments ...string) ([]byte, error) {
					command := name + " " + strings.Join(arguments, " ")
					if command == "systemctl daemon-reload" {
						*reloads++
						if *reloads == 1 {
							return []byte("reload failed"), errors.New("reload failed")
						}
					}
					if command == "systemctl restart tailscaled.service" {
						*starts++
					}
					if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
						return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
					}
					return nil, nil
				}
			},
			wantReload: 2,
			wantStart:  1,
		},
		{
			name: "inactive enable",
			configure: func(reloads, starts *int, active *bool) func(context.Context, string, ...string) ([]byte, error) {
				return func(_ context.Context, name string, arguments ...string) ([]byte, error) {
					command := name + " " + strings.Join(arguments, " ")
					if command == "systemctl daemon-reload" {
						*reloads++
					}
					if command == "systemctl is-active --quiet tailscaled.service" && !*active {
						return nil, errors.New("inactive")
					}
					if command == "systemctl enable --now tailscaled.service" {
						*starts++
						if *starts == 1 {
							return []byte("enable failed"), errors.New("enable failed")
						}
						*active = true
					}
					if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
						return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
					}
					return nil, nil
				}
			},
			wantReload: 2,
			wantStart:  2,
		},
		{
			name: "post restart inactive",
			configure: func(reloads, starts *int, active *bool) func(context.Context, string, ...string) ([]byte, error) {
				activeChecks := 0
				*active = true
				return func(_ context.Context, name string, arguments ...string) ([]byte, error) {
					command := name + " " + strings.Join(arguments, " ")
					if command == "systemctl daemon-reload" {
						*reloads++
					}
					if command == "systemctl restart tailscaled.service" {
						*starts++
					}
					if command == "systemctl is-active --quiet tailscaled.service" {
						activeChecks++
						if activeChecks == 2 {
							return []byte("inactive after restart"), errors.New("inactive")
						}
					}
					if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
						return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
					}
					return nil, nil
				}
			},
			wantReload: 2,
			wantStart:  2,
		},
		{
			name: "privacy environment",
			configure: func(reloads, starts *int, _ *bool) func(context.Context, string, ...string) ([]byte, error) {
				shows := 0
				return func(_ context.Context, name string, arguments ...string) ([]byte, error) {
					command := name + " " + strings.Join(arguments, " ")
					if command == "systemctl daemon-reload" {
						*reloads++
					}
					if command == "systemctl restart tailscaled.service" {
						*starts++
					}
					if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
						shows++
						if shows == 1 {
							return []byte("OTHER=value\n"), nil
						}
						return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
					}
					return nil, nil
				}
			},
			wantReload: 2,
			wantStart:  2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			hostsPath := filepath.Join(root, "hosts")
			if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			reloads, starts := 0, 0
			active := false
			run := test.configure(&reloads, &starts, &active)
			desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}}
			environment := tailscaleIsolationEnvironment{overridePath: filepath.Join(root, "90-vastora-privacy.conf"), hostsPath: hostsPath, derpCache: filepath.Join(root, "missing-cache"), run: run}
			if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err == nil {
				t.Fatal("first systemd/runtime failure was accepted")
			}
			pending, err := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
			if err != nil || string(pending) != tailscalePrivacyPendingMarker {
				t.Fatalf("failed reconciliation marker = %q, err=%v", pending, err)
			}
			if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
				t.Fatal(err)
			}
			if reloads != test.wantReload || starts != test.wantStart {
				t.Fatalf("reloads=%d starts=%d, want %d/%d", reloads, starts, test.wantReload, test.wantStart)
			}
		})
	}
}

func TestTailscaleIsolationRepairsDiskDriftDespiteAppliedMarker(t *testing.T) {
	desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}}
	for _, drift := range []string{"hosts", "override"} {
		t.Run(drift, func(t *testing.T) {
			root := t.TempDir()
			overridePath := filepath.Join(root, "tailscaled.service.d", "90-vastora-privacy.conf")
			hostsPath := filepath.Join(root, "hosts")
			hosts, err := replaceTailscaleHostsSection("127.0.0.1 localhost\n", []string{"headscale.example.com"}, desired.ControlAddresses)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(overridePath), 0o755); err != nil {
				t.Fatal(err)
			}
			for path, value := range map[string]string{
				overridePath: tailscalePrivacyOverride,
				tailscalePrivacyAppliedPath(overridePath): tailscalePrivacyAppliedMarker,
				hostsPath: hosts,
			} {
				if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if drift == "hosts" {
				if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(overridePath, []byte("[Service]\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			restarts := 0
			run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
				if name == "systemctl" && strings.Join(arguments, " ") == "restart tailscaled.service" {
					restarts++
				}
				if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
					return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
				}
				return nil, nil
			}
			environment := tailscaleIsolationEnvironment{overridePath: overridePath, hostsPath: hostsPath, derpCache: filepath.Join(root, "missing-cache"), run: run}
			if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
				t.Fatal(err)
			}
			if restarts != 1 {
				t.Fatalf("restart count = %d, want 1", restarts)
			}
		})
	}
}

func TestTailscaleIsolationConfigureOnlyLeavesDurablePendingWork(t *testing.T) {
	root := t.TempDir()
	hostsPath := filepath.Join(root, "hosts")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	run := func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		if name == "systemctl" && strings.Join(arguments, " ") == "restart tailscaled.service" {
			restarts++
		}
		if name == "systemctl" && len(arguments) > 0 && arguments[0] == "show" {
			return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
		}
		return nil, nil
	}
	desired := agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}}
	environment := tailscaleIsolationEnvironment{overridePath: filepath.Join(root, "90-vastora-privacy.conf"), hostsPath: hostsPath, derpCache: filepath.Join(root, "missing-cache"), run: run}
	if err := reconcileTailscaleIsolation(context.Background(), desired, true, environment); err != nil {
		t.Fatal(err)
	}
	pending, err := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
	if err != nil || string(pending) != tailscalePrivacyPendingMarker || restarts != 0 {
		t.Fatalf("configure-only state: marker=%q restarts=%d err=%v", pending, restarts, err)
	}
	if err := reconcileTailscaleIsolation(context.Background(), desired, false, environment); err != nil {
		t.Fatal(err)
	}
	applied, err := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
	if err != nil || string(applied) != tailscalePrivacyAppliedMarker || restarts != 1 {
		t.Fatalf("resumed state: marker=%q restarts=%d err=%v", applied, restarts, err)
	}
}

func TestTailscaleIsolationFailsBeforeChangingHostStateWhenTLSVerificationFails(t *testing.T) {
	root := t.TempDir()
	hostsPath := filepath.Join(root, "hosts")
	cachePath := filepath.Join(root, "derpmap.cached.json")
	if err := os.WriteFile(hostsPath, []byte("127.0.0.1 localhost\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := []byte(`{"Regions":{"2":{"Nodes":[{"HostName":"derp2.tailscale.com"}]}}}`)
	if err := os.WriteFile(cachePath, cache, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "curl" {
			return []byte("certificate mismatch"), errors.New("verification failed")
		}
		return nil, nil
	}
	environment := tailscaleIsolationEnvironment{overridePath: filepath.Join(root, "override.conf"), hostsPath: hostsPath, derpCache: cachePath, run: run}
	err := reconcileTailscaleIsolation(context.Background(), agent.TailscaleIsolationDesiredState{ControlURL: "https://headscale.example.com", ControlAddresses: []string{"203.0.113.10"}}, false, environment)
	if err == nil {
		t.Fatal("unverified Headscale endpoint was accepted")
	}
	hosts, _ := os.ReadFile(hostsPath)
	remainingCache, _ := os.ReadFile(cachePath)
	if string(hosts) != "127.0.0.1 localhost\n" || !bytes.Equal(remainingCache, cache) {
		t.Fatalf("host state changed before verification: hosts=%q cache=%q", hosts, remainingCache)
	}
}

func TestAgentUpdateVerifiesAndKeepsRollbackBinary(t *testing.T) {
	newBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then printf '0.2.0\\n'; fi\n")
	digest := sha256.Sum256(newBinary)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		wantPath := "/api/v1/agents/node-id/binary/linux/" + runtime.GOARCH
		if request.URL.Path != wantPath || request.Header.Get("Authorization") != "Bearer node-credential" {
			t.Fatalf("unexpected update request: %s auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		response.Header().Set("X-Vastora-Version", "0.2.0")
		response.Header().Set("X-Vastora-SHA256", fmt.Sprintf("%x", digest[:]))
		_, _ = response.Write(newBinary)
	}))
	defer server.Close()
	executable := filepath.Join(t.TempDir(), "vastora")
	oldBinary := []byte("old-agent-binary")
	if err := os.WriteFile(executable, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	version, err := updateAgentExecutable(context.Background(), server.Client(), agent.Connection{AgentID: "node-id", CenterURL: server.URL, Credential: "node-credential"}, executable, func() error { restarts++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(executable)
	rollback, _ := os.ReadFile(executable + ".previous")
	if version != "0.2.0" || restarts != 1 || string(installed) != string(newBinary) || string(rollback) != string(oldBinary) {
		t.Fatalf("unexpected Agent update: version=%q restarts=%d installed=%q rollback=%q", version, restarts, installed, rollback)
	}
}

func TestAgentUpdateEndpointSupportsBothLinuxArchitectures(t *testing.T) {
	connection := agent.Connection{AgentID: "node id", CenterURL: "https://center.example.com/"}
	for _, architecture := range []string{"amd64", "arm64"} {
		endpoint, err := agentUpdateEndpoint(connection, "linux", architecture)
		if err != nil {
			t.Fatalf("agentUpdateEndpoint(%s): %v", architecture, err)
		}
		want := "https://center.example.com/api/v1/agents/node%20id/binary/linux/" + architecture
		if endpoint != want {
			t.Fatalf("endpoint = %q, want %q", endpoint, want)
		}
	}
	if _, err := agentUpdateEndpoint(connection, "linux", "386"); err == nil {
		t.Fatal("unsupported update architecture was accepted")
	}
}

func TestAgentUpdateRestoresPreviousBinaryWhenRestartFails(t *testing.T) {
	newBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then printf '0.2.0\\n'; fi\n")
	digest := sha256.Sum256(newBinary)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Vastora-Version", "0.2.0")
		response.Header().Set("X-Vastora-SHA256", fmt.Sprintf("%x", digest[:]))
		_, _ = response.Write(newBinary)
	}))
	defer server.Close()
	executable := filepath.Join(t.TempDir(), "vastora")
	oldBinary := []byte("old-agent-binary")
	if err := os.WriteFile(executable, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	_, err := updateAgentExecutable(context.Background(), server.Client(), agent.Connection{AgentID: "node-id", CenterURL: server.URL, Credential: "node-credential"}, executable, func() error {
		restarts++
		if restarts == 1 {
			return fmt.Errorf("new service failed")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "previous binary restored") {
		t.Fatalf("restart failure was not reported: %v", err)
	}
	restored, _ := os.ReadFile(executable)
	if restarts != 2 || string(restored) != string(oldBinary) {
		t.Fatalf("previous Agent was not restored: restarts=%d content=%q", restarts, restored)
	}
}
