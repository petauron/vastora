//go:build linux

package nodesecurity

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

const preparationPath = stateDirectory + "/preparation.json"
const fail2banMask = "/etc/systemd/system/fail2ban.service"

// prepare installs only missing distribution dependencies. A persistent owned
// mask prevents the package postinst from starting a default jail before its
// management exemptions have been reviewed. An interrupted install stays
// masked and requires this explicit operation again; no firewall is enabled.
func (h Host) prepare(ctx context.Context, s *checkpoint) (View, error) {
	v := View{State: "unconfigured", Listeners: []string{}, Banned: []string{}, Reasons: []string{}}
	if s != nil && s.View.State != "rolled_back" && s.View.State != "preview" && s.View.State != "unconfigured" {
		return v, errors.New("security: resolve existing security state before preparing dependencies")
	}
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil || !supportedDistro(string(raw)) {
		return v, errors.New("security: unsupported distribution")
	}
	if _, e := h.run(ctx, "systemctl", "is-active", "fail2ban"); e == nil {
		return v, errors.New("security: refusing to take over active Fail2ban")
	}
	owned := false
	if b, e := readOwned(preparationPath); e == nil && string(b) == "{\"fail2banMask\":true}\n" {
		owned = true
	} else if !errors.Is(e, os.ErrNotExist) {
		return v, errors.New("security: unknown dependency preparation state")
	}
	if !owned {
		if enabled, _ := h.run(ctx, "systemctl", "is-enabled", "fail2ban"); enabled == "enabled" {
			return v, errors.New("security: existing Fail2ban boot ownership requires review")
		}
	}
	packages := []string{}
	for _, entry := range []struct{ pkg, config string }{{"ufw", "/etc/ufw"}, {"fail2ban", "/etc/fail2ban"}, {"python3-systemd", ""}, {"nftables", ""}} {
		installed, e := h.run(ctx, "dpkg-query", "-W", "-f=${db:Status-Status}", entry.pkg)
		if e != nil || installed != "installed" {
			if entry.config != "" && !owned {
				if _, e := os.Lstat(entry.config); e == nil {
					return v, errors.New("security: unowned residual package configuration requires review")
				}
			}
			packages = append(packages, entry.pkg)
		}
	}
	if len(packages) == 0 && !owned {
		return v, nil
	}
	if !owned {
		if _, e := os.Lstat(fail2banMask); !errors.Is(e, os.ErrNotExist) {
			return v, errors.New("security: existing Fail2ban unit must not be masked")
		}
		if err = writeOwned(preparationPath, []byte("{\"fail2banMask\":true}\n"), 0600); err != nil {
			return v, err
		}
		if _, err = h.run(ctx, "systemctl", "mask", "fail2ban.service"); err != nil {
			return v, err
		}
	}
	if target, e := os.Readlink(fail2banMask); e != nil || target != "/dev/null" {
		return v, errors.New("security: Fail2ban preparation mask changed; inspect manually")
	}
	if len(packages) > 0 {
		// Installation is independently bounded; this does not run apt upgrade,
		// restart Docker/Tailscale, change repositories or reboot the server.
		installCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		if _, err = h.run(installCtx, "apt-get", "update"); err != nil {
			return v, err
		}
		args := append([]string{"install", "--no-remove", "--no-install-recommends", "-y"}, packages...)
		if _, err = h.run(installCtx, "apt-get", args...); err != nil {
			return v, err
		}
	}
	// A cancelled apt parent can leave dpkg children running. Never remove the
	// persistent mask while a package-manager process can still finish postinst.
	unlock, err := holdPackageLocks([]string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock"})
	if err != nil {
		return v, err
	}
	defer unlock()
	if _, err = h.run(ctx, "systemctl", "disable", "fail2ban.service"); err != nil {
		return v, err
	}
	if target, e := os.Readlink(fail2banMask); e != nil || target != "/dev/null" {
		return v, errors.New("security: preparation mask ownership lost")
	}
	if _, err = h.run(ctx, "systemctl", "unmask", "fail2ban.service"); err != nil {
		return v, err
	}
	if err = os.Remove(preparationPath); err != nil {
		return v, err
	}
	v.Reasons = []string{"Distribution dependencies prepared. Firewall and SSH bans remain disabled until explicit preview and apply."}
	if s != nil && s.View.State == "preview" {
		s.View = v
		s.SSHVerified = false
		if err = saveCheckpoint(s); err != nil {
			return v, err
		}
	}
	return v, nil
}

func holdPackageLocks(paths []string) (func(), error) {
	files := []*os.File{}
	release := func() {
		for _, f := range files {
			_ = f.Close()
		}
	}
	for _, path := range paths {
		f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOFOLLOW, 0)
		if err != nil {
			release()
			return nil, err
		}
		files = append(files, f)
		lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
		if err = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err != nil {
			release()
			return nil, errors.New("security: package manager is still active; retain the preparation mask and retry after it stops")
		}
	}
	return release, nil
}
