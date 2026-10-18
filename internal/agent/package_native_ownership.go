package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type nativeAccount struct{ UID, GID uint32 }

func (a nativeAccount) identity() string {
	return strconv.FormatUint(uint64(a.UID), 10) + ":" + strconv.FormatUint(uint64(a.GID), 10)
}

func readNativeAccount(ctx context.Context, manager SystemdHostApplicationManager, name, applicationID string) (nativeAccount, bool, error) {
	var raw []byte
	var err error
	if manager.ReadCommand != nil {
		raw, err = manager.ReadCommand(ctx, "getent", "passwd", name)
	} else {
		raw, err = exec.CommandContext(ctx, "getent", "passwd", name).Output()
	}
	if err != nil {
		var status interface{ ExitCode() int }
		if errors.As(err, &status) && status.ExitCode() == 2 && len(raw) == 0 {
			return nativeAccount{}, false, nil
		}
		return nativeAccount{}, false, errors.New("agent: cannot inspect native service account")
	}
	parts := strings.Split(strings.TrimSuffix(string(raw), "\n"), ":")
	if len(parts) != 7 || parts[0] != name || parts[4] != "Vastora "+applicationID || parts[5] != "/nonexistent" || parts[6] != "/usr/sbin/nologin" {
		return nativeAccount{}, false, errors.New("agent: native service account is not owned by this application")
	}
	uid, e1 := strconv.ParseUint(parts[2], 10, 32)
	gid, e2 := strconv.ParseUint(parts[3], 10, 32)
	if e1 != nil || e2 != nil || uid == 0 || gid == 0 {
		return nativeAccount{}, false, errors.New("agent: invalid native service account IDs")
	}
	return nativeAccount{UID: uint32(uid), GID: uint32(gid)}, true, nil
}

// expectedNativeResource binds a receipt to the current signed declaration and
// deterministic app namespace. Neither receipt paths nor host names are trusted.
func expectedNativeResource(applicationID string, p nativePackagePlan, r RuntimeResource) (nativePreparedFile, error) {
	var expected nativePreparedFile
	expected.Kind, expected.LogicalName = r.Kind, r.LogicalName
	switch r.Kind {
	case "user":
		name, ok := p.Users[r.LogicalName]
		if !ok || r.Name != name || r.Path != "" || r.Component != "" {
			return expected, errors.New("agent: unexpected native account receipt")
		}
		return expected, nil
	case "directory":
		state, ok := p.State[r.LogicalName]
		if !ok || r.Persistent != state.Persistent || r.Component != "" {
			return expected, errors.New("agent: unexpected native state receipt")
		}
		expected.Path = state.Path
	case "unit":
		for _, unit := range p.Units {
			if unit.LogicalName == r.LogicalName {
				expected.Path, expected.Component = unit.Path, unit.LogicalName
			}
		}
	case "file":
		if strings.HasPrefix(r.LogicalName, "artifact:") {
			parts := strings.SplitN(strings.TrimPrefix(r.LogicalName, "artifact:"), ":", 2)
			if len(parts) == 2 {
				for _, file := range p.Artifacts[parts[0]] {
					if file.Path == parts[1] {
						expected.Path = filepath.Join("/opt/vastora/packages", packageIdentity(applicationID), "artifacts", parts[0], file.Path)
					}
				}
			}
		} else {
			for _, unit := range p.Units {
				prefix := unit.LogicalName + ":"
				if !strings.HasPrefix(r.LogicalName, prefix) {
					continue
				}
				logical := strings.TrimPrefix(r.LogicalName, prefix)
				if logical == "environment" {
					expected.Path = filepath.Join(unit.ConfigDirectory, "environment")
				}
				for _, file := range unit.Process.ConfigFiles {
					if logical == "config:"+file.Path {
						expected.Path = filepath.Join(unit.ConfigDirectory, "config", file.Path)
					}
				}
				for name := range unit.Process.Credentials {
					if logical == "credential:"+name {
						expected.Path = filepath.Join(unit.ConfigDirectory, "credentials", name)
					}
				}
				if expected.Path != "" {
					expected.Component, expected.ApplicationOwned = unit.LogicalName, true
				}
			}
		}
	default:
		return expected, errors.New("agent: unsupported native receipt resource")
	}
	if expected.Path == "" || expected.Path != r.Path || r.Name != filepath.Base(expected.Path) || r.Component != expected.Component {
		return expected, errors.New("agent: native receipt escapes declared resources")
	}
	return expected, nil
}

func inspectNativeOwnership(ctx context.Context, manager SystemdHostApplicationManager, applicationID string, p nativePackagePlan, receipt *InstanceResources) error {
	if receipt == nil || receipt.ApplicationID != applicationID || receipt.Runtime != "systemd" {
		return errors.New("agent: invalid native ownership receipt")
	}
	seen, paths := map[string]bool{}, map[string]bool{}
	for _, resource := range receipt.Resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := resource.Kind + ":" + resource.LogicalName
		if seen[key] || resource.Path != "" && paths[resource.Path] {
			return errors.New("agent: duplicate native ownership receipt")
		}
		seen[key], paths[resource.Path] = true, true
		expected, err := expectedNativeResource(applicationID, p, resource)
		if err != nil {
			return err
		}
		if resource.Kind == "user" {
			account, exists, err := readNativeAccount(ctx, manager, resource.Name, applicationID)
			if err != nil || !exists || account.identity() != resource.ID {
				return errors.New("agent: native account identity changed")
			}
			continue
		}
		actual := manager.path(expected.Path)
		if err := checkPackageParents(actual); err != nil {
			return err
		}
		if resource.Kind == "directory" {
			info, err := os.Lstat(actual)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("agent: native state directory is missing or unsafe")
			}
			owner := nativeAccount{}
			if user := p.State[resource.LogicalName].User; user != "root" {
				var exists bool
				owner, exists, err = readNativeAccount(ctx, manager, user, applicationID)
				if err != nil || !exists {
					return errors.New("agent: native state owner is unavailable")
				}
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Uid != owner.UID || stat.Gid != owner.GID || resource.ID != owner.identity() {
				return errors.New("agent: native state ownership changed")
			}
			continue
		}
		var snapshot hostFileSnapshot
		if expected.ApplicationOwned {
			snapshot, err = captureApplicationConfiguration(actual)
		} else {
			snapshot, err = captureHostFile(actual)
		}
		if err != nil || !snapshot.Exists || resource.SHA256 == "" {
			return errors.New("agent: native ownership file is missing or unsafe")
		}
		digest := sha256.Sum256(snapshot.Data)
		if !expected.ApplicationOwned && resource.SHA256 != hex.EncodeToString(digest[:]) {
			return errors.New("agent: native installed program or unit has changed")
		}
		if resource.Kind == "unit" && (!bytes.Contains(snapshot.Data, []byte("# Managed by Vastora\n")) || !bytes.Contains(snapshot.Data, []byte("# Application: "+applicationID+"\n"))) {
			return errors.New("agent: native unit lacks instance ownership marker")
		}
	}
	return nil
}

// Before mutation, every destination must be absent or backed by an inspected
// receipt. A matching deterministic name is never proof of ownership.
func inspectNativeCollisions(ctx context.Context, manager SystemdHostApplicationManager, applicationID string, prepared nativePreparedPackage, receipt *InstanceResources) error {
	if err := inspectNativeOwnership(ctx, manager, applicationID, prepared.Plan, receipt); err != nil {
		return err
	}
	for logical, name := range prepared.Plan.Users {
		account, exists, err := readNativeAccount(ctx, manager, name, applicationID)
		if err != nil {
			return err
		}
		if exists {
			r := resourceNamed(receipt, "user", logical)
			if r == nil || r.Name != name || r.ID != account.identity() {
				return errors.New("agent: unowned native account collision")
			}
		}
	}
	check := func(path, kind, logical string) error {
		actual := manager.path(path)
		if err := checkPackageParents(actual); err != nil {
			return err
		}
		_, err := os.Lstat(actual)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		owned := resourceNamed(receipt, kind, logical)
		if owned == nil || owned.Path != path {
			return errors.New("agent: native destination collides with unowned resource")
		}
		return nil
	}
	for _, file := range prepared.Files {
		if err := check(file.Path, file.Kind, file.LogicalName); err != nil {
			return err
		}
	}
	for name, state := range prepared.Plan.State {
		if err := check(state.Path, "directory", name); err != nil {
			return err
		}
	}
	return nil
}
