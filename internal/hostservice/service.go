// Package hostservice implements the supported Linux init systems without
// emulating another init system's CLI or accepting remote shell commands.
package hostservice

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Kind string

const (
	Systemd Kind = "systemd"
	OpenRC  Kind = "openrc"
)

type Command func(context.Context, string, ...string) ([]byte, error)

func Detect() (Kind, error) { return DetectAt("") }
func DetectAt(root string) (Kind, error) {
	at := func(p string) string { return filepath.Join(root, p) }
	if s, err := os.Stat(at("/run/systemd/system")); err == nil && s.IsDir() {
		return Systemd, nil
	}
	if s, err := os.Stat(at("/run/openrc")); err == nil && s.IsDir() {
		for _, p := range []string{"/sbin/openrc-run", "/sbin/rc-service", "/sbin/rc-update", "/sbin/supervise-daemon"} {
			s, err := os.Stat(at(p))
			if err != nil || !s.Mode().IsRegular() || s.Mode().Perm()&0111 == 0 {
				return "", errors.New("hostservice: OpenRC supervision tools unavailable")
			}
		}
		return OpenRC, nil
	}
	return "", errors.New("hostservice: a running systemd or OpenRC host is required")
}

func Name(unit string) (string, error) {
	if !strings.HasSuffix(unit, ".service") {
		return "", errors.New("hostservice: concrete service identifier required")
	}
	name := strings.TrimSuffix(unit, ".service")
	if len(name) == 0 || len(name) > 128 || name[0] == '-' {
		return "", errors.New("hostservice: invalid service identifier")
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", errors.New("hostservice: unsafe service identifier")
		}
	}
	return name, nil
}
func Path(kind Kind, unit string) (string, error) {
	n, e := Name(unit)
	if e != nil {
		return "", e
	}
	switch kind {
	case Systemd:
		return filepath.Join("/etc/systemd/system", unit), nil
	case OpenRC:
		return filepath.Join("/etc/init.d", n), nil
	}
	return "", errors.New("hostservice: unsupported init system")
}
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
func Action(ctx context.Context, kind Kind, run Command, action, unit string) error {
	if run == nil {
		run = Exec
	}
	if action == "reload" {
		switch kind {
		case OpenRC:
			return nil
		case Systemd:
			_, e := run(ctx, "systemctl", "daemon-reload")
			return e
		default:
			return errors.New("hostservice: unsupported init system")
		}
	}
	name, err := Name(unit)
	if err != nil {
		return err
	}
	switch kind {
	case Systemd:
		if action == "status" {
			action = "is-active"
		}
		switch action {
		case "start", "stop", "restart", "enable", "disable", "is-active":
		default:
			return errors.New("hostservice: unsupported action")
		}
		_, err = run(ctx, "systemctl", action, unit)
	case OpenRC:
		switch action {
		case "enable":
			_, err = run(ctx, "rc-update", "add", name, "default")
		case "disable":
			// rc-update del fails if an already-disabled service has no membership.
			link := filepath.Join("/etc/runlevels/default", name)
			if _, e := os.Lstat(link); errors.Is(e, os.ErrNotExist) {
				return nil
			} else if e != nil {
				return e
			}
			_, err = run(ctx, "rc-update", "del", name, "default")
		case "start", "stop", "restart", "status":
			_, err = run(ctx, "rc-service", name, action)
		default:
			return errors.New("hostservice: unsupported action")
		}
	default:
		return errors.New("hostservice: unsupported init system")
	}
	if err != nil {
		return fmt.Errorf("hostservice: %s %s failed: %w", action, unit, err)
	}
	return nil
}

// Invocation identifies a supervised process across PID reuse and reboot. It
// is derived from the kernel's boot identity and process start ticks, never a
// timestamp guessed from a successful start command.
type Invocation struct {
	PID         int
	ID, Started string
}

func OpenRCInvocation(ctx context.Context, run Command, unit string) (Invocation, error) {
	name, err := Name(unit)
	if err != nil {
		return Invocation{}, err
	}
	if run == nil {
		run = Exec
	}
	raw, err := run(ctx, "rc-service", name, "vastora_pid")
	if err != nil || len(raw) > 64 {
		return Invocation{}, errors.New("hostservice: supervised PID unavailable")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid < 0 {
		return Invocation{}, errors.New("hostservice: malformed supervised PID")
	}
	if pid == 0 {
		return Invocation{}, nil
	}
	return ProcessInvocation("", pid)
}
func ProcessInvocation(root string, pid int) (Invocation, error) {
	if pid <= 1 {
		return Invocation{}, errors.New("hostservice: invalid process PID")
	}
	raw, err := os.ReadFile(filepath.Join(root, "/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return Invocation{}, err
	}
	end := strings.LastIndex(string(raw), ") ")
	if end < 0 {
		return Invocation{}, errors.New("hostservice: malformed process stat")
	}
	fields := strings.Fields(string(raw)[end+2:])
	if len(fields) < 20 || fields[0] == "Z" {
		return Invocation{}, errors.New("hostservice: unavailable process generation")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return Invocation{}, errors.New("hostservice: invalid process start time")
	}
	boot, err := os.ReadFile(filepath.Join(root, "/proc/sys/kernel/random/boot_id"))
	if err != nil {
		return Invocation{}, err
	}
	id := strings.TrimSpace(string(boot))
	if len(id) != 36 || strings.ContainsAny(id, "\r\n /") {
		return Invocation{}, errors.New("hostservice: invalid boot identity")
	}
	return Invocation{PID: pid, ID: id + ":" + strconv.Itoa(pid) + ":" + fields[19], Started: fields[19]}, nil
}

// Quote is used solely for executor-generated init scripts. Configuration and
// credential values are passed through JSON/execve, never sourced by a shell.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
