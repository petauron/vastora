package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
)

// applyNativePackage is called only after the outer executor has persisted its
// applying intent. Every completed resource is durably receipted before moving
// on. A partial failure is review-required, never an implicit installation retry.
func applyNativePackage(ctx context.Context, manager SystemdHostApplicationManager, task DeploymentTask, prepared nativePreparedPackage, receipt *InstanceResources, persist func() error) error {
	return installNativePackage(ctx, manager, task, prepared, receipt, persist, true)
}

// The one-time local handover stages the same verified resources without
// enabling or starting services. Ordinary application tasks always activate.
func installNativePackage(ctx context.Context, manager SystemdHostApplicationManager, task DeploymentTask, prepared nativePreparedPackage, receipt *InstanceResources, persist func() error, activate bool) error {
	if task.Operation != "install" && task.Operation != "upgrade" {
		return errors.New("agent: native bundles support install or upgrade only")
	}
	if err := inspectNativeCollisions(ctx, manager, task.ApplicationID, prepared, receipt); err != nil {
		return err
	}
	// Check all configuration paths before stopping anything. An upgrade must
	// not initialize missing configuration or overwrite application edits.
	if task.Operation == "upgrade" {
		for _, file := range prepared.Files {
			if !file.ApplicationOwned {
				continue
			}
			owned := resourceNamed(receipt, "file", file.LogicalName)
			if owned == nil || owned.Path != file.Path {
				return errors.New("agent: new native configuration requires explicit migration")
			}
			if snapshot, err := captureApplicationConfiguration(manager.path(file.Path)); err != nil || !snapshot.Exists {
				return errors.New("agent: native upgrade configuration is unavailable")
			}
		}
	}
	// Stop automatically managed controllers before manual workers. A controller
	// must not race this transaction by starting its worker during replacement.
	for _, automatic := range []bool{true, false} {
		for _, unit := range prepared.Plan.Units {
			if unit.Automatic != automatic || resourceNamed(receipt, "unit", unit.LogicalName) == nil {
				continue
			}
			if err := manager.run(ctx, "systemctl", "stop", unit.Name); err != nil {
				return errors.New("agent: native service could not stop before update")
			}
		}
	}
	if prepared.BeforeReplace != nil {
		if err := prepared.BeforeReplace(ctx); err != nil {
			return err
		}
	}
	record := func(resource RuntimeResource) error {
		if old := resourceNamed(receipt, resource.Kind, resource.LogicalName); old != nil {
			*old = resource
		} else {
			receipt.Resources = append(receipt.Resources, resource)
		}
		return persist()
	}
	var users []string
	for logical := range prepared.Plan.Users {
		users = append(users, logical)
	}
	sort.Strings(users)
	for _, logical := range users {
		name := prepared.Plan.Users[logical]
		if resourceNamed(receipt, "user", logical) != nil {
			continue
		}
		// useradd --user-group must not encounter an unrelated pre-existing group.
		var raw []byte
		var err error
		if manager.ReadCommand != nil {
			raw, err = manager.ReadCommand(ctx, "getent", "group", name)
		} else {
			raw, err = exec.CommandContext(ctx, "getent", "group", name).Output()
		}
		var status interface{ ExitCode() int }
		if err == nil || !errors.As(err, &status) || status.ExitCode() != 2 || len(raw) != 0 {
			return errors.New("agent: native group is not safely available")
		}
		if err := manager.run(ctx, "useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--comment", "Vastora "+task.ApplicationID, name); err != nil {
			return errors.New("agent: native account creation failed")
		}
		account, exists, err := readNativeAccount(ctx, manager, name, task.ApplicationID)
		if err != nil || !exists {
			return errors.New("agent: created native account could not be verified")
		}
		if err := record(RuntimeResource{Kind: "user", LogicalName: logical, Name: name, ID: account.identity()}); err != nil {
			return err
		}
	}
	var states []string
	for name := range prepared.Plan.State {
		states = append(states, name)
	}
	sort.Strings(states)
	for _, name := range states {
		if resourceNamed(receipt, "directory", name) != nil {
			continue
		}
		state := prepared.Plan.State[name]
		owner := nativeAccount{}
		if state.User != "root" {
			var exists bool
			var err error
			owner, exists, err = readNativeAccount(ctx, manager, state.User, task.ApplicationID)
			if err != nil || !exists {
				return errors.New("agent: native state owner is unavailable")
			}
		}
		actual := manager.path(state.Path)
		if err := checkPackageParents(actual); err != nil {
			return err
		}
		if _, err := os.Lstat(actual); !errors.Is(err, os.ErrNotExist) {
			return errors.New("agent: native state destination appeared during installation")
		}
		if err := manager.run(ctx, "install", "-d", "-m", "0700", "-o", state.User, "-g", state.User, actual); err != nil {
			return errors.New("agent: native state creation failed")
		}
		if err := record(RuntimeResource{Kind: "directory", LogicalName: name, Name: filepath.Base(state.Path), Path: state.Path, ID: owner.identity(), Persistent: state.Persistent}); err != nil {
			return err
		}
	}
	var paths []string
	for path := range prepared.Files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		file := prepared.Files[path]
		actual := manager.path(path)
		if err := checkPackageParents(actual); err != nil {
			return err
		}
		if file.ApplicationOwned && resourceNamed(receipt, "file", file.LogicalName) != nil {
			snapshot, err := captureApplicationConfiguration(actual)
			if err != nil || !snapshot.Exists {
				return errors.New("agent: native configuration changed during update")
			}
			file.Data, file.Mode = snapshot.Data, snapshot.Mode
		} else if err := writeHostFileAtomic(actual, file.Data, file.Mode); err != nil {
			return err
		}
		digest := sha256.Sum256(file.Data)
		if err := record(RuntimeResource{Kind: file.Kind, LogicalName: file.LogicalName, Component: file.Component, Name: filepath.Base(path), Path: path, SHA256: hex.EncodeToString(digest[:])}); err != nil {
			return err
		}
	}
	// Revalidate the installed bytes and state owners before systemd can exec.
	if err := inspectNativeOwnership(ctx, manager, task.ApplicationID, prepared.Plan, receipt); err != nil {
		return err
	}
	if err := manager.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return errors.New("agent: native unit reload failed")
	}
	for _, unit := range prepared.Plan.Units {
		action := "disable"
		if activate && unit.Automatic {
			action = "enable"
		}
		if err := manager.run(ctx, "systemctl", action, unit.Name); err != nil {
			return errors.New("agent: native startup policy could not be applied")
		}
	}
	for _, unit := range prepared.Plan.Units {
		if activate && unit.Automatic {
			if err := manager.run(ctx, "systemctl", "start", unit.Name); err != nil {
				return errors.New("agent: native service start failed")
			}
		}
	}
	return nil
}
