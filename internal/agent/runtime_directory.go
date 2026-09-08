package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// prepareAdminSocketDirectory runs before both container creation and restart:
// /run is volatile even when Docker's container metadata survives a reboot.
// Do not follow symlinks, fix ownership, or delete an unexpected existing file.
func prepareAdminSocketDirectory(socketPath string) error {
	if socketPath == "" {
		return nil
	}
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || filepath.Dir(socketPath) == string(filepath.Separator) {
		return errors.New("agent: Caddy Admin socket requires a dedicated absolute directory")
	}
	directory := filepath.Dir(socketPath)
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(directory, current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
				return fmt.Errorf("agent: prepare Caddy Admin directory: %w", err)
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return fmt.Errorf("agent: inspect Caddy Admin directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("agent: Caddy Admin directory must not contain a symlink or non-directory")
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return errors.New("agent: Caddy Admin directory has an unsafe writable ancestor")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(owner.Uid) != 0 && int(owner.Uid) != os.Geteuid() {
			return errors.New("agent: Caddy Admin directory has an untrusted owner")
		}
		if current == directory {
			if int(owner.Uid) != os.Geteuid() || info.Mode().Perm() != 0o700 {
				return errors.New("agent: Caddy Admin directory must be owned by the Agent user with mode 0700")
			}
		}
	}
	if info, err := os.Lstat(socketPath); err == nil {
		owner, ok := info.Sys().(*syscall.Stat_t)
		if info.Mode()&os.ModeSocket == 0 || !ok || int(owner.Uid) != os.Geteuid() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("agent: Caddy Admin socket path has an unsafe type, owner or permissions")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("agent: inspect Caddy Admin socket: %w", err)
	}
	return nil
}
