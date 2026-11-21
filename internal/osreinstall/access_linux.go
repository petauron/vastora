//go:build linux

package osreinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func readOwnedAccessFile(name string, owner uint32, max int64) ([]byte, error) {
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() < 0 || info.Size() > max {
		return nil, errors.New("reinstall: access file requires review")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != owner) || stat.Nlink != 1 {
		return nil, errors.New("reinstall: access file requires review")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("reinstall: access changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, max+1))
	if err != nil || int64(len(data)) != info.Size() {
		return nil, errors.New("reinstall: access changed")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		return nil, errors.New("reinstall: access changed")
	}
	return data, nil
}

func snapshotSSHDirectory() ([]byte, error) {
	var output bytes.Buffer
	compressor := gzip.NewWriter(&output)
	writer := tar.NewWriter(compressor)
	count := 0
	var total int64
	err := filepath.WalkDir("/etc/ssh", func(name string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "/etc/ssh" {
			return nil
		}
		count++
		if count > 128 {
			return errors.New("reinstall: too many SSH files")
		}
		info, err := os.Lstat(name)
		if err != nil {
			return errors.New("reinstall: unsafe SSH configuration")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || stat.Gid != 0 {
			return errors.New("reinstall: unsafe SSH owner")
		}
		relative, err := filepath.Rel("/", name)
		if err != nil {
			return err
		}
		header := &tar.Header{Name: filepath.ToSlash(relative), Mode: int64(info.Mode().Perm()), Uid: 0, Gid: 0}
		if info.Mode()&os.ModeSymlink != 0 {
			target, e := os.Readlink(name)
			if e != nil || name != "/etc/ssh/ssh_config.d/20-systemd-ssh-proxy.conf" || target != "/usr/lib/systemd/ssh_config.d/20-systemd-ssh-proxy.conf" {
				return errors.New("reinstall: unsupported SSH link")
			}
			header.Typeflag = tar.TypeSymlink
			header.Linkname = target
		} else if info.Mode().Perm()&0022 != 0 {
			return errors.New("reinstall: unsafe SSH configuration")
		} else if info.IsDir() {
			header.Typeflag = tar.TypeDir
		} else if info.Mode().IsRegular() {
			data, err := readOwnedAccessFile(name, 0, 1<<20)
			if err != nil {
				return err
			}
			total += int64(len(data))
			if total > 2<<20 {
				return errors.New("reinstall: SSH configuration too large")
			}
			header.Typeflag = tar.TypeReg
			header.Size = int64(len(data))
			if err = writer.WriteHeader(header); err != nil {
				return err
			}
			_, err = writer.Write(data)
			return err
		} else {
			return errors.New("reinstall: unsupported SSH configuration")
		}
		return writer.WriteHeader(header)
	})
	if err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	if err = compressor.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// CaptureAccessSnapshot supports a key-only account and existing passwordless
// sudo, one unmodified systemd Agent unit, and an empty host nftables ruleset.
// Other installations need their own proven restoration before using this MVP.
func CaptureAccessSnapshot(ctx context.Context, username string, port int, expectedHostKey string) (AccessSnapshot, error) {
	bad := func() (AccessSnapshot, error) {
		return AccessSnapshot{}, errors.New("reinstall: SSH, firewall or Agent service requires review")
	}
	if os.Geteuid() != 0 || !safeUser.MatchString(username) || port < 1 || port > 65535 {
		return bad()
	}
	keys, err := reviewedSSHKeys(username)
	if err != nil {
		return AccessSnapshot{}, err
	}
	var rootKeys []byte
	if username != "root" {
		rootKeys, err = readOwnedAccessFile("/root/.ssh/authorized_keys", 0, 64<<10)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			if _, e := os.Lstat("/root/.ssh/authorized_keys"); !errors.Is(e, os.ErrNotExist) {
				return bad()
			}
			rootKeys = nil
		}
	}
	newest, err := verifySSHPolicy(ctx, username, port)
	if err != nil {
		return AccessSnapshot{}, err
	}
	if err := verifySSHService(ctx, port, newest); err != nil {
		return AccessSnapshot{}, err
	}
	policy := "none"
	if username != "root" {
		cmd := exec.CommandContext(ctx, "sudo", "-n", "-l", "-U", username)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		output, err := cmd.Output()
		if err != nil || strings.Contains(string(output), "!") || !strings.HasSuffix(strings.TrimSpace(string(output)), "(ALL) NOPASSWD: ALL") {
			return bad()
		}
		policy = "passwordless_all"
	}
	rules, err := readCommand(ctx, "nft", "list", "ruleset")
	if err != nil || len(bytes.TrimSpace(rules)) != 0 {
		return bad()
	}
	// A legacy iptables stack is independent of nftables and needs its own
	// reviewed recovery plan; do not silently discard it during reinstall.
	for _, name := range []string{"iptables-legacy-save", "ip6tables-legacy-save"} {
		if _, e := exec.LookPath(name); e == nil {
			legacy, e := readCommand(ctx, name)
			if e != nil || len(bytes.TrimSpace(legacy)) != 0 {
				return bad()
			}
		} else if !errors.Is(e, exec.ErrNotFound) {
			return bad()
		}
	}
	for _, unit := range []string{"docker.service", "tailscaled.service", "ufw.service", "firewalld.service"} {
		active, e := managerActive(ctx, unit)
		if e != nil || active {
			return bad()
		}
	}
	unit, err := verifyAgentService(ctx)
	if err != nil {
		return AccessSnapshot{}, err
	}
	archive, err := snapshotSSHDirectory()
	if err != nil {
		return bad()
	}
	result := AccessSnapshot{Username: username, SSHPort: port, SudoPolicy: policy, SSHArchive: archive, AuthorizedKeys: keys, RootKeys: rootKeys, AgentUnit: unit}
	if ValidateAccessSnapshot(result, username, port, expectedHostKey) != nil {
		return bad()
	}
	return result, nil
}
