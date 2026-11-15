//go:build linux

package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/petauron/catalog/catalog"
	"golang.org/x/sys/unix"
)

const budgetUnit = "vastora-pkg-glivela-node.service"
const budgetUnitFile = "/etc/systemd/system/" + budgetUnit

var budgetKeys = []string{"vastora-official/glivela-node", "vastora-official/glivela-entry", "vastora-official/glivela-dual"}

func (s *Store) budgetOwner(ctx context.Context, pid int32) (string, error) {
	denied := errors.New("TCP budget peer is not the authorized installed manager")
	cgroup, e := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/cgroup")
	if e != nil || !slices.Contains(strings.Split(string(cgroup), "\n"), "0::/system.slice/"+budgetUnit) {
		return "", denied
	}
	pidText, e := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", budgetUnit, "--property=MainPID", "--value").Output()
	if e != nil || strings.TrimSpace(string(pidText)) != strconv.Itoa(int(pid)) {
		return "", denied
	}
	unit, e := os.ReadFile(budgetUnitFile)
	if e != nil {
		return "", denied
	}
	unitHash := sha256.Sum256(unit)
	for _, k := range budgetKeys {
		a, e := s.AppliedInstallation(ctx, k)
		if e != nil || !budgetManifest(k, a.Manifest) || !slices.Contains(a.Manifest.Runtime.RequiredCapabilities, "host-tcp-budget") {
			continue
		}
		r, e := (PackageExecutor{StateDirectory: s.dataDir}).ReadResources(a.ApplicationID)
		if e != nil || r.State != "ready" || r.Runtime != a.Manifest.Runtime.Kind || r.RuntimeVersion != 2 || r.AppKey != k || r.ApplicationID != a.ApplicationID || r.PackageVersion != a.Version || r.PackageRevision != a.Manifest.PackageRevision || !slices.Contains(r.AuthorizedCapabilities, "host-tcp-budget") {
			continue
		}
		retired, e := s.executionRetired(a.ApplicationID)
		if e != nil || retired {
			continue
		}
		canonical, e := catalog.CanonicalAppManifest(a.Manifest)
		if e != nil {
			continue
		}
		raw, e := json.Marshal(canonical)
		if e != nil {
			continue
		}
		h := sha256.Sum256(raw)
		if hex.EncodeToString(h[:]) != r.ManifestSHA256 {
			continue
		}
		for _, u := range r.Resources {
			if u.Kind == "unit" && u.LogicalName == "node" && u.Path == budgetUnitFile && u.SHA256 == hex.EncodeToString(unitHash[:]) {
				return a.ApplicationID, nil
			}
		}
	}
	return "", denied
}

func budgetPeer(c *net.UnixConn) (int32, error) {
	rc, e := c.SyscallConn()
	if e != nil {
		return 0, e
	}
	var peer *unix.Ucred
	var problem error
	e = rc.Control(func(fd uintptr) { peer, problem = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if e != nil || problem != nil || peer == nil || peer.Uid != 0 || peer.Pid < 2 {
		return 0, errors.New("unauthorized TCP budget peer")
	}
	return peer.Pid, nil
}

func (s *Store) serveBudget(c *net.UnixConn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	respond := func(v *budgetView, reason string) {
		_ = json.NewEncoder(c).Encode(struct {
			Status *budgetView `json:"status,omitempty"`
			Error  string      `json:"error,omitempty"`
		}{v, reason})
	}
	pid, e := budgetPeer(c)
	if e != nil {
		respond(nil, "unauthorized host budget peer")
		return
	}
	data, e := io.ReadAll(io.LimitReader(c, 1025))
	if e != nil || len(data) > 1024 {
		respond(nil, "invalid host budget request")
		return
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var q budgetRequest
	if d.Decode(&q) != nil || d.Decode(new(any)) != io.EOF {
		respond(nil, "invalid host budget request")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	owner, e := s.budgetOwner(ctx, pid)
	if e != nil {
		respond(nil, "host TCP budget permission is not granted")
		return
	}
	v, e := hostTCPBudget(s.dataDir, owner, q)
	if e != nil {
		respond(nil, "host TCP budget request refused; inspect kernel and ownership state")
		return
	}
	respond(&v, "")
}

func (s *Store) StartTCPBudget() (func(), error) {
	noop := func() {}
	if os.Geteuid() != 0 {
		return noop, nil
	}
	if _, e := os.Stat("/sys/fs/cgroup/cgroup.controllers"); e != nil {
		return noop, nil
	}
	if _, e := os.Stat("/run/systemd/system"); e != nil {
		return noop, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e := exec.CommandContext(ctx, "/usr/bin/systemd-detect-virt", "--container", "--quiet").Run()
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.ExitCode() != 1 {
		return noop, nil
	}
	a, e := os.Readlink("/proc/self/ns/net")
	b, other := os.Readlink("/proc/1/ns/net")
	if e != nil || other != nil || a != b {
		return noop, nil
	}
	dir := filepath.Dir(budgetSocket)
	if e = os.Mkdir(dir, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return noop, e
	}
	if e = budgetSafeParents(budgetSocket); e != nil {
		return noop, e
	}
	st, e := os.Lstat(dir)
	if e != nil || st.Mode().Perm() != 0700 {
		return noop, errors.New("unsafe host TCP budget socket directory")
	}
	lock, e := os.OpenFile(filepath.Join(dir, "listener.lock"), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0600)
	if e != nil {
		return noop, e
	}
	ok := false
	defer func() {
		if !ok {
			lock.Close()
		}
	}()
	if e = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); e != nil {
		return noop, e
	}
	if old, err := os.Lstat(budgetSocket); err == nil {
		if old.Mode()&os.ModeSocket == 0 || old.Sys().(*syscall.Stat_t).Uid != 0 {
			return noop, errors.New("unowned host TCP budget socket")
		}
		if e = os.Remove(budgetSocket); e != nil {
			return noop, e
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return noop, err
	}
	l, e := net.ListenUnix("unix", &net.UnixAddr{Name: budgetSocket, Net: "unix"})
	if e != nil {
		return noop, e
	}
	if e = os.Chmod(budgetSocket, 0600); e != nil {
		l.Close()
		return noop, e
	}

	ok = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.AcceptUnix()
			if err != nil {
				return
			}
			s.serveBudget(c)
		}
	}()
	return func() { l.Close(); <-done; lock.Close() }, nil
}
