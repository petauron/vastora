package main

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/tailscalehost"
)

type tailscaleAdoptionEnvironment struct {
	dataDir       string
	agentUnitPath string
	privacyPath   string
	hostsPath     string
	aptHistoryDir string
	run           func(context.Context, string, ...string) ([]byte, error)
}

func defaultTailscaleAdoptionEnvironment(dataDir string) tailscaleAdoptionEnvironment {
	privacy := defaultTailscaleIsolationEnvironment()
	return tailscaleAdoptionEnvironment{
		dataDir:       dataDir,
		agentUnitPath: vastoraAgentUnitPath,
		privacyPath:   privacy.overridePath,
		hostsPath:     privacy.hostsPath,
		aptHistoryDir: "/var/log/apt",
		run:           runHostCommand,
	}
}

func adoptLegacyVastoraTailscale(ctx context.Context, environment tailscaleAdoptionEnvironment) error {
	state, err := agent.ReadHostInstallState(environment.dataDir)
	if err != nil {
		return err
	}
	if state.TailscaleOwnership == "managed" {
		return nil
	}
	if state.TailscaleOwnership != "external" || !state.TailscaleEnrolled {
		return errors.New("cannot adopt Tailscale because this Agent has no older enrolled external-ownership state")
	}
	unit, err := os.ReadFile(environment.agentUnitPath)
	if err != nil || !strings.Contains(string(unit), "Description=Vastora Agent") || !strings.Contains(string(unit), " agent serve --data-dir "+strconv.Quote(environment.dataDir)) {
		return errors.New("cannot verify the Vastora-managed Agent service")
	}
	privacy, err := os.ReadFile(environment.privacyPath)
	if err != nil || string(privacy) != tailscalePrivacyOverride {
		return errors.New("cannot verify the Vastora-managed Tailscale privacy override")
	}
	applied, err := os.ReadFile(tailscalePrivacyAppliedPath(environment.privacyPath))
	if err != nil || string(applied) != tailscalePrivacyAppliedMarker {
		return errors.New("cannot verify that Vastora applied the Tailscale privacy override")
	}
	hosts, err := os.ReadFile(environment.hostsPath)
	if err != nil || !validVastoraTailscaleHostsSection(string(hosts)) {
		return errors.New("cannot verify the Vastora-managed Headscale resolver section")
	}
	if environment.run == nil {
		return errors.New("cannot verify the installed Tailscale binary")
	}
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	version, err := environment.run(verifyCtx, "tailscale", "version")
	cancel()
	if err != nil || strings.TrimSpace(strings.SplitN(string(version), "\n", 2)[0]) != tailscalehost.SupportedVersion {
		return fmt.Errorf("cannot adopt Tailscale: Vastora requires installed version %s", tailscalehost.SupportedVersion)
	}
	provenance, err := aptHistoryContainsVastoraTailscaleInstall(environment.aptHistoryDir, tailscalehost.SupportedVersion)
	if err != nil {
		return err
	}
	if !provenance {
		return errors.New("cannot prove that an older Vastora installer installed this Tailscale package; it remains external")
	}
	statePath := filepath.Join(environment.dataDir, agent.HostInstallStateName)
	previous, err := os.ReadFile(statePath)
	if err != nil {
		return fmt.Errorf("read existing host ownership state: %w", err)
	}
	managed := "HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=managed\nTAILSCALE_ENROLLED=1\n"
	if err := writeAtomicHostFile(statePath, managed, 0o600); err != nil {
		return fmt.Errorf("record Vastora Tailscale ownership: %w", err)
	}
	restartCtx, restartCancel := context.WithTimeout(ctx, 30*time.Second)
	output, restartErr := environment.run(restartCtx, "systemctl", "restart", "vastora-agent.service")
	restartCancel()
	if restartErr != nil {
		if restoreErr := writeAtomicHostFile(statePath, string(previous), 0o600); restoreErr != nil {
			return errors.Join(fmt.Errorf("restart Agent after Tailscale adoption: %s: %w", strings.TrimSpace(string(output)), restartErr), fmt.Errorf("restore previous ownership state: %w", restoreErr))
		}
		return fmt.Errorf("restart Agent after Tailscale adoption; previous ownership state restored: %s: %w", strings.TrimSpace(string(output)), restartErr)
	}
	return nil
}

func validVastoraTailscaleHostsSection(content string) bool {
	begin := strings.Index(content, tailscaleHostsBeginMarker)
	end := strings.Index(content, tailscaleHostsEndMarker)
	if begin < 0 || end <= begin || strings.Count(content, tailscaleHostsBeginMarker) != 1 || strings.Count(content, tailscaleHostsEndMarker) != 1 {
		return false
	}
	body := strings.TrimSpace(content[begin+len(tailscaleHostsBeginMarker) : end])
	if body == "" || strings.Contains(body, "# BEGIN") || strings.Contains(body, "# END") {
		return false
	}
	valid := false
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || net.ParseIP(fields[0]) == nil {
			return false
		}
		valid = true
	}
	return valid
}

func aptHistoryContainsVastoraTailscaleInstall(directory, version string) (bool, error) {
	paths, err := filepath.Glob(filepath.Join(directory, "history.log*"))
	if err != nil {
		return false, fmt.Errorf("inspect apt history: %w", err)
	}
	needle := "apt-get install -y tailscale=" + version + " tailscale-archive-keyring"
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return false, fmt.Errorf("read apt history: %w", err)
		}
		var reader io.Reader = file
		var compressed *gzip.Reader
		if strings.HasSuffix(path, ".gz") {
			compressed, err = gzip.NewReader(file)
			if err != nil {
				_ = file.Close()
				return false, fmt.Errorf("read compressed apt history: %w", err)
			}
			reader = compressed
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, 32<<20))
		if compressed != nil {
			_ = compressed.Close()
		}
		_ = file.Close()
		if readErr != nil {
			return false, fmt.Errorf("read apt history: %w", readErr)
		}
		normalized := strings.NewReplacer("\"", "", "'", "", "\t", " ").Replace(string(content))
		for strings.Contains(normalized, "  ") {
			normalized = strings.ReplaceAll(normalized, "  ", " ")
		}
		if strings.Contains(normalized, "Commandline: "+needle) {
			return true, nil
		}
	}
	return false, nil
}
