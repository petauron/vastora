package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/tailscalehost"
)

type tailscaleEndpointEnvironment struct {
	configPath   string
	overridePath string
	run          func(context.Context, string, ...string) ([]byte, error)
}

type hostFileSnapshot struct {
	exists  bool
	content []byte
	mode    os.FileMode
}

func defaultTailscaleEndpointEnvironment() tailscaleEndpointEnvironment {
	return tailscaleEndpointEnvironment{
		configPath:   "/etc/vastora/tailscaled.json",
		overridePath: "/etc/systemd/system/tailscaled.service.d/91-vastora-endpoint.conf",
		run:          runHostCommand,
	}
}

func reconcileTailscaleEndpoint(ctx context.Context, staticEndpoints []string, environment tailscaleEndpointEnvironment) error {
	if environment.run == nil {
		return errors.New("configure Tailscale static endpoint: host command runner is required")
	}
	if len(staticEndpoints) > 1 {
		return errors.New("configure Tailscale static endpoint: exactly one Center endpoint is supported")
	}
	var expectedConfig []byte
	var err error
	if len(staticEndpoints) > 0 {
		expectedConfig, err = tailscalehost.RenderConfig(staticEndpoints)
		if err != nil {
			return fmt.Errorf("configure Tailscale static endpoint: %w", err)
		}
	}
	expectedOverride := []byte(nil)
	if len(expectedConfig) > 0 {
		expectedOverride, err = renderTailscaleEndpointOverride(environment.configPath)
		if err != nil {
			return err
		}
	}
	configSnapshot, err := inspectHostFile(environment.configPath)
	if err != nil {
		return fmt.Errorf("inspect Tailscale static endpoint config: %w", err)
	}
	overrideSnapshot, err := inspectHostFile(environment.overridePath)
	if err != nil {
		return fmt.Errorf("inspect Tailscale static endpoint override: %w", err)
	}
	if hostFileMatches(configSnapshot, expectedConfig) && hostFileMatches(overrideSnapshot, expectedOverride) {
		healthContext, healthCancel := context.WithTimeout(ctx, 15*time.Second)
		healthErr := verifyTailscaleEndpointHealth(healthContext, len(expectedConfig) > 0, environment.configPath, environment.run)
		healthCancel()
		if healthErr == nil {
			return nil
		}
	}
	commandContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := tailscalehost.CheckCompatibility(commandContext, environment.run, false); err != nil {
		return fmt.Errorf("verify Vastora-managed Tailscale compatibility: %w", err)
	}
	rollback := func(cause error) error {
		rollbackContext, rollbackCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer rollbackCancel()
		var rollbackErrors []error
		if restoreErr := restoreHostFile(environment.configPath, configSnapshot); restoreErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore Tailscale config: %w", restoreErr))
		}
		if restoreErr := restoreHostFile(environment.overridePath, overrideSnapshot); restoreErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore Tailscale override: %w", restoreErr))
		}
		if output, reloadErr := environment.run(rollbackContext, "systemctl", "daemon-reload"); reloadErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("reload restored Tailscale service: %s: %w", strings.TrimSpace(string(output)), reloadErr))
		}
		if output, restartErr := environment.run(rollbackContext, "systemctl", "restart", "tailscaled.service"); restartErr != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restart restored Tailscale service: %s: %w", strings.TrimSpace(string(output)), restartErr))
		}
		if len(rollbackErrors) == 0 {
			return cause
		}
		return errors.Join(cause, fmt.Errorf("rollback Tailscale static endpoint: %w", errors.Join(rollbackErrors...)))
	}
	if err := applyHostFile(environment.configPath, expectedConfig, 0o644); err != nil {
		return rollback(fmt.Errorf("install Tailscale static endpoint config: %w", err))
	}
	if err := applyHostFile(environment.overridePath, expectedOverride, 0o644); err != nil {
		return rollback(fmt.Errorf("install Tailscale static endpoint override: %w", err))
	}
	if output, reloadErr := environment.run(commandContext, "systemctl", "daemon-reload"); reloadErr != nil {
		return rollback(fmt.Errorf("reload Tailscale static endpoint service: %s: %w", strings.TrimSpace(string(output)), reloadErr))
	}
	if output, restartErr := environment.run(commandContext, "systemctl", "restart", "tailscaled.service"); restartErr != nil {
		return rollback(fmt.Errorf("restart Tailscale with static endpoint: %s: %w", strings.TrimSpace(string(output)), restartErr))
	}
	if healthErr := verifyTailscaleEndpointHealth(commandContext, len(expectedConfig) > 0, environment.configPath, environment.run); healthErr != nil {
		return rollback(healthErr)
	}
	return nil
}

func verifyTailscaleEndpointHealth(ctx context.Context, requireFixedPort bool, configPath string, run func(context.Context, string, ...string) ([]byte, error)) error {
	if err := tailscalehost.CheckCompatibility(ctx, run, true); err != nil {
		return fmt.Errorf("verify Tailscale static endpoint compatibility: %w", err)
	}
	if requireFixedPort {
		output, err := run(ctx, "systemctl", "show", "--property=Environment", "--value", "tailscaled.service")
		if err != nil {
			return fmt.Errorf("verify Tailscale static endpoint environment: %s: %w", strings.TrimSpace(string(output)), err)
		}
		environment := strings.Fields(string(output))
		if !slicesContain(environment, "PORT=41641") || !slicesContain(environment, "FLAGS=--config="+configPath) {
			return errors.New("verify Tailscale static endpoint environment: managed fixed-port settings are not loaded")
		}
		output, err = run(ctx, "ss", "-H", "-lun", "sport = :41641")
		if err != nil {
			return fmt.Errorf("verify Tailscale static endpoint listener: %s: %w", strings.TrimSpace(string(output)), err)
		}
		if !strings.Contains(string(output), ":41641") {
			return errors.New("verify Tailscale static endpoint listener: UDP 41641 is not listening")
		}
	}
	return nil
}

func slicesContain(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func renderTailscaleEndpointOverride(configPath string) ([]byte, error) {
	if !filepath.IsAbs(configPath) || strings.ContainsAny(configPath, "\r\n\"") {
		return nil, errors.New("configure Tailscale static endpoint: managed config path is invalid")
	}
	return []byte("[Service]\nEnvironment=\"PORT=41641\"\nEnvironment=\"FLAGS=--config=" + configPath + "\"\n"), nil
}

func inspectHostFile(path string) (hostFileSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return hostFileSnapshot{}, nil
	}
	if err != nil {
		return hostFileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return hostFileSnapshot{}, errors.New("managed host path is not a regular file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return hostFileSnapshot{}, err
	}
	return hostFileSnapshot{exists: true, content: content, mode: info.Mode().Perm()}, nil
}

func hostFileMatches(snapshot hostFileSnapshot, expected []byte) bool {
	if len(expected) == 0 {
		return !snapshot.exists
	}
	return snapshot.exists && bytes.Equal(snapshot.content, expected)
}

func applyHostFile(path string, expected []byte, mode os.FileMode) error {
	if len(expected) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomicHostFile(path, string(expected), mode)
}

func restoreHostFile(path string, snapshot hostFileSnapshot) error {
	if !snapshot.exists {
		return applyHostFile(path, nil, 0)
	}
	return applyHostFile(path, snapshot.content, snapshot.mode)
}
