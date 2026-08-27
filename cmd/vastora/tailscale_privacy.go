package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	tailscalePrivacyOverride      = "[Service]\nEnvironment=TS_NO_LOGS_NO_SUPPORT=true\n"
	tailscalePrivacyAppliedMarker = "v1\n"
)

func tailscalePrivacyAppliedPath(path string) string {
	return path + ".applied"
}

func reconcileTailscalePrivacy(path string, run func(context.Context, string, ...string) ([]byte, error)) error {
	appliedPath := tailscalePrivacyAppliedPath(path)
	current, fileErr := os.ReadFile(path)
	applied, markerErr := os.ReadFile(appliedPath)
	if fileErr == nil && markerErr == nil && string(current) == tailscalePrivacyOverride && string(applied) == tailscalePrivacyAppliedMarker {
		return nil
	}
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		return fmt.Errorf("inspect Tailscale privacy override: %w", fileErr)
	}
	if markerErr != nil && !errors.Is(markerErr, os.ErrNotExist) {
		return fmt.Errorf("inspect Tailscale privacy reconciliation marker: %w", markerErr)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create Tailscale systemd override directory: %w", err)
	}
	if string(current) != tailscalePrivacyOverride {
		if err := writeAtomicHostFile(path, tailscalePrivacyOverride, 0o644); err != nil {
			return fmt.Errorf("install Tailscale privacy override: %w", err)
		}
	}
	_ = os.Remove(appliedPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if output, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("reload systemd for Tailscale privacy: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if output, err := run(ctx, "systemctl", "restart", "tailscaled.service"); err != nil {
		return fmt.Errorf("restart Tailscale with log uploads disabled: %s: %w", strings.TrimSpace(string(output)), err)
	}
	if err := writeAtomicHostFile(appliedPath, tailscalePrivacyAppliedMarker, 0o644); err != nil {
		return fmt.Errorf("record applied Tailscale privacy state: %w", err)
	}
	return nil
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
