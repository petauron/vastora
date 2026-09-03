package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/tailscalehost"
)

func legacyTailscaleAdoptionFixture(t *testing.T, version string) (tailscaleAdoptionEnvironment, *[]string) {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "agent")
	privacyPath := filepath.Join(root, "90-vastora-privacy.conf")
	hostsPath := filepath.Join(root, "hosts")
	unitPath := filepath.Join(root, "vastora-agent.service")
	aptDir := filepath.Join(root, "apt")
	for _, directory := range []string{dataDir, aptDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(dataDir, agent.HostInstallStateName): "HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=external\nTAILSCALE_ENROLLED=1\n",
		unitPath:                                 "[Unit]\nDescription=Vastora Agent\n[Service]\nExecStart=\"/usr/local/bin/vastora\" agent serve --data-dir \"" + dataDir + "\"\n",
		privacyPath:                              tailscalePrivacyOverride,
		tailscalePrivacyAppliedPath(privacyPath): tailscalePrivacyAppliedMarker,
		hostsPath:                                "127.0.0.1 localhost\n" + tailscaleHostsBeginMarker + "\n203.0.113.10 headscale.example.com\n" + tailscaleHostsEndMarker + "\n",
		filepath.Join(aptDir, "history.log"):     "Start-Date: 2026-08-01\nCommandline: apt-get install -y tailscale=1.102.3 tailscale-archive-keyring\nEnd-Date: 2026-08-01\n",
	}
	for path, content := range files {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commands := []string{}
	environment := tailscaleAdoptionEnvironment{
		dataDir: dataDir, agentUnitPath: unitPath, privacyPath: privacyPath, hostsPath: hostsPath, aptHistoryDir: aptDir,
		run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			command := name + " " + strings.Join(arguments, " ")
			commands = append(commands, command)
			switch command {
			case "tailscale version --json --daemon":
				return json.Marshal(map[string]string{"short": version, "long": version, "daemonLong": version})
			case "tailscaled --version":
				return []byte(version + "\n  long version: " + version + "\n"), nil
			case "tailscaled --help":
				return []byte("  -config string\n"), nil
			case "tailscale status --json":
				return json.Marshal(map[string]string{"BackendState": "Running", "Version": version})
			case "systemctl show --property=Environment --value tailscaled.service":
				return []byte("TS_NO_LOGS_NO_SUPPORT=true\n"), nil
			case "systemctl is-active --quiet tailscaled.service", "systemctl restart vastora-agent.service":
				return nil, nil
			default:
				t.Fatalf("unexpected adoption command: %s", command)
				return nil, nil
			}
		},
	}
	return environment, &commands
}

func TestAdoptLegacyVastoraTailscaleRequiresCompleteProvenance(t *testing.T) {
	for _, version := range []string{tailscalehost.MinimumCompatibleVersion, "1.104.0"} {
		t.Run(version, func(t *testing.T) {
			environment, commands := legacyTailscaleAdoptionFixture(t, version)
			if err := adoptLegacyVastoraTailscale(context.Background(), environment); err != nil {
				t.Fatal(err)
			}
			state, err := agent.ReadHostInstallState(environment.dataDir)
			if err != nil || state.TailscaleOwnership != "managed" || !state.TailscaleEnrolled {
				t.Fatalf("adopted state = %#v, err=%v", state, err)
			}
			if len(*commands) != 7 || (*commands)[6] != "systemctl restart vastora-agent.service" {
				t.Fatalf("adoption commands = %#v", *commands)
			}
		})
	}
}

func TestAdoptionVersionCompatibilityCannotReplaceHistoricalProvenance(t *testing.T) {
	for _, history := range []string{
		"Commandline: apt-get install -y tailscale\n",
		"Commandline: apt-get install -y tailscale=1.104.0 tailscale-archive-keyring\n",
		"Commandline: apt-get install -y tailscale=1.102.3 tailscale-archive-keyring-extra\n",
		"Commandline: apt-get install -y tailscale=1.102.3 tailscale-archive-keyring another-package\n",
	} {
		t.Run(history, func(t *testing.T) {
			environment, commands := legacyTailscaleAdoptionFixture(t, "1.104.0")
			if err := os.WriteFile(filepath.Join(environment.aptHistoryDir, "history.log"), []byte(history), 0o600); err != nil {
				t.Fatal(err)
			}
			err := adoptLegacyVastoraTailscale(context.Background(), environment)
			if err == nil || !strings.Contains(err.Error(), "cannot prove") {
				t.Fatalf("external package history was accepted: %v", err)
			}
			state, readErr := agent.ReadHostInstallState(environment.dataDir)
			if readErr != nil || state.TailscaleOwnership != "external" || strings.Contains(strings.Join(*commands, "\n"), "restart") {
				t.Fatalf("unproven package was adopted: state=%#v commands=%v err=%v", state, *commands, readErr)
			}
		})
	}
}

func TestAdoptionRestoresOwnershipAfterAgentRestartFailure(t *testing.T) {
	environment, _ := legacyTailscaleAdoptionFixture(t, "1.104.0")
	previousRun := environment.run
	environment.run = func(ctx context.Context, name string, arguments ...string) ([]byte, error) {
		if name+" "+strings.Join(arguments, " ") == "systemctl restart vastora-agent.service" {
			return nil, errors.New("restart failed")
		}
		return previousRun(ctx, name, arguments...)
	}
	if err := adoptLegacyVastoraTailscale(context.Background(), environment); err == nil {
		t.Fatal("failed restart was accepted")
	}
	state, err := agent.ReadHostInstallState(environment.dataDir)
	if err != nil || state.TailscaleOwnership != "external" || !state.TailscaleEnrolled {
		t.Fatalf("previous ownership was not restored: %#v err=%v", state, err)
	}
}

func TestAdoptionRejectsAnIncompatibleDaemonWithoutChangingOwnership(t *testing.T) {
	environment, commands := legacyTailscaleAdoptionFixture(t, "1.102.2")
	if err := adoptLegacyVastoraTailscale(context.Background(), environment); err == nil {
		t.Fatal("below-minimum daemon was adopted")
	}
	state, err := agent.ReadHostInstallState(environment.dataDir)
	if err != nil || state.TailscaleOwnership != "external" || len(*commands) != 1 {
		t.Fatalf("failed version preflight changed state: %#v commands=%v err=%v", state, *commands, err)
	}
}

func TestAdoptLegacyVastoraTailscaleRejectsExternalInstall(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, agent.HostInstallStateName), []byte("HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=external\nTAILSCALE_ENROLLED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := adoptLegacyVastoraTailscale(context.Background(), tailscaleAdoptionEnvironment{dataDir: dataDir, agentUnitPath: filepath.Join(root, "missing")})
	if err == nil || !strings.Contains(err.Error(), "Agent service") {
		t.Fatalf("external Tailscale was not rejected: %v", err)
	}
	state, readErr := agent.ReadHostInstallState(dataDir)
	if readErr != nil || state.TailscaleOwnership != "external" {
		t.Fatalf("external ownership changed: %#v, err=%v", state, readErr)
	}
}
