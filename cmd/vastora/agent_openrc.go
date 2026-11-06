package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/petauron/vastora/internal/hostservice"
)

const openRCUpdatePID = "/run/vastora-agent-update.pid"
const openRCUpdateUnit = "/etc/init.d/vastora-agent-update"

func isOpenRCHost() bool {
	kind, err := hostservice.Detect()
	return err == nil && kind == hostservice.OpenRC
}
func agentServiceAction(ctx context.Context, action string) error {
	kind, err := hostservice.Detect()
	if err != nil {
		return err
	}
	return hostservice.Action(ctx, kind, nil, action, "vastora-agent.service")
}
func hostUpdateServiceAction(ctx context.Context, environment hostUpdateActivationEnvironment, action string) error {
	if environment.initSystem == hostservice.OpenRC {
		return hostservice.Action(ctx, hostservice.OpenRC, environment.run, action, "vastora-agent.service")
	}
	_, err := environment.run(ctx, "systemctl", action, "vastora-agent.service")
	return err
}
func openRCHostUpdateService() string {
	return "#!/sbin/openrc-run\n# Managed by Vastora\n# Resource: agent-update\n" +
		"description='Vastora Agent update'\ncommand=" + hostservice.Quote(hostUpdateBinary) + "\n" +
		"command_args='agent finish-update --operation-file " + hostUpdateOperationPath + "'\n" +
		"command_background=true\npidfile=" + hostservice.Quote(openRCUpdatePID) + "\numask=0077\n" +
		"depend() { need net; }\n"
}

// A oneshot update runs outside the supervised Agent process, so stopping the
// Agent cannot kill its updater. Inspect both PID and executable/argv before
// clearing OpenRC state; do not zap a running helper or signal an unknown PID.
func stoppedOpenRCUpdateHelper() error { return inspectOpenRCUpdateHelper(false) }

func inspectOpenRCUpdateHelper(allowSelf bool) error {
	info, err := os.Lstat(openRCUpdatePID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 32 {
		return errors.New("agent: unsafe OpenRC update PID record")
	}
	raw, err := os.ReadFile(openRCUpdatePID)
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 1 {
		return errors.New("agent: malformed OpenRC update PID")
	}
	root := filepath.Join("/proc", strconv.Itoa(pid))
	executable, err := os.Readlink(filepath.Join(root, "exe"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return errors.New("agent: cannot inspect OpenRC update process")
	}
	arguments, err := os.ReadFile(filepath.Join(root, "cmdline"))
	if err != nil {
		return err
	}
	if strings.TrimSuffix(executable, " (deleted)") != hostUpdateBinary || !bytes.Contains(arguments, []byte("agent\x00finish-update\x00")) {
		return errors.New("agent: OpenRC update PID was reused; inspect before staging")
	}
	if allowSelf && pid == os.Getpid() {
		return nil
	}
	return errors.New("agent: previous OpenRC update helper is still running")
}
func startOpenRCUpdateHelper(ctx context.Context, run hostservice.Command) error {
	if err := stoppedOpenRCUpdateHelper(); err != nil {
		return err
	}
	name := "vastora-agent-update"
	// The helper is intentionally not a boot service. Interrupted database
	// activation retains its protected recovery and requires Center disposition.
	if err := hostservice.Action(ctx, hostservice.OpenRC, run, "disable", hostUpdateUnitName); err != nil {
		return err
	}
	if _, err := run(ctx, "rc-service", name, "zap"); err != nil {
		return fmt.Errorf("agent: clear stopped OpenRC helper state: %w", err)
	}
	_, err := run(ctx, "rc-service", name, "start")
	return err
}

// An existing init script must be the exact root-owned helper before staging
// or cleanup. A matching filename alone never authorizes replacement/removal.
func verifyOpenRCUpdateService(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 64<<10 {
		return errors.New("agent: unsafe OpenRC update service")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(raw) != openRCHostUpdateService() {
		return errors.New("agent: unowned OpenRC update service; inspect before replacement")
	}
	return nil
}

func cleanOpenRCUpdateService() error {
	if err := verifyOpenRCUpdateService(openRCUpdateUnit); err != nil {
		return err
	}
	if err := inspectOpenRCUpdateHelper(true); err != nil {
		return err
	}
	if err := os.Remove(openRCUpdatePID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
