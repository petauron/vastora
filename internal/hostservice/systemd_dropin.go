package hostservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// EnsureSystemdDropin changes only an exact, previously owned drop-in. The
// parent unit and unrelated drop-ins remain untouched. A failed daemon reload
// restores the prior file before returning.
func EnsureSystemdDropin(ctx context.Context, dir, name string, wanted []byte, owned func([]byte) bool, run func(context.Context, string, ...string) error) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || filepath.Base(name) != name || name == "." || owned == nil || run == nil {
		return errors.New("hostservice: invalid managed systemd drop-in")
	}
	if len(wanted) != 0 && !owned(wanted) {
		return errors.New("hostservice: refusing an invalid managed systemd drop-in")
	}
	checkDir := func(path string) error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
			return errors.New("hostservice: unsafe systemd drop-in directory")
		}
		return nil
	}
	if err := checkDir(filepath.Dir(dir)); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0755); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := checkDir(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	var previous []byte
	info, err := os.Lstat(path)
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Geteuid()) || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 16<<10 {
			return errors.New("hostservice: unsafe systemd drop-in file")
		}
		previous, err = os.ReadFile(path)
		if err != nil {
			return err
		}
		if !owned(previous) {
			return errors.New("hostservice: refusing to replace an unowned systemd drop-in")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if bytes.Equal(previous, wanted) {
		return nil
	}
	write := func(raw []byte) error {
		if len(raw) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			return nil
		}
		file, err := os.CreateTemp(dir, ".vastora-dropin-*")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		if err = file.Chmod(0644); err == nil {
			_, err = file.Write(raw)
		}
		if err == nil {
			err = file.Sync()
		}
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return err
		}
		return os.Rename(file.Name(), path)
	}
	if err := write(wanted); err != nil {
		return err
	}
	if err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		if restoreErr := write(previous); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore managed drop-in: %w", restoreErr))
		}
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if reloadErr := run(recovery, "systemctl", "daemon-reload"); reloadErr != nil {
			return errors.Join(err, fmt.Errorf("reload restored systemd drop-in: %w", reloadErr))
		}
		return err
	}
	return nil
}
