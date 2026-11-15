package tailscalehost

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/petauron/vastora/internal/hostservice"
)

func OpenRCHost() bool { k, e := hostservice.Detect(); return e == nil && k == hostservice.OpenRC }
func OpenRCAction(ctx context.Context, run hostservice.Command, action string) error {
	return hostservice.Action(ctx, hostservice.OpenRC, run, action, "tailscale.service")
}
func OpenRCActive(ctx context.Context, run hostservice.Command) (bool, error) {
	_, err := run(ctx, "rc-service", "tailscale", "status")
	if err == nil {
		return true, nil
	}
	var status interface{ ExitCode() int }
	if errors.As(err, &status) && status.ExitCode() == 3 {
		return false, nil
	}
	return false, errors.New("tailscale: OpenRC service state unavailable")
}

// RuntimeEnvironment reads evidence from the installed daemon itself. A
// conf.d file is not proof that its settings reached an already-running daemon.
// Output contains only the non-secret settings checked by Vastora.
func OpenRCRuntimeEnvironment() ([]byte, error) {
	executable, err := os.Stat("/usr/sbin/tailscaled")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var found []byte
	count := 0
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		base := filepath.Join("/proc", entry.Name())
		running, err := os.Stat(filepath.Join(base, "exe"))
		if err != nil || !os.SameFile(executable, running) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(base, "environ"))
		if err != nil || len(raw) > 1<<20 {
			return nil, errors.New("tailscale: daemon environment unavailable")
		}
		args, err := os.ReadFile(filepath.Join(base, "cmdline"))
		if err != nil || len(args) > 64<<10 {
			return nil, errors.New("tailscale: daemon arguments unavailable")
		}
		found = normalizedOpenRCTailscaleEnvironment(raw, args)
		count++
	}
	if count != 1 {
		return nil, errors.New("tailscale: unique installed OpenRC daemon required")
	}
	return found, nil
}
func normalizedOpenRCTailscaleEnvironment(environment, args []byte) []byte {
	var settings []string
	if bytes.Contains(append([]byte{0}, environment...), []byte("\x00TS_NO_LOGS_NO_SUPPORT=true\x00")) || bytes.Contains(append(args, 0), []byte("\x00--no-logs-no-support\x00")) {
		settings = append(settings, "TS_NO_LOGS_NO_SUPPORT=true")
	}
	for _, arg := range strings.Split(string(args), "\x00") {
		if strings.HasPrefix(arg, "--port=") {
			settings = append(settings, "PORT="+strings.TrimPrefix(arg, "--port="))
		}
		if strings.HasPrefix(arg, "--config=") {
			settings = append(settings, "FLAGS="+arg)
		}
	}
	return []byte(strings.Join(settings, " "))
}
