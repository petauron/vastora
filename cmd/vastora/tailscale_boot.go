package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/petauron/vastora/internal/hostservice"
)

const tailscaleBootDropin = "/etc/systemd/system/tailscaled.service.d/92-vastora-network-online.conf"
const tailscaleBootContent = "# Managed by Vastora: Tailscale network readiness\n[Unit]\nWants=network-online.target\nAfter=network-online.target\n"

func manageTailscaleBootOrder(ctx context.Context, path string, install bool, run func(context.Context, string, ...string) ([]byte, error)) error {
	if !install {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	var wanted []byte
	if install {
		wanted = []byte(tailscaleBootContent)
	}
	return hostservice.EnsureSystemdDropin(ctx, filepath.Dir(path), filepath.Base(path), wanted,
		func(raw []byte) bool { return bytes.Equal(raw, []byte(tailscaleBootContent)) },
		func(ctx context.Context, name string, args ...string) error {
			out, err := run(ctx, name, args...)
			if err != nil {
				return fmt.Errorf("reload systemd: %s: %w", strings.TrimSpace(string(out)), err)
			}
			return nil
		})
}
