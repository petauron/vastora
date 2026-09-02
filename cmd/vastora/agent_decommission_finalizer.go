package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func hostDecommissionServiceUnit() string {
	return "[Unit]\nDescription=Vastora Agent host decommission\nWants=network-online.target\nAfter=network-online.target\nStartLimitIntervalSec=0\n\n[Service]\nType=simple\nExecStart=" + hostDecommissionBinary + " agent finish-decommission --operation-file " + hostDecommissionOperationPath + "\nRestart=on-failure\nRestartSec=5s\n\n[Install]\nWantedBy=multi-user.target\n"
}

func checkHostDecommissionOwnership(unitPath, operationPath, generatorPath string, operation hostDecommissionOperation) error {
	if _, err := os.Lstat(generatorPath); err == nil {
		return errors.New("agent: previous host cleanup is already finalizing")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if info, err := os.Lstat(unitPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
			return errors.New("agent: refusing to overwrite an unprotected host cleanup service")
		}
		current, err := os.ReadFile(unitPath)
		if err != nil {
			return err
		}
		if string(current) != hostDecommissionServiceUnit() {
			return errors.New("agent: existing host cleanup service is finalizing or is not owned by this helper")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(operationPath); err == nil {
		previous, err := readHostDecommissionOperation(operationPath)
		if err != nil {
			return err
		}
		if previous != operation {
			return errors.New("agent: refusing to replace a pending host cleanup operation")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		for _, name := range []string{"result.json", "completed"} {
			if _, err := os.Lstat(filepath.Join(filepath.Dir(operationPath), name)); err == nil {
				return errors.New("agent: host cleanup result exists without its operation")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// After Center acknowledges the result, a systemd generator becomes the
// durable finalization record. It is removed only after all other removals
// have been synced, so a reboot can resume even without the original unit.
// Type=oneshot stops at the first failing command; on-failure retries it:
// https://github.com/systemd/systemd/blob/main/man/systemd.service.xml
func hostDecommissionFinalizerCommands(directory, unitPath, enabledLink, generatorPath string) [][]string {
	commands := make([][]string, 0, 14)
	for _, name := range []string{"result.json", "operation.json", "completed", "vastora"} {
		commands = append(commands, []string{"/usr/bin/rm", "-f", "--", filepath.Join(directory, name)})
	}
	commands = append(commands,
		// Do not recursively remove the directory: unexpected files are not
		// owned by the finalizer and must keep it in the failed/retry state.
		[]string{"/bin/sh", "-ec", "if [ -e \"$1\" ] || [ -L \"$1\" ]; then rmdir -- \"$1\"; fi", "vastora-finalizer", directory},
		[]string{"/usr/bin/rm", "-f", "--", enabledLink},
		[]string{"/usr/bin/rm", "-f", "--", unitPath},
		[]string{"/usr/bin/sync", "-f", filepath.Dir(directory)},
		[]string{"/usr/bin/sync", "-f", filepath.Dir(enabledLink)},
		[]string{"/usr/bin/sync", "-f", filepath.Dir(unitPath)},
		[]string{"/usr/bin/rm", "-f", "--", generatorPath},
		[]string{"/usr/bin/sync", "-f", filepath.Dir(generatorPath)},
		[]string{"/usr/bin/systemctl", "daemon-reload"},
	)
	return commands
}

func hostDecommissionFinalizerUnit(directory, unitPath, enabledLink, generatorPath string) string {
	var unit strings.Builder
	unit.WriteString("[Unit]\nDescription=Vastora Agent host cleanup finalizer\nStartLimitIntervalSec=0\n\n[Service]\nType=oneshot\nTimeoutStartSec=0\n")
	for _, command := range hostDecommissionFinalizerCommands(directory, unitPath, enabledLink, generatorPath) {
		unit.WriteString("ExecStart=")
		for index, argument := range command {
			if index != 0 {
				unit.WriteByte(' ')
			}
			// systemd expands specifiers and environment variables even in
			// quoted arguments. Leave the shell's positional $1 untouched.
			argument = strings.ReplaceAll(strings.ReplaceAll(argument, "%", "%%"), "$", "$$")
			unit.WriteString(strconv.Quote(argument))
		}
		unit.WriteByte('\n')
	}
	unit.WriteString("Restart=on-failure\nRestartSec=5s\n\n[Install]\nWantedBy=multi-user.target\n")
	return unit.String()
}

// The generator writes only systemd's supplied runtime output directory.
// The early directory intentionally replaces this helper's own unit, including
// after its persistent unit/link disappear. No unrelated service is changed.
// https://github.com/systemd/systemd/blob/main/man/systemd.generator.xml
func hostDecommissionGeneratorScript(finalizer string) string {
	quoted := "'" + strings.ReplaceAll(finalizer, "'", "'\\''") + "'"
	return "#!/bin/sh\nset -eu\noutput=\"${2:-$1}\"\nmkdir -p -- \"$output/multi-user.target.wants\"\nprintf '%s' " + quoted + " > \"$output/" + hostDecommissionUnitName + "\"\nln -sfn -- ../" + hostDecommissionUnitName + " \"$output/multi-user.target.wants/" + hostDecommissionUnitName + "\"\n"
}

func activateHostDecommissionFinalizer(ctx context.Context, unitPath, generatorPath, generator string, run func(context.Context, string, ...string) ([]byte, error)) error {
	info, err := os.Lstat(unitPath)
	if err != nil {
		return fmt.Errorf("agent: inspect host cleanup service before finalization: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return errors.New("agent: host cleanup service is not a protected regular file")
	}
	current, err := os.ReadFile(unitPath)
	if err != nil {
		return err
	}
	if string(current) != hostDecommissionServiceUnit() {
		return errors.New("agent: refusing to replace an unrelated host cleanup service")
	}
	if info, err := os.Lstat(generatorPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 {
			return errors.New("agent: host cleanup generator is not a protected executable")
		}
		current, err := os.ReadFile(generatorPath)
		if err != nil {
			return err
		}
		if string(current) != generator {
			return errors.New("agent: refusing to replace an unrelated host cleanup generator")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else if err := writeRootFileAtomic(generatorPath, []byte(generator), 0o700); err != nil {
		return fmt.Errorf("agent: persist independent host cleanup finalizer: %w", err)
	}
	// Persist a newly created generator directory as well as the file before
	// reloading systemd can start deleting the previous recovery records.
	if output, err := run(ctx, "sync", "-f", filepath.Dir(generatorPath)); err != nil {
		return fmt.Errorf("agent: sync host cleanup finalizer: %s: %w", strings.TrimSpace(string(output)), err)
	}
	for _, arguments := range [][]string{{"daemon-reload"}, {"restart", "--no-block", hostDecommissionUnitName}} {
		if output, err := run(ctx, "systemctl", arguments...); err != nil {
			return fmt.Errorf("agent: activate host cleanup finalizer: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	return nil
}
