//go:build linux

package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/nodeswap"
)

type fakeSwapHost struct {
	root       string
	env        hostSwapEnv
	virt       string
	container  bool
	zramMod    bool
	diskFree   int64
	activeSwap []string
}

func newFakeSwapHost(t *testing.T, memTotalKB, memAvailKB int64, swappiness int) *fakeSwapHost {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(t.TempDir(), "agent")
	for _, dir := range []string{
		"/proc/sys/vm",
		"/sys/block/zram0",
		"/sys/module/zram",
		"/run/systemd/system",
		"/etc/sysctl.d",
		"/etc/systemd/system",
		"/usr/local/sbin",
	} {
		if err := os.MkdirAll(filepath.Join(root, strings.TrimPrefix(dir, "/")), 0755); err != nil {
			t.Fatal(err)
		}
	}
	h := &fakeSwapHost{
		root:     root,
		virt:     "kvm",
		zramMod:  true,
		diskFree: 4 << 30,
	}
	h.setMeminfo(t, memTotalKB, memAvailKB)
	h.writeText(t, "/proc/sys/vm/swappiness", fmt.Sprintf("%d\n", swappiness))
	h.writeText(t, "/sys/block/zram0/disksize", "0\n")
	h.writeText(t, "/sys/block/zram0/comp_algorithm", "lzo [lz4] zstd\n")
	h.writeText(t, "/sys/block/zram0/mm_stat", "0 0 0 0 0 0 0 0 0\n")
	h.writeText(t, "/sys/block/zram0/reset", "0\n")
	h.writeText(t, "/etc/fstab", "UUID=test-root / ext4 errors=remount-ro 0 1\n")
	h.syncProcSwaps(t)

	h.env = hostSwapEnv{
		root:         root,
		dataDir:      dataDir,
		checkRootUID: false,
		pageSize:     4096,
		diskFreeBytes: func(string) (int64, error) {
			return h.diskFree, nil
		},
		allocateSwap: func(target string, sizeBytes int64) error {
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			defer f.Close()
			return f.Truncate(sizeBytes)
		},
		run: func(_ context.Context, name string, args ...string) (string, error) {
			switch name {
			case "systemd-detect-virt":
				if len(args) == 1 && args[0] == "--container" {
					if h.container {
						return "lxc", nil
					}
					return "none", errors.New("not container")
				}
				if len(args) == 1 && args[0] == "--vm" {
					if h.virt == "none" {
						return "none", errors.New("not vm")
					}
					return h.virt, nil
				}
			case "modinfo":
				if h.zramMod {
					return "filename: zram.ko", nil
				}
				return "", errors.New("missing module")
			case "mkswap":
				return "mkswap ok", nil
			case "swapon":
				// swapon -p <prio> <dev>
				prio := args[1]
				dev := args[2]
				if strings.HasSuffix(dev, "/swapfile") {
					h.activeSwap = append(h.activeSwap, fmt.Sprintf("/swapfile\tfile\t%d\t0\t%s", 524284, prio))
				}
				h.syncProcSwaps(t)
				return "", nil
			case "swapoff":
				dev := args[0]
				kept := []string{}
				for _, line := range h.activeSwap {
					if strings.HasSuffix(dev, "/swapfile") && strings.HasPrefix(line, "/swapfile\t") {
						continue
					}
					if dev == "/dev/zram0" && strings.HasPrefix(line, "/dev/zram0\t") {
						continue
					}
					kept = append(kept, line)
				}
				h.activeSwap = kept
				h.syncProcSwaps(t)
				return "", nil
			case "systemctl":
				if len(args) >= 3 && args[0] == "enable" && args[1] == "--now" && args[2] == "vastora-zram.service" {
					h.writeText(t, "/sys/block/zram0/disksize", fmt.Sprintf("%d\n", 256<<20))
					h.writeText(t, "/sys/block/zram0/comp_algorithm", "lzo [lz4] zstd\n")
					h.activeSwap = append(h.activeSwap, "/dev/zram0\tpartition\t262140\t0\t100")
					h.syncProcSwaps(t)
				}
				if len(args) >= 3 && args[0] == "disable" && args[1] == "--now" && args[2] == "vastora-zram.service" {
					h.writeText(t, "/sys/block/zram0/disksize", "0\n")
					kept := []string{}
					for _, line := range h.activeSwap {
						if !strings.HasPrefix(line, "/dev/zram0\t") {
							kept = append(kept, line)
						}
					}
					h.activeSwap = kept
					h.syncProcSwaps(t)
				}
				return "", nil
			}
			return "", nil
		},
	}
	return h
}

func (h *fakeSwapHost) setMeminfo(t *testing.T, totalKB, availKB int64) {
	t.Helper()
	h.writeText(t, "/proc/meminfo", fmt.Sprintf("MemTotal:       %d kB\nMemAvailable:   %d kB\n", totalKB, availKB))
}

func (h *fakeSwapHost) writeText(t *testing.T, rel, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(h.root, strings.TrimPrefix(rel, "/")), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func (h *fakeSwapHost) syncProcSwaps(t *testing.T) {
	t.Helper()
	body := "Filename\tType\tSize\tUsed\tPriority\n"
	if len(h.activeSwap) > 0 {
		body += strings.Join(h.activeSwap, "\n") + "\n"
	}
	h.writeText(t, "/proc/swaps", body)
}

func TestHostSwapLifecycle512MiBAndHeadroomGuard(t *testing.T) {
	ctx := context.Background()
	h := newFakeSwapHost(t, 475136, 300*1024, 30)

	inspected, err := h.env.execute(ctx, "inspect-1", nodeswap.Task{Action: "inspect"})
	if err != nil {
		t.Fatalf("inspect failed: %v", err)
	}
	if inspected.State != "eligible" || !inspected.CanApply || inspected.Target.TierMiB != 512 || inspected.Target.ZramBytes != 256<<20 {
		t.Fatalf("unexpected initial view: %+v", inspected)
	}
	if err := h.env.checkRemoval(); err != nil {
		t.Fatalf("expected unconfigured host swap to allow removal, got %v", err)
	}

	applied, err := h.env.execute(ctx, "apply-1", nodeswap.Task{Action: "apply", Revision: inspected.Revision})
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if applied.State != "applied" || !applied.CanRestore || applied.Swappiness != 60 || len(applied.Swaps) != 2 {
		t.Fatalf("unexpected applied view: %+v", applied)
	}
	if err := h.env.checkRemoval(); err == nil {
		t.Fatal("expected applied host swap to block Agent removal until restored")
	}

	// Simulate heavy zram usage exceeding MemAvailable headroom -> restore must refuse fail-closed.
	h.writeText(t, "/sys/block/zram0/mm_stat", fmt.Sprintf("%d 50000000 52000000 0 0 0 0 0 0\n", 220<<20))
	h.setMeminfo(t, 475136, 180*1024)
	tight, err := h.env.execute(ctx, "inspect-2", nodeswap.Task{Action: "inspect"})
	if err != nil {
		t.Fatalf("tight inspect failed: %v", err)
	}
	if tight.CanRestore {
		t.Fatalf("expected CanRestore=false when MemAvailable < zram_logical + margin, got %+v", tight)
	}
	if _, err := h.env.execute(ctx, "restore-blocked", nodeswap.Task{Action: "restore", Revision: tight.Revision}); err == nil {
		t.Fatal("expected restore with insufficient memory headroom to fail")
	}

	// Free memory headroom and verify clean restoration of original swappiness (30) and fstab.
	h.writeText(t, "/sys/block/zram0/mm_stat", "0 0 0 0 0 0 0 0 0\n")
	h.setMeminfo(t, 475136, 320*1024)
	readyRestore, err := h.env.execute(ctx, "inspect-3", nodeswap.Task{Action: "inspect"})
	if err != nil || !readyRestore.CanRestore {
		t.Fatalf("expected restorable state, got %+v err=%v", readyRestore, err)
	}
	restored, err := h.env.execute(ctx, "restore-1", nodeswap.Task{Action: "restore", Revision: readyRestore.Revision})
	if err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if restored.State != "eligible" || !restored.CanApply || restored.Swappiness != 30 || len(restored.Swaps) != 0 {
		t.Fatalf("unexpected restored view: %+v", restored)
	}
	if err := h.env.checkRemoval(); err != nil {
		t.Fatalf("expected restored host swap to allow Agent removal, got %v", err)
	}
	fstabAfter, err := os.ReadFile(filepath.Join(h.root, "etc/fstab"))
	if err != nil || strings.Contains(string(fstabAfter), "/swapfile") {
		t.Fatalf("expected /swapfile removed from fstab, got %q err=%v", string(fstabAfter), err)
	}
}

func TestHostSwap2GiBTierAndReadOnlyBoundaries(t *testing.T) {
	ctx := context.Background()
	h2g := newFakeSwapHost(t, 2020352, 1600*1024, 60)
	v2g, err := h2g.env.execute(ctx, "inspect-2g", nodeswap.Task{Action: "inspect"})
	if err != nil || !v2g.CanApply || v2g.Target.TierMiB != 2048 || v2g.Target.ZramBytes != 0 || v2g.Target.Swappiness != 10 {
		t.Fatalf("unexpected 2 GiB inspect: %+v err=%v", v2g, err)
	}
	applied2g, err := h2g.env.execute(ctx, "apply-2g", nodeswap.Task{Action: "apply", Revision: v2g.Revision})
	if err != nil || applied2g.State != "applied" || applied2g.Swappiness != 10 || applied2g.Zram != nil || len(applied2g.Swaps) != 1 {
		t.Fatalf("unexpected 2 GiB apply: %+v err=%v", applied2g, err)
	}
	if _, err := os.Lstat(filepath.Join(h2g.root, "etc/systemd/system/vastora-zram.service")); !os.IsNotExist(err) {
		t.Fatal("2 GiB tier must not install vastora-zram.service")
	}

	// Container / OpenRC host must remain strictly read-only.
	hlxc := newFakeSwapHost(t, 512*1024, 400*1024, 60)
	hlxc.container = true
	vlxc, err := hlxc.env.execute(ctx, "inspect-lxc", nodeswap.Task{Action: "inspect"})
	if err != nil || vlxc.State != "read_only" || vlxc.CanApply {
		t.Fatalf("expected LXC host to be read_only, got %+v err=%v", vlxc, err)
	}

	// Unmanaged pre-existing swap must remain strictly read-only.
	hunmanaged := newFakeSwapHost(t, 1024*1024, 800*1024, 60)
	hunmanaged.activeSwap = []string{"/dev/vda2\tpartition\t524284\t0\t-2"}
	hunmanaged.syncProcSwaps(t)
	vunmanaged, err := hunmanaged.env.execute(ctx, "inspect-unmanaged", nodeswap.Task{Action: "inspect"})
	if err != nil || vunmanaged.State != "read_only" || vunmanaged.CanApply {
		t.Fatalf("expected unmanaged swap host to be read_only, got %+v err=%v", vunmanaged, err)
	}
}
