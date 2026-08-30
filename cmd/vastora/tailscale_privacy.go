package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
)

const (
	tailscalePrivacyOverride      = "[Service]\nEnvironment=TS_NO_LOGS_NO_SUPPORT=true\n"
	tailscalePrivacyPendingMarker = "v3:pending\n"
	tailscalePrivacyAppliedMarker = "v3:applied\n"
	tailscaleHostsBeginMarker     = "# BEGIN VASTORA TAILSCALE CONTROL"
	tailscaleHostsEndMarker       = "# END VASTORA TAILSCALE CONTROL"
)

type tailscaleIsolationEnvironment struct {
	overridePath string
	hostsPath    string
	derpCache    string
	run          func(context.Context, string, ...string) ([]byte, error)
}

func defaultTailscaleIsolationEnvironment() tailscaleIsolationEnvironment {
	return tailscaleIsolationEnvironment{
		overridePath: "/etc/systemd/system/tailscaled.service.d/90-vastora-privacy.conf",
		hostsPath:    "/etc/hosts",
		derpCache:    "/var/lib/tailscale/derpmap.cached.json",
		run:          runHostCommand,
	}
}

func tailscalePrivacyAppliedPath(path string) string {
	return path + ".applied"
}

// reconcileTailscaleIsolation applies host privacy controls before tailscaled
// is started or restarted. It deliberately leaves tailscaled.state untouched
// because that file contains the node identity and login state.
func reconcileTailscaleIsolation(ctx context.Context, desired agent.TailscaleIsolationDesiredState, configureOnly bool, environment tailscaleIsolationEnvironment) error {
	desired, err := validateTailscaleIsolationDesiredState(desired)
	if err != nil {
		return err
	}
	if environment.run == nil {
		return errors.New("configure Tailscale isolation: host command runner is required")
	}
	current, err := tailscaleIsolationCurrent(ctx, environment, desired)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	if err := verifyTailscaleControlEndpoint(ctx, desired, environment.run); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(environment.overridePath), 0o755); err != nil {
		return fmt.Errorf("create Tailscale systemd override directory: %w", err)
	}
	if err := writeAtomicHostFile(tailscalePrivacyAppliedPath(environment.overridePath), tailscalePrivacyPendingMarker, 0o644); err != nil {
		return fmt.Errorf("record pending Tailscale isolation state: %w", err)
	}
	if _, err := reconcileTailscaleControlHosts(environment.hostsPath, desired); err != nil {
		return err
	}
	if _, err := reconcileTailscalePrivacyFiles(environment.overridePath); err != nil {
		return err
	}
	if _, err := removeUntrustedDERPCache(environment.derpCache); err != nil {
		return err
	}

	commandContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if output, runErr := environment.run(commandContext, "systemctl", "daemon-reload"); runErr != nil {
		return fmt.Errorf("reload systemd for Tailscale isolation: %s: %w", strings.TrimSpace(string(output)), runErr)
	}
	if configureOnly {
		return nil
	}
	active := true
	if _, activeErr := environment.run(commandContext, "systemctl", "is-active", "--quiet", "tailscaled.service"); activeErr != nil {
		active = false
	}
	var output []byte
	if active {
		output, err = environment.run(commandContext, "systemctl", "restart", "tailscaled.service")
	} else {
		output, err = environment.run(commandContext, "systemctl", "enable", "--now", "tailscaled.service")
	}
	if err != nil {
		return fmt.Errorf("start Tailscale with Vastora isolation: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := verifyTailscaleRuntimePrivacy(commandContext, environment.run); err != nil {
		return err
	}
	if err := writeAtomicHostFile(tailscalePrivacyAppliedPath(environment.overridePath), tailscalePrivacyAppliedMarker, 0o644); err != nil {
		return fmt.Errorf("record applied Tailscale isolation state: %w", err)
	}
	return nil
}

func tailscaleIsolationCurrent(ctx context.Context, environment tailscaleIsolationEnvironment, desired agent.TailscaleIsolationDesiredState) (bool, error) {
	override, err := os.ReadFile(environment.overridePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect Tailscale privacy override: %w", err)
	}
	marker, markerErr := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect Tailscale isolation marker: %w", markerErr)
	}
	hosts, err := os.ReadFile(environment.hostsPath)
	if err != nil {
		return false, fmt.Errorf("read host resolver configuration: %w", err)
	}
	expectedHosts, err := replaceTailscaleHostsSection(string(hosts), tailscaleControlHostnames(desired), desired.ControlAddresses)
	if err != nil {
		return false, err
	}
	untrustedCache, err := untrustedDERPCache(environment.derpCache)
	if err != nil {
		return false, err
	}
	diskCurrent := string(override) == tailscalePrivacyOverride && markerErr == nil && string(marker) == tailscalePrivacyAppliedMarker && expectedHosts == string(hosts) && !untrustedCache
	if !diskCurrent {
		return false, nil
	}
	runtimeContext, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return tailscaleRuntimePrivacyCurrent(runtimeContext, environment.run), nil
}

func tailscaleRuntimePrivacyCurrent(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error)) bool {
	if _, err := run(ctx, "systemctl", "is-active", "--quiet", "tailscaled.service"); err != nil {
		return false
	}
	output, err := run(ctx, "systemctl", "show", "--property=Environment", "--value", "tailscaled.service")
	if err != nil {
		return false
	}
	for _, value := range strings.Fields(string(output)) {
		if strings.Trim(value, "\"") == "TS_NO_LOGS_NO_SUPPORT=true" {
			return true
		}
	}
	return false
}

func verifyTailscaleRuntimePrivacy(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error)) error {
	output, err := run(ctx, "systemctl", "is-active", "--quiet", "tailscaled.service")
	if err != nil {
		return fmt.Errorf("verify Tailscale service is active after isolation: %s: %w", strings.TrimSpace(string(output)), err)
	}
	output, err = run(ctx, "systemctl", "show", "--property=Environment", "--value", "tailscaled.service")
	if err != nil {
		return fmt.Errorf("verify Tailscale privacy environment: %s: %w", strings.TrimSpace(string(output)), err)
	}
	for _, value := range strings.Fields(string(output)) {
		if strings.Trim(value, "\"") == "TS_NO_LOGS_NO_SUPPORT=true" {
			return nil
		}
	}
	return errors.New("verify Tailscale privacy environment: TS_NO_LOGS_NO_SUPPORT=true is not loaded")
}

func validateTailscaleIsolationDesiredState(desired agent.TailscaleIsolationDesiredState) (agent.TailscaleIsolationDesiredState, error) {
	var err error
	desired.ControlURL, err = normalizeTailscaleControlURL(desired.ControlURL)
	if err != nil {
		return agent.TailscaleIsolationDesiredState{}, err
	}
	seenAliases := map[string]struct{}{desired.ControlURL: {}}
	aliases := make([]string, 0, len(desired.ControlAliases))
	for _, value := range desired.ControlAliases {
		alias, aliasErr := normalizeTailscaleControlURL(value)
		if aliasErr != nil {
			return agent.TailscaleIsolationDesiredState{}, aliasErr
		}
		if _, exists := seenAliases[alias]; exists {
			continue
		}
		seenAliases[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	desired.ControlAliases = aliases
	seen := make(map[string]struct{}, len(desired.ControlAddresses))
	addresses := make([]string, 0, len(desired.ControlAddresses))
	for _, value := range desired.ControlAddresses {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil {
			return agent.TailscaleIsolationDesiredState{}, errors.New("configure Tailscale isolation: control address is invalid")
		}
		address := ip.String()
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		addresses = append(addresses, address)
	}
	desired.ControlAddresses = addresses
	return desired, nil
}

func normalizeTailscaleControlURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("configure Tailscale isolation: Headscale control URL must be an HTTPS origin")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func tailscaleControlHostnames(desired agent.TailscaleIsolationDesiredState) []string {
	values := append([]string{desired.ControlURL}, desired.ControlAliases...)
	hostnames := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, _ := url.Parse(value)
		hostname := parsed.Hostname()
		if _, exists := seen[hostname]; !exists {
			seen[hostname] = struct{}{}
			hostnames = append(hostnames, hostname)
		}
	}
	return hostnames
}

func verifyTailscaleControlEndpoint(ctx context.Context, desired agent.TailscaleIsolationDesiredState, run func(context.Context, string, ...string) ([]byte, error)) error {
	parsed, _ := url.Parse(desired.ControlURL)
	hostname := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	addresses := desired.ControlAddresses
	if len(addresses) == 0 {
		addresses = []string{""}
	}
	var failures []string
	for _, address := range addresses {
		arguments := []string{"--proto", "=https", "--tlsv1.2", "--connect-timeout", "10", "--max-time", "20", "--silent", "--show-error", "--output", "/dev/null"}
		if address != "" {
			resolved := address
			if strings.Contains(address, ":") {
				resolved = "[" + address + "]"
			}
			arguments = append(arguments, "--resolve", hostname+":"+port+":"+resolved)
		}
		arguments = append(arguments, desired.ControlURL+"/")
		verifyContext, cancel := context.WithTimeout(ctx, 25*time.Second)
		output, err := run(verifyContext, "curl", arguments...)
		cancel()
		if err == nil {
			return nil
		}
		failures = append(failures, strings.TrimSpace(string(output)))
	}
	return fmt.Errorf("verify Headscale control endpoint before changing Tailscale: %s", strings.Join(failures, "; "))
}

func reconcileTailscaleControlHosts(path string, desired agent.TailscaleIsolationDesiredState) (bool, error) {
	current, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read host resolver configuration: %w", err)
	}
	updated, err := replaceTailscaleHostsSection(string(current), tailscaleControlHostnames(desired), desired.ControlAddresses)
	if err != nil {
		return false, err
	}
	if updated == string(current) {
		return false, nil
	}
	if err := writeAtomicHostFile(path, updated, 0o644); err != nil {
		return false, fmt.Errorf("install Headscale control address pin: %w", err)
	}
	return true, nil
}

func replaceTailscaleHostsSection(current string, hostnames, addresses []string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(current, "\r\n", "\n"), "\n")
	kept := make([]string, 0, len(lines)+len(addresses)+2)
	inside := false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case tailscaleHostsBeginMarker:
			if inside {
				return "", errors.New("host resolver configuration contains nested Vastora markers")
			}
			inside = true
		case tailscaleHostsEndMarker:
			if !inside {
				return "", errors.New("host resolver configuration contains an unmatched Vastora marker")
			}
			inside = false
		default:
			if !inside {
				kept = append(kept, line)
			}
		}
	}
	if inside {
		return "", errors.New("host resolver configuration contains an incomplete Vastora section")
	}
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}
	if len(addresses) > 0 {
		kept = append(kept, tailscaleHostsBeginMarker)
		for _, address := range addresses {
			kept = append(kept, address+" "+strings.Join(hostnames, " "))
		}
		kept = append(kept, tailscaleHostsEndMarker)
	}
	return strings.Join(kept, "\n") + "\n", nil
}

func reconcileTailscalePrivacyFiles(path string) (changed bool, resultErr error) {
	current, fileErr := os.ReadFile(path)
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		return false, fmt.Errorf("inspect Tailscale privacy override: %w", fileErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create Tailscale systemd override directory: %w", err)
	}
	if string(current) != tailscalePrivacyOverride {
		if err := writeAtomicHostFile(path, tailscalePrivacyOverride, 0o644); err != nil {
			return false, fmt.Errorf("install Tailscale privacy override: %w", err)
		}
		changed = true
	}
	return changed, nil
}

func removeUntrustedDERPCache(path string) (bool, error) {
	untrusted, err := untrustedDERPCache(path)
	if err != nil || !untrusted {
		return false, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("remove untrusted Tailscale DERP cache: %w", err)
	}
	return true, nil
}

func untrustedDERPCache(path string) (bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect cached Tailscale DERP map: %w", err)
	}
	lower := strings.ToLower(string(raw))
	if json.Valid(raw) && !strings.Contains(lower, ".tailscale.com") {
		return false, nil
	}
	return true, nil
}

func removeTailscaleControlHosts(path string) (bool, error) {
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	updated, err := replaceTailscaleHostsSection(string(current), nil, nil)
	if err != nil {
		return false, err
	}
	if updated == string(current) {
		return false, nil
	}
	if err := writeAtomicHostFile(path, updated, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func writeAtomicHostFile(path, content string, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".vastora-host-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
