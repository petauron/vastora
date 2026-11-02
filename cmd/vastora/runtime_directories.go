package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

const runtimeDirectoriesUnitName = "vastora-runtime-directories.service"
const runtimeDirectoriesUnitPath = "/etc/systemd/system/" + runtimeDirectoriesUnitName

// This platform resource belongs to Vastora, not to its first website. It only
// prepares local directories: no Docker mutation, task replay or Center access.
func runtimeDirectoriesUnit(executable string) string {
	return "# Managed by Vastora\n# Resource: runtime-directories\n" +
		"[Unit]\nDescription=Vastora runtime directories\nDefaultDependencies=no\n" +
		"After=local-fs.target\nBefore=sysinit.target docker.service vastora-agent.service\n" +
		"Conflicts=shutdown.target\nBefore=shutdown.target\n\n[Service]\nType=oneshot\n" +
		"ExecStart=" + strconv.Quote(executable) + " agent prepare-runtime-directories\n" +
		"RemainAfterExit=yes\n\n[Install]\nWantedBy=sysinit.target\n"
}

func readRuntimeDirectoriesUnit() ([]byte, error) {
	if err := validateRuntimeUnitParents(); err != nil {
		return nil, err
	}
	info, err := os.Lstat(runtimeDirectoriesUnitPath)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() > 16<<10 {
		return nil, errors.New("runtime directory unit has an unsafe type, owner or permissions")
	}
	raw, err := os.ReadFile(runtimeDirectoriesUnitPath)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "ExecStart=") || !strings.HasSuffix(line, " agent prepare-runtime-directories") {
			continue
		}
		quoted := strings.TrimSuffix(strings.TrimPrefix(line, "ExecStart="), " agent prepare-runtime-directories")
		executable, err := strconv.Unquote(quoted)
		if err == nil && filepath.IsAbs(executable) && bytes.Equal(raw, []byte(runtimeDirectoriesUnit(executable))) {
			return raw, nil
		}
	}
	return nil, errors.New("refusing to change an unowned runtime directory unit")
}

func validateRuntimeUnitParents() error {
	for _, path := range []string{"/etc", "/etc/systemd", "/etc/systemd/system"} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("unsafe systemd directory for managed runtime resources")
		}
	}
	return nil
}

func installRuntimeDirectoriesUnit(executable string) (result error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable || strings.ContainsAny(executable, "\n\r\x00%$") {
		return errors.New("unsafe executable path for runtime directory unit")
	}
	if err := gatewayruntime.PrepareAdminDirectory(gatewayruntime.CaddyAdminSocket); err != nil {
		return err
	}
	old, err := readRuntimeDirectoriesUnit()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	wanted := []byte(runtimeDirectoriesUnit(executable))
	if !bytes.Equal(old, wanted) {
		if err := writeRootFileAtomic(runtimeDirectoriesUnitPath, wanted, 0o644); err != nil {
			return err
		}
		// A failed registration must not leave a new boot unit pointing at a
		// command that the host updater will subsequently roll back.
		defer func() {
			if result == nil {
				return
			}
			if len(old) != 0 {
				result = errors.Join(result, writeRootFileAtomic(runtimeDirectoriesUnitPath, old, 0o644))
			} else {
				result = errors.Join(result, exec.Command("systemctl", "disable", runtimeDirectoriesUnitName).Run())
				if err := os.Remove(runtimeDirectoriesUnitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
					result = errors.Join(result, err)
				}
			}
			result = errors.Join(result, exec.Command("systemctl", "daemon-reload").Run())
		}()
		if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
			return fmt.Errorf("reload runtime directory unit: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	if exec.Command("systemctl", "is-enabled", "--quiet", runtimeDirectoriesUnitName).Run() != nil {
		if out, err := exec.Command("systemctl", "enable", runtimeDirectoriesUnitName).CombinedOutput(); err != nil {
			return fmt.Errorf("enable runtime directory unit: %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	return nil
}

func removeRuntimeDirectoriesUnit(ctx context.Context) error {
	if _, err := readRuntimeDirectoriesUnit(); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	directory := filepath.Dir(gatewayruntime.CaddyAdminSocket)
	if _, err := os.Lstat("/var/run/docker.sock"); err == nil {
		docker, err := client.New(client.WithHost("unix:///var/run/docker.sock"))
		if err != nil {
			return err
		}
		defer docker.Close()
		check, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		containers, err := docker.ContainerList(check, client.ContainerListOptions{All: true})
		if err != nil {
			return fmt.Errorf("inspect runtime directory consumers: %w", err)
		}
		for _, item := range containers.Items {
			for _, mount := range item.Mounts {
				if mount.Source == directory || strings.HasPrefix(mount.Source, directory+"/") {
					return errors.New("shared runtime directory still has a container consumer; retain its unit and host command")
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if err := gatewayruntime.PrepareAdminDirectory(gatewayruntime.CaddyAdminSocket); err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != filepath.Base(gatewayruntime.CaddyAdminSocket) {
				return errors.New("unrecognized file prevents runtime directory removal")
			}
		}
	}
	if out, err := exec.CommandContext(ctx, "systemctl", "disable", "--now", runtimeDirectoriesUnitName).CombinedOutput(); err != nil {
		return fmt.Errorf("disable runtime directory unit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	for _, path := range []string{gatewayruntime.CaddyAdminSocket, directory, runtimeDirectoriesUnitPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
}

func runRuntimeDirectories(arguments []string) error {
	if len(arguments) != 1 || runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("managed runtime directory operations require root on Linux and no extra arguments")
	}
	switch arguments[0] {
	case "prepare-runtime-directories":
		return gatewayruntime.PrepareAdminDirectory(gatewayruntime.CaddyAdminSocket)
	case "install-runtime-directories":
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		return installRuntimeDirectoriesUnit(executable)
	case "uninstall-runtime-directories":
		return removeRuntimeDirectoriesUnit(context.Background())
	default:
		return errors.New("unknown runtime directory operation")
	}
}
