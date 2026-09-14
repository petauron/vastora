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

// A new Center-authorized execution owns fresh staging state, not the files
// left by a different attempt. Remove obsolete staging, without retaining a
// backup per attempt or discarding unresolved recovery points. Center still
// requires explicit disposition before issuing an execution after failure.
func prepareHostUpdateDirectory(ctx context.Context, directory string, operation hostUpdateOperation, run func(context.Context, string, ...string) ([]byte, error), version func(context.Context, string) (string, error)) error {
	if err := validateHostUpdateOperation(operation); err != nil {
		return err
	}
	if operation.ExecutionID == "" || operation.SessionID == "" {
		return errors.New("agent: staging an update requires a Center execution authorization")
	}
	if _, err := stoppedHostUpdateHelper(ctx, run); err != nil {
		return err
	}
	if info, err := os.Lstat(directory); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
			return errors.New("agent: update staging directory is not protected")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	operationPath := filepath.Join(directory, "operation.json")
	if cancelled, err := hostUpdateCancelled(operationPath); err != nil {
		return err
	} else if cancelled {
		return errors.New("agent: uninstall cancelled the pending Agent update")
	}
	previous, err := readHostUpdateOperation(operationPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && previous != operation {
		if previous.ExecutionID == operation.ExecutionID || previous.AgentID != operation.AgentID || previous.DataDir != operation.DataDir || previous.Executable != operation.Executable {
			return errors.New("agent: previous update ownership does not match the authorized replacement")
		}
		installed, err := version(ctx, operation.Executable)
		if err != nil || installed != operation.SourceVersion {
			return errors.New("agent: installed executable differs from the running Agent; inspect update recovery before replacement")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := clearStoppedHostUpdateStaging(directory); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("agent: create persistent update directory: %w", err)
	}
	return os.Chmod(directory, 0o700)
}

func clearStoppedHostUpdateStaging(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	// Validate every entry before deleting anything. A recovery directory or
	// unknown file requires explicit maintenance, not automatic data removal.
	allowed := map[string]bool{"operation.json": true, "result.json": true, "completed": true, "vastora": true}
	for _, entry := range entries {
		info, err := os.Lstat(filepath.Join(directory, entry.Name()))
		if err != nil || !allowed[entry.Name()] || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("agent: update staging contains protected recovery or unexpected files; explicit maintenance required")
		}
	}
	for _, entry := range entries {
		if entry.Name() == "operation.json" {
			continue
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	// Persist removal of stale results before releasing ownership. If interrupted,
	// the old operation still identifies the remaining files for explicit retry.
	if err := syncHostUpdateDirectory(directory); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, "operation.json")); err != nil {
		return err
	}
	return syncHostUpdateDirectory(directory)
}

// Querying an inactive/not-loaded unit is valid; resetting a not-loaded unit
// is not. MainPID and ControlPID also cover ExecStopPost cleanup ownership.
func stoppedHostUpdateHelper(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error)) (bool, error) {
	checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	output, err := run(checkCtx, "systemctl", "show", hostUpdateUnitName, "--property=LoadState,ActiveState,MainPID,ControlPID")
	if err != nil {
		return false, fmt.Errorf("agent: inspect host update helper: %w", err)
	}
	properties := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			properties[name] = value
		}
	}
	load, state := properties["LoadState"], properties["ActiveState"]
	if load != "loaded" && load != "not-found" || state != "inactive" && state != "failed" {
		return false, errors.New("agent: previous host update helper is not stopped")
	}
	for _, name := range []string{"MainPID", "ControlPID"} {
		value := properties[name]
		if value != "0" && !(load == "not-found" && value == "") {
			return false, errors.New("agent: previous host update helper still has a running process")
		}
	}
	return state == "failed", nil
}

func startHostUpdateHelper(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error)) error {
	command := func(arguments ...string) error {
		commandCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		output, err := run(commandCtx, "systemctl", arguments...)
		if err != nil {
			return fmt.Errorf("agent: start persistent host update: %s: %w", strings.TrimSpace(string(output)), err)
		}
		return nil
	}
	for _, arguments := range [][]string{{"daemon-reload"}, {"disable", hostUpdateUnitName}} {
		if err := command(arguments...); err != nil {
			return err
		}
	}
	failed, err := stoppedHostUpdateHelper(ctx, run)
	if err != nil {
		return err
	}
	if failed {
		if err := command("reset-failed", hostUpdateUnitName); err != nil {
			return err
		}
	}
	// Start exactly once, only after staging and every preceding command succeeds.
	return command("start", "--no-block", hostUpdateUnitName)
}
