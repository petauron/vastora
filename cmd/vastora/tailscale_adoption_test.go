package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/tailscalehost"
)

func TestAdoptLegacyVastoraTailscaleRequiresCompleteProvenance(t *testing.T) {
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
		filepath.Join(aptDir, "history.log"):     "Start-Date: 2026-08-01\nCommandline: apt-get install -y tailscale=" + tailscalehost.SupportedVersion + " tailscale-archive-keyring\nEnd-Date: 2026-08-01\n",
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
			commands = append(commands, name+" "+strings.Join(arguments, " "))
			if name == "tailscale" {
				return []byte(tailscalehost.SupportedVersion + "\n"), nil
			}
			return nil, nil
		},
	}
	if err := adoptLegacyVastoraTailscale(context.Background(), environment); err != nil {
		t.Fatal(err)
	}
	state, err := agent.ReadHostInstallState(dataDir)
	if err != nil || state.TailscaleOwnership != "managed" || !state.TailscaleEnrolled {
		t.Fatalf("adopted state = %#v, err=%v", state, err)
	}
	if len(commands) != 2 || commands[1] != "systemctl restart vastora-agent.service" {
		t.Fatalf("adoption commands = %#v", commands)
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
