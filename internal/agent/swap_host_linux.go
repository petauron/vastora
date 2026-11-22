//go:build linux

package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/petauron/vastora/internal/nodeswap"
)

const (
	swapStateFile        = "host-swap.json"
	swapLockFile         = "host-swap.lock"
	swapSysctlPath       = "/etc/sysctl.d/zz-petauron-swap.conf"
	swapUnitPath         = "/etc/systemd/system/vastora-zram.service"
	swapLegacyUnitPath   = "/etc/systemd/system/petauron-zram.service"
	swapScriptPath       = "/usr/local/sbin/vastora-zram"
	swapFilePath         = "/swapfile"
	swapFstabPath        = "/etc/fstab"
	swapFstabMarker      = "# Managed by Vastora host swap"
	swapFstabEntry       = "/swapfile none swap sw,pri=-2 0 0"
	swapMinDiskFreeBytes = 1536 << 20
	swapRestoreMargin    = 64 << 20
)

type swapRecord struct {
	Stage              string                `json:"stage"`
	Command            string                `json:"command"`
	OriginalSwappiness int                   `json:"originalSwappiness"`
	Target             nodeswap.TargetPolicy `json:"target"`
}

func (r *swapRecord) sysctlContent() []byte {
	return []byte(fmt.Sprintf("# Managed by Vastora host swap\nvm.swappiness = %d\n", r.Target.Swappiness))
}

func (r *swapRecord) scriptContent() []byte {
	if r.Target.ZramBytes <= 0 {
		return nil
	}
	return []byte(fmt.Sprintf(`#!/bin/sh
# Managed by Vastora host swap
set -eu
export PATH="/usr/sbin:/usr/bin:/sbin:/bin"

case "${1:-}" in
  start)
    if [ ! -b /dev/zram0 ]; then
      modprobe zram num_devices=1
    fi
    udevadm settle --timeout=15
    if ! awk 'NR>1 && ($1=="/dev/zram0" || $1=="/zram0") {found=1} END {exit !found}' /proc/swaps; then
      if [ -e /sys/block/zram0/reset ]; then
        echo 1 >/sys/block/zram0/reset || true
      fi
      echo %s >/sys/block/zram0/comp_algorithm
      echo %d >/sys/block/zram0/disksize
      mkswap /dev/zram0 >/dev/null
      swapon -p %d /dev/zram0
    fi
    ;;
  stop)
    if awk 'NR>1 && ($1=="/dev/zram0" || $1=="/zram0") {found=1} END {exit !found}' /proc/swaps; then
      swapoff /dev/zram0
    fi
    if [ -e /sys/block/zram0/reset ]; then
      echo 1 >/sys/block/zram0/reset || true
    fi
    ;;
  *)
    echo "usage: $0 {start|stop}" >&2
    exit 2
    ;;
esac
`, r.Target.ZramAlgorithm, r.Target.ZramBytes, r.Target.ZramPriority))
}

func (r *swapRecord) unitContent() []byte {
	if r.Target.ZramBytes <= 0 {
		return nil
	}
	return []byte(`# Managed by Vastora host swap
[Unit]
Description=Vastora Managed Zram Swap
DefaultDependencies=no
After=local-fs.target systemd-modules-load.service
Before=swap.target
Conflicts=shutdown.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/local/sbin/vastora-zram start
ExecStop=/usr/local/sbin/vastora-zram stop

[Install]
WantedBy=swap.target multi-user.target
`)
}

type hostSwapEnv struct {
	root          string
	dataDir       string
	checkRootUID  bool
	pageSize      int64
	run           func(context.Context, string, ...string) (string, error)
	diskFreeBytes func(string) (int64, error)
	allocateSwap  func(string, int64) error
}

func newHostSwapEnv(dataDir string) hostSwapEnv {
	return hostSwapEnv{
		dataDir:      dataDir,
		checkRootUID: true,
		pageSize:     int64(os.Getpagesize()),
		run:          runSwapCommand,
		diskFreeBytes: func(path string) (int64, error) {
			var st syscall.Statfs_t
			if err := syscall.Statfs(path, &st); err != nil {
				return 0, err
			}
			return int64(st.Bavail) * int64(st.Bsize), nil
		},
		allocateSwap: writeZeroSwapfile,
	}
}

func (s *Store) executeNodeSwap(ctx context.Context, taskID string, task nodeswap.Task) (nodeswap.View, error) {
	if err := task.Validate(); err != nil {
		return nodeswap.View{}, err
	}
	if os.Geteuid() != 0 {
		return nodeswap.View{}, errors.New("swap: Linux root is required")
	}
	env := newHostSwapEnv(s.dataDir)
	return env.execute(ctx, taskID, task)
}

// CheckHostSwapRemoval refuses Agent decommission or uninstall while managed
// host swap has not been restored, so removing the Agent data directory cannot
// orphan /swapfile, vastora-zram.service, or host-swap.json recovery state.
func CheckHostSwapRemoval(dataDir string) error {
	return newHostSwapEnv(dataDir).checkRemoval()
}

func (e hostSwapEnv) checkRemoval() error {
	if strings.TrimSpace(e.dataDir) == "" {
		return nil
	}
	path := filepath.Join(e.dataDir, swapStateFile)
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	recBytes, err := e.readRegular(path, 0600, 8192)
	if err != nil {
		return err
	}
	var parsed swapRecord
	if json.Unmarshal(recBytes, &parsed) != nil || !parsed.Target.Valid() || parsed.Target.TierMiB == 0 {
		return errors.New("swap: invalid host swap recovery record; reconcile before removing the Agent")
	}
	if parsed.Stage != "restored" {
		return errors.New("swap: explicitly restore managed host swap before removing the Agent")
	}
	return nil
}

func runSwapCommand(ctx context.Context, name string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "PATH=/usr/sbin:/usr/bin:/sbin:/bin")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("swap: %s failed: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func writeZeroSwapfile(target string, sizeBytes int64) error {
	tmp := target + ".vastora-tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		_ = f.Close()
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	buf := make([]byte, 1<<20)
	remaining := sizeBytes
	for remaining > 0 {
		chunk := int64(len(buf))
		if remaining < chunk {
			chunk = remaining
		}
		if _, err := f.Write(buf[:chunk]); err != nil {
			return err
		}
		remaining -= chunk
	}
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(tmp, target); err != nil {
		return err
	}
	_ = os.Remove(tmp)
	cleanup = false
	d, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (e hostSwapEnv) hostPath(abs string) string {
	if e.root == "" {
		return abs
	}
	return filepath.Join(e.root, strings.TrimPrefix(abs, "/"))
}

func (e hostSwapEnv) safeParents(file string) error {
	stop := "/"
	if e.root != "" {
		stop = filepath.Clean(e.root)
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("swap: unsafe parent directory")
		}
		if e.checkRootUID {
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
				return errors.New("swap: untrusted parent directory permissions")
			}
		}
		if dir == stop || dir == "/" || dir == "." {
			break
		}
	}
	return nil
}

func (e hostSwapEnv) readRegular(path string, mode os.FileMode, maxBytes int64) ([]byte, error) {
	if err := e.safeParents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, errors.New("swap: unexpected host file type or size")
	}
	if mode != 0 && info.Mode().Perm() != mode {
		return nil, errors.New("swap: unexpected host file mode")
	}
	if e.checkRootUID {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 {
			return nil, errors.New("swap: unowned host file")
		}
	}
	return os.ReadFile(path)
}

func (e hostSwapEnv) atomicSave(path string, content []byte, mode os.FileMode, createOnly bool) error {
	if err := e.safeParents(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".vastora-swap-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(content); err != nil {
		return err
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if createOnly {
		err = os.Link(f.Name(), path)
	} else {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (e hostSwapEnv) removeSynced(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (e hostSwapEnv) lock() (func(), error) {
	if err := os.MkdirAll(e.dataDir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(e.dataDir, swapLockFile), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("swap: another host swap operation is in progress")
	}
	return func() { _ = f.Close() }, nil
}

func readProcMeminfoKB(path string) (totalKB, availKB int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	var gotTotal, gotAvail bool
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB, err = strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, 0, err
			}
			gotTotal = true
		case "MemAvailable:":
			availKB, err = strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, 0, err
			}
			gotAvail = true
		}
	}
	if err := scan.Err(); err != nil {
		return 0, 0, err
	}
	if !gotTotal || !gotAvail || totalKB <= 0 || availKB < 0 {
		return 0, 0, errors.New("swap: incomplete /proc/meminfo")
	}
	return totalKB, availKB, nil
}

func readProcSwaps(path string) ([]nodeswap.SwapDevice, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	out := []nodeswap.SwapDevice{}
	for i, line := range lines {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			return nil, errors.New("swap: malformed /proc/swaps entry")
		}
		sizeKB, err1 := strconv.ParseInt(fields[2], 10, 64)
		usedKB, err2 := strconv.ParseInt(fields[3], 10, 64)
		prio, err3 := strconv.Atoi(fields[4])
		if err1 != nil || err2 != nil || err3 != nil || sizeKB < 0 || usedKB < 0 {
			return nil, errors.New("swap: invalid /proc/swaps metrics")
		}
		name := strings.ReplaceAll(fields[0], `\040(deleted)`, "")
		if name == "/zram0" {
			name = "/dev/zram0"
		}
		out = append(out, nodeswap.SwapDevice{
			Name:      name,
			Type:      fields[1],
			SizeBytes: sizeKB * 1024,
			UsedBytes: usedKB * 1024,
			Priority:  prio,
		})
	}
	return out, nil
}

func parseZramAlgorithm(raw string) string {
	for _, token := range strings.Fields(raw) {
		if strings.HasPrefix(token, "[") && strings.HasSuffix(token, "]") && len(token) > 2 {
			return token[1 : len(token)-1]
		}
	}
	return strings.TrimSpace(raw)
}

func (e hostSwapEnv) readZram0(swaps []nodeswap.SwapDevice) *nodeswap.ZramDevice {
	diskRaw, err := os.ReadFile(e.hostPath("/sys/block/zram0/disksize"))
	if err != nil {
		return nil
	}
	diskBytes, err := strconv.ParseInt(strings.TrimSpace(string(diskRaw)), 10, 64)
	if err != nil {
		return nil
	}
	activeSwap := false
	for _, s := range swaps {
		if s.Name == "/dev/zram0" {
			activeSwap = true
			break
		}
	}
	if diskBytes == 0 && !activeSwap {
		return nil
	}
	var origBytes int64
	if mmRaw, err := os.ReadFile(e.hostPath("/sys/block/zram0/mm_stat")); err == nil {
		fields := strings.Fields(string(mmRaw))
		if len(fields) > 0 {
			origBytes, _ = strconv.ParseInt(fields[0], 10, 64)
		}
	}
	var alg string
	if algRaw, err := os.ReadFile(e.hostPath("/sys/block/zram0/comp_algorithm")); err == nil {
		alg = parseZramAlgorithm(string(algRaw))
	}
	return &nodeswap.ZramDevice{
		DisksizeBytes: diskBytes,
		OrigDataBytes: origBytes,
		Algorithm:     alg,
	}
}

func (e hostSwapEnv) detectEnvironment(ctx context.Context) (systemd bool, virt string, zramSupported bool) {
	if info, err := os.Stat(e.hostPath("/run/systemd/system")); err == nil && info.IsDir() {
		systemd = true
	}
	if _, err := e.run(ctx, "systemd-detect-virt", "--container"); err == nil {
		virt = "container"
	} else if out, err := e.run(ctx, "systemd-detect-virt", "--vm"); err == nil && out != "" {
		virt = out
	} else if out != "" {
		virt = out
	} else {
		virt = "none"
	}
	if info, err := os.Stat(e.hostPath("/sys/module/zram")); err == nil && info.IsDir() {
		zramSupported = true
	} else if _, err := e.run(ctx, "modinfo", "zram"); err == nil {
		zramSupported = true
	}
	return systemd, virt, zramSupported
}

func fstabSwapStatus(raw []byte) (hasManaged bool, hasUnmanagedSwap bool) {
	lines := strings.Split(string(raw), "\n")
	managedCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == swapFstabEntry {
			managedCount++
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) >= 3 && (fields[2] == "swap" || fields[1] == "swap" || fields[0] == swapFilePath) {
			hasUnmanagedSwap = true
		}
	}
	if managedCount > 1 {
		hasUnmanagedSwap = true
	}
	return managedCount == 1 && !hasUnmanagedSwap, hasUnmanagedSwap
}

func appendManagedFstab(existing []byte) []byte {
	text := strings.TrimRight(string(existing), "\n")
	if text == "" {
		return []byte(swapFstabMarker + "\n" + swapFstabEntry + "\n")
	}
	return []byte(text + "\n" + swapFstabMarker + "\n" + swapFstabEntry + "\n")
}

func removeManagedFstab(existing []byte) []byte {
	lines := strings.Split(string(existing), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == swapFstabMarker || trimmed == swapFstabEntry {
			continue
		}
		kept = append(kept, line)
	}
	out := strings.Join(kept, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return []byte(out)
}

func swapSizeMatches(observedBytes, targetBytes, pageSize int64) bool {
	if pageSize <= 0 {
		pageSize = 4096
	}
	minBytes := targetBytes - pageSize*4
	return observedBytes >= minBytes && observedBytes <= targetBytes
}

func (e hostSwapEnv) inspect(ctx context.Context) (v nodeswap.View, r *swapRecord, err error) {
	totalKB, availKB, err := readProcMeminfoKB(e.hostPath("/proc/meminfo"))
	if err != nil {
		return v, nil, err
	}
	v.MemoryTotalBytes = totalKB * 1024
	v.MemoryAvailableBytes = availKB * 1024
	v.DiskFreeBytes, err = e.diskFreeBytes(e.hostPath("/"))
	if err != nil {
		return v, nil, err
	}
	swpRaw, err := os.ReadFile(e.hostPath("/proc/sys/vm/swappiness"))
	if err != nil {
		return v, nil, err
	}
	v.Swappiness, err = strconv.Atoi(strings.TrimSpace(string(swpRaw)))
	if err != nil || v.Swappiness < 0 || v.Swappiness > 200 {
		return v, nil, errors.New("swap: invalid kernel swappiness")
	}
	v.Swaps, err = readProcSwaps(e.hostPath("/proc/swaps"))
	if err != nil {
		return v, nil, err
	}
	v.Zram = e.readZram0(v.Swaps)
	var zramSupported bool
	v.Systemd, v.Virtualization, zramSupported = e.detectEnvironment(ctx)
	v.Target = nodeswap.TierForMemoryKB(totalKB)

	recBytes, err := e.readRegular(filepath.Join(e.dataDir, swapStateFile), 0600, 8192)
	if err != nil {
		return v, nil, err
	}
	if recBytes != nil {
		var parsed swapRecord
		if json.Unmarshal(recBytes, &parsed) != nil || !parsed.Target.Valid() || parsed.Target.TierMiB == 0 || parsed.OriginalSwappiness < 0 || parsed.OriginalSwappiness > 200 {
			return v, nil, errors.New("swap: invalid host swap recovery record")
		}
		switch parsed.Stage {
		case "applying", "applied", "restoring", "restored":
		default:
			return v, nil, errors.New("swap: invalid host swap recovery stage")
		}
		r = &parsed
	}

	sysctlBytes, err := e.readRegular(e.hostPath(swapSysctlPath), 0644, 4096)
	if err != nil {
		return v, nil, err
	}
	unitBytes, err := e.readRegular(e.hostPath(swapUnitPath), 0644, 8192)
	if err != nil {
		return v, nil, err
	}
	scriptBytes, err := e.readRegular(e.hostPath(swapScriptPath), 0755, 8192)
	if err != nil {
		return v, nil, err
	}
	fstabBytes, err := e.readRegular(e.hostPath(swapFstabPath), 0, 65536)
	if err != nil {
		return v, nil, err
	}
	fstabManaged, fstabUnmanaged := fstabSwapStatus(fstabBytes)
	_, legacyUnitErr := os.Lstat(e.hostPath(swapLegacyUnitPath))
	hasLegacyUnit := legacyUnitErr == nil
	swapfileInfo, swapfileErr := os.Lstat(e.hostPath(swapFilePath))
	hasSwapfile := swapfileErr == nil

	if r == nil || r.Stage == "restored" {
		switch {
		case !v.Systemd || (v.Virtualization != "kvm" && v.Virtualization != "qemu"):
			v.State = "read_only"
			v.Reason = "Requires a Linux systemd KVM host; containers and non-KVM hosts remain read-only."
		case v.Target.TierMiB == 0:
			v.State = "read_only"
			v.Reason = "Host memory is outside the 512 MiB / 1 GiB / 2 GiB managed tiers."
		case len(v.Swaps) > 0 || v.Zram != nil || hasSwapfile || sysctlBytes != nil || unitBytes != nil || scriptBytes != nil || hasLegacyUnit || fstabManaged || fstabUnmanaged:
			v.State = "read_only"
			v.Reason = "Existing unmanaged swap or zram configuration detected; Vastora will not overwrite it."
		case v.Target.ZramBytes > 0 && !zramSupported:
			v.State = "read_only"
			v.Reason = "Kernel zram support is unavailable on this host."
		case v.DiskFreeBytes < swapMinDiskFreeBytes:
			v.State = "read_only"
			v.Reason = "Insufficient root filesystem free space (at least 1.5 GiB required)."
		default:
			v.State = "eligible"
			v.CanApply = true
		}
	} else {
		tampered := false
		if sysctlBytes != nil && !bytes.Equal(sysctlBytes, r.sysctlContent()) {
			tampered = true
		}
		if r.Target.ZramBytes > 0 {
			if unitBytes != nil && !bytes.Equal(unitBytes, r.unitContent()) {
				tampered = true
			}
			if scriptBytes != nil && !bytes.Equal(scriptBytes, r.scriptContent()) {
				tampered = true
			}
		} else if unitBytes != nil || scriptBytes != nil {
			tampered = true
		}
		if fstabUnmanaged {
			tampered = true
		}
		for _, s := range v.Swaps {
			if s.Name != swapFilePath && (r.Target.ZramBytes == 0 || s.Name != "/dev/zram0") {
				tampered = true
			}
		}
		if tampered {
			v.State = "action_required"
			v.Reason = "Managed swap configuration was modified externally; inspect the host before restoring."
		} else if r.Stage == "applied" && e.liveMatchesTarget(v, r, sysctlBytes, unitBytes, scriptBytes, fstabManaged, swapfileInfo, hasSwapfile) {
			v.State = "applied"
			if e.hasRestoreHeadroom(v) {
				v.CanRestore = true
			} else {
				v.Reason = "Insufficient available memory headroom to safely disable active swap/zram."
			}
		} else {
			v.State = "action_required"
			if e.hasRestoreHeadroom(v) {
				v.CanRestore = true
				v.Reason = "Managed swap state is incomplete or drifted; restore pre-apply state to recover."
			} else {
				v.Reason = "Managed swap state drifted and available memory is too low for automatic swapoff."
			}
		}
	}

	encoded, _ := json.Marshal(struct {
		V nodeswap.View
		R *swapRecord
	}{V: v, R: r})
	sum := sha256.Sum256(encoded)
	v.Revision = hex.EncodeToString(sum[:])
	return v, r, nil
}

func (e hostSwapEnv) liveMatchesTarget(
	v nodeswap.View,
	r *swapRecord,
	sysctlBytes, unitBytes, scriptBytes []byte,
	fstabManaged bool,
	swapfileInfo os.FileInfo,
	hasSwapfile bool,
) bool {
	if !bytes.Equal(sysctlBytes, r.sysctlContent()) || !fstabManaged || v.Swappiness != r.Target.Swappiness {
		return false
	}
	if !hasSwapfile || !swapfileInfo.Mode().IsRegular() || swapfileInfo.Mode().Perm() != 0600 || swapfileInfo.Size() != r.Target.SwapfileBytes {
		return false
	}
	var gotSwapfile, gotZram bool
	for _, s := range v.Swaps {
		switch s.Name {
		case swapFilePath:
			gotSwapfile = swapSizeMatches(s.SizeBytes, r.Target.SwapfileBytes, e.pageSize) && s.Priority == r.Target.SwapfilePriority
		case "/dev/zram0":
			if r.Target.ZramBytes == 0 {
				return false
			}
			gotZram = swapSizeMatches(s.SizeBytes, r.Target.ZramBytes, e.pageSize) && s.Priority == r.Target.ZramPriority
		default:
			return false
		}
	}
	if !gotSwapfile {
		return false
	}
	if r.Target.ZramBytes > 0 {
		if !gotZram || v.Zram == nil || v.Zram.DisksizeBytes != r.Target.ZramBytes || v.Zram.Algorithm != r.Target.ZramAlgorithm {
			return false
		}
		if !bytes.Equal(unitBytes, r.unitContent()) || !bytes.Equal(scriptBytes, r.scriptContent()) {
			return false
		}
	} else if v.Zram != nil {
		return false
	}
	return true
}

func (e hostSwapEnv) hasRestoreHeadroom(v nodeswap.View) bool {
	var zramUsed, swapfileUsed int64
	for _, s := range v.Swaps {
		switch s.Name {
		case "/dev/zram0":
			zramUsed = s.UsedBytes
		case swapFilePath:
			swapfileUsed = s.UsedBytes
		}
	}
	if v.Zram != nil && v.Zram.OrigDataBytes > zramUsed {
		zramUsed = v.Zram.OrigDataBytes
	}
	return v.MemoryAvailableBytes > zramUsed+swapfileUsed+swapRestoreMargin
}

func (e hostSwapEnv) saveRecord(r *swapRecord) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return e.atomicSave(filepath.Join(e.dataDir, swapStateFile), raw, 0600, false)
}

func (e hostSwapEnv) execute(ctx context.Context, taskID string, task nodeswap.Task) (nodeswap.View, error) {
	unlock, err := e.lock()
	if err != nil {
		return nodeswap.View{}, err
	}
	defer unlock()

	v, r, err := e.inspect(ctx)
	if err != nil {
		return nodeswap.View{}, err
	}
	if task.Action == "inspect" {
		return v, nil
	}
	if task.Revision != v.Revision {
		return v, errors.New("swap: stale host swap revision; inspect current state first")
	}

	switch task.Action {
	case "apply":
		if !v.CanApply {
			return v, errors.New("swap: host is not eligible for managed swap application")
		}
		rec := &swapRecord{
			Stage:              "applying",
			Command:            taskID,
			OriginalSwappiness: v.Swappiness,
			Target:             v.Target,
		}
		if err := e.saveRecord(rec); err != nil {
			return v, err
		}
		if err := e.apply(ctx, rec); err != nil {
			updated, _, inspectErr := e.inspect(ctx)
			if inspectErr == nil {
				return updated, err
			}
			return v, err
		}
		rec.Stage = "applied"
		if err := e.saveRecord(rec); err != nil {
			return v, err
		}
		updated, _, err := e.inspect(ctx)
		if err != nil {
			return v, err
		}
		if err := updated.ValidateFor(task); err != nil {
			return updated, err
		}
		return updated, nil

	case "restore":
		if !v.CanRestore || r == nil {
			return v, errors.New("swap: host swap cannot be restored in its current state")
		}
		if !e.hasRestoreHeadroom(v) {
			return v, errors.New("swap: insufficient available memory to safely disable active swap")
		}
		r.Stage = "restoring"
		r.Command = taskID
		if err := e.saveRecord(r); err != nil {
			return v, err
		}
		if err := e.restore(ctx, r); err != nil {
			updated, _, inspectErr := e.inspect(ctx)
			if inspectErr == nil {
				return updated, err
			}
			return v, err
		}
		r.Stage = "restored"
		if err := e.saveRecord(r); err != nil {
			return v, err
		}
		updated, _, err := e.inspect(ctx)
		if err != nil {
			return v, err
		}
		if err := updated.ValidateFor(task); err != nil {
			return updated, err
		}
		return updated, nil

	default:
		return v, errors.New("swap: unsupported action")
	}
}

func (e hostSwapEnv) apply(ctx context.Context, r *swapRecord) error {
	swapHostFile := e.hostPath(swapFilePath)
	if err := e.safeParents(swapHostFile); err != nil {
		return err
	}
	if err := e.allocateSwap(swapHostFile, r.Target.SwapfileBytes); err != nil {
		return err
	}
	if _, err := e.run(ctx, "mkswap", swapHostFile); err != nil {
		return err
	}
	if _, err := e.run(ctx, "swapon", "-p", strconv.Itoa(r.Target.SwapfilePriority), swapHostFile); err != nil {
		return err
	}

	fstabHost := e.hostPath(swapFstabPath)
	existingFstab, err := e.readRegular(fstabHost, 0, 65536)
	if err != nil {
		return err
	}
	fstabMode := os.FileMode(0644)
	if info, statErr := os.Lstat(fstabHost); statErr == nil {
		fstabMode = info.Mode().Perm()
	}
	if err := e.atomicSave(fstabHost, appendManagedFstab(existingFstab), fstabMode, false); err != nil {
		return err
	}

	if r.Target.ZramBytes > 0 {
		scriptHost := e.hostPath(swapScriptPath)
		if err := os.MkdirAll(filepath.Dir(scriptHost), 0755); err != nil {
			return err
		}
		if err := e.atomicSave(scriptHost, r.scriptContent(), 0755, true); err != nil {
			return err
		}
		unitHost := e.hostPath(swapUnitPath)
		if err := e.atomicSave(unitHost, r.unitContent(), 0644, true); err != nil {
			return err
		}
		if _, err := e.run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
		if _, err := e.run(ctx, "systemctl", "enable", "--now", "vastora-zram.service"); err != nil {
			return err
		}
	}

	sysctlHost := e.hostPath(swapSysctlPath)
	if err := e.atomicSave(sysctlHost, r.sysctlContent(), 0644, true); err != nil {
		return err
	}
	if err := os.WriteFile(e.hostPath("/proc/sys/vm/swappiness"), []byte(strconv.Itoa(r.Target.Swappiness)+"\n"), 0); err != nil {
		return err
	}
	return nil
}

func (e hostSwapEnv) restore(ctx context.Context, r *swapRecord) error {
	swaps, err := readProcSwaps(e.hostPath("/proc/swaps"))
	if err != nil {
		return err
	}
	var zramActive, swapfileActive bool
	for _, s := range swaps {
		if s.Name == "/dev/zram0" {
			zramActive = true
		}
		if s.Name == swapFilePath {
			swapfileActive = true
		}
	}

	if r.Target.ZramBytes > 0 {
		if _, statErr := os.Lstat(e.hostPath(swapUnitPath)); statErr == nil {
			_, _ = e.run(ctx, "systemctl", "disable", "--now", "vastora-zram.service")
		} else if zramActive {
			if _, err := e.run(ctx, "swapoff", "/dev/zram0"); err != nil {
				return err
			}
		}
		if resetPath := e.hostPath("/sys/block/zram0/reset"); fileExists(resetPath) {
			_ = os.WriteFile(resetPath, []byte("1\n"), 0)
		}
		if err := e.removeSynced(e.hostPath(swapUnitPath)); err != nil {
			return err
		}
		if err := e.removeSynced(e.hostPath(swapScriptPath)); err != nil {
			return err
		}
		if _, err := e.run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}

	if swapfileActive {
		if _, err := e.run(ctx, "swapoff", e.hostPath(swapFilePath)); err != nil {
			return err
		}
	}
	fstabHost := e.hostPath(swapFstabPath)
	existingFstab, err := e.readRegular(fstabHost, 0, 65536)
	if err != nil {
		return err
	}
	if existingFstab != nil {
		fstabMode := os.FileMode(0644)
		if info, statErr := os.Lstat(fstabHost); statErr == nil {
			fstabMode = info.Mode().Perm()
		}
		if err := e.atomicSave(fstabHost, removeManagedFstab(existingFstab), fstabMode, false); err != nil {
			return err
		}
	}
	if err := e.removeSynced(e.hostPath(swapFilePath)); err != nil {
		return err
	}
	if err := e.removeSynced(e.hostPath(swapSysctlPath)); err != nil {
		return err
	}
	if err := os.WriteFile(e.hostPath("/proc/sys/vm/swappiness"), []byte(strconv.Itoa(r.OriginalSwappiness)+"\n"), 0); err != nil {
		return err
	}
	return nil
}
