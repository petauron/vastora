package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/tailscalehost"
)

// Alpine's official service sources /etc/conf.d/tailscale. Keep unrelated
// administrator settings and replace only complete executor-owned sections.
func renderOpenRCTailscaleSection(raw, name, body string) (string, error) {
	begin, end := "# BEGIN VASTORA "+name, "# END VASTORA "+name
	if strings.Count(raw, begin) > 1 || strings.Count(raw, end) > 1 {
		return "", errors.New("ambiguous OpenRC Tailscale configuration")
	}

	first, last := strings.Index(raw, begin), strings.Index(raw, end)
	if first >= 0 || last >= 0 {
		if first < 0 || last < first || first > 0 && raw[first-1] != '\n' {
			return "", errors.New("incomplete OpenRC Tailscale configuration")
		}
		tail := last + len(end)
		if tail < len(raw) && raw[tail] != '\n' {
			return "", errors.New("invalid OpenRC Tailscale section boundary")
		}
		if tail < len(raw) {
			tail++
		}
		replacement := ""
		if body != "" {
			replacement = begin + "\n" + body + "\n" + end + "\n"
		}
		return raw[:first] + replacement + raw[tail:], nil
	}

	if body == "" {
		return raw, nil
	}
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}
	return raw + begin + "\n" + body + "\n" + end + "\n", nil
}
func applyOpenRCTailscaleSection(path, name, body string) (bool, error) {
	before, err := inspectHostFile(path)
	if err != nil {
		return false, err
	}
	next, err := renderOpenRCTailscaleSection(string(before.content), name, body)
	if err != nil {
		return false, err
	}
	if next == string(before.content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	return true, writeAtomicHostFile(path, next, 0644)
}

const openRCPrivacy = "export TS_NO_LOGS_NO_SUPPORT=true\nno_logs_no_support=yes"

func reconcileOpenRCTailscaleIsolation(ctx context.Context, desired agent.TailscaleIsolationDesiredState, configureOnly bool, environment tailscaleIsolationEnvironment) error {
	raw, err := os.ReadFile(environment.overridePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	marker, _ := os.ReadFile(tailscalePrivacyAppliedPath(environment.overridePath))
	expected, err := renderOpenRCTailscaleSection(string(raw), "PRIVACY", openRCPrivacy)
	if err != nil {
		return err
	}
	hosts, err := os.ReadFile(environment.hostsPath)
	if err != nil {
		return err
	}
	expectedHosts, err := replaceTailscaleHostsSection(string(hosts), tailscaleControlHostnames(desired), desired.ControlAddresses)
	if err != nil {
		return err
	}
	cache, err := untrustedDERPCache(environment.derpCache)
	if err != nil {
		return err
	}
	if string(raw) == expected && expectedHosts == string(hosts) && !cache && (string(marker) == tailscalePrivacyAppliedMarker || string(marker) == tailscalePrivacyVerifyingMarker) {
		if err := verifyTailscaleRuntimePrivacy(ctx, environment.run, desired); err == nil {
			return writeAtomicHostFile(tailscalePrivacyAppliedPath(environment.overridePath), tailscalePrivacyAppliedMarker, 0644)
		} else if !errors.Is(err, errTailscaleIsolationDrift) {
			return err
		}
	}
	if err := verifyTailscaleControlEndpoint(ctx, desired, environment.run); err != nil {
		return err
	}
	if err := writeAtomicHostFile(tailscalePrivacyAppliedPath(environment.overridePath), tailscalePrivacyPendingMarker, 0644); err != nil {
		return err
	}
	if _, err := reconcileTailscaleControlHosts(environment.hostsPath, desired); err != nil {
		return err
	}
	if _, err := applyOpenRCTailscaleSection(environment.overridePath, "PRIVACY", openRCPrivacy); err != nil {
		return err
	}
	if _, err := removeUntrustedDERPCache(environment.derpCache); err != nil {
		return err
	}
	if configureOnly {
		return nil
	}
	commandCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	active, err := tailscalehost.OpenRCActive(commandCtx, environment.run)
	if err != nil {
		return err
	}
	if active {
		err = tailscalehost.OpenRCAction(commandCtx, environment.run, "restart")
	} else {
		if err = tailscalehost.OpenRCAction(commandCtx, environment.run, "enable"); err == nil {
			err = tailscalehost.OpenRCAction(commandCtx, environment.run, "start")
		}
	}
	if err != nil {
		return err
	}
	if err := verifyTailscaleRuntimePrivacy(commandCtx, environment.run, desired); err != nil {
		if !errors.Is(err, errTailscaleIsolationDrift) {
			_ = writeAtomicHostFile(tailscalePrivacyAppliedPath(environment.overridePath), tailscalePrivacyVerifyingMarker, 0644)
		}
		return err
	}
	return writeAtomicHostFile(tailscalePrivacyAppliedPath(environment.overridePath), tailscalePrivacyAppliedMarker, 0644)
}
func reconcileOpenRCTailscaleEndpoint(ctx context.Context, endpoints []string, environment tailscaleEndpointEnvironment) error {
	if len(endpoints) > 1 {
		return errors.New("one static Tailscale endpoint required")
	}
	var config []byte
	var body string
	var err error
	if len(endpoints) > 0 {
		config, err = tailscalehost.RenderConfig(endpoints)
		if err != nil {
			return err
		}
		if environment.configPath != "/etc/vastora/tailscaled.json" {
			return errors.New("managed OpenRC endpoint configuration path required")
		}
		body = "port=41641\ncommand_args=\"${command_args} --config=/etc/vastora/tailscaled.json\""
	}
	beforeConfig, err := inspectHostFile(environment.configPath)
	if err != nil {
		return err
	}
	beforeOverride, err := inspectHostFile(environment.overridePath)
	if err != nil {
		return err
	}
	desired, err := renderOpenRCTailscaleSection(string(beforeOverride.content), "ENDPOINT", body)
	if err != nil {
		return err
	}
	if string(config) == string(beforeConfig.content) && desired == string(beforeOverride.content) {
		return verifyTailscaleEndpointHealth(ctx, len(config) > 0, environment.configPath, environment.run)
	}
	rollback := func(cause error) error {
		restoreErr := errors.Join(restoreHostFile(environment.configPath, beforeConfig), restoreHostFile(environment.overridePath, beforeOverride))
		restartErr := tailscalehost.OpenRCAction(context.WithoutCancel(ctx), environment.run, "restart")
		return errors.Join(cause, restoreErr, restartErr)
	}
	if err := applyHostFile(environment.configPath, config, 0644); err != nil {
		return err
	}
	if _, err := applyOpenRCTailscaleSection(environment.overridePath, "ENDPOINT", body); err != nil {
		return rollback(err)
	}
	if err := tailscalehost.OpenRCAction(ctx, environment.run, "restart"); err != nil {
		return rollback(err)
	}
	if err := verifyTailscaleEndpointHealth(ctx, len(config) > 0, environment.configPath, environment.run); err != nil {
		return rollback(fmt.Errorf("verify OpenRC endpoint: %w", err))
	}
	return nil
}
