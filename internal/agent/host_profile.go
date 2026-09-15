package agent

import (
	"bufio"
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/petauron/vastora/internal/nodediagnostics"
)

// CheckHostProfile reads kernel and filesystem state only. It never invokes
// third-party scripts, writes sysctls, or changes the traffic scheduler.
func (ApplicationExecutor) CheckHostProfile(ctx context.Context, task nodediagnostics.Task) (nodediagnostics.Result, error) {
	if err := task.ValidateHostProfile(); err != nil {
		return nodediagnostics.Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return nodediagnostics.Result{}, err
	}
	mem, err := readMemTotal("/proc/meminfo")
	if err != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	kernel, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return nodediagnostics.Result{Error: "probe_failed"}, nil
	}
	profile := &nodediagnostics.HostProfile{
		CPUCount: runtime.NumCPU(), MemoryBytes: mem, DiskBytes: int64(stat.Blocks) * int64(stat.Bsize),
		Kernel: strings.TrimSpace(string(kernel)), Architecture: runtime.GOARCH,
		CongestionControl: readKernelValue("/proc/sys/net/ipv4/tcp_congestion_control"),
		DefaultQdisc:      readKernelValue("/proc/sys/net/core/default_qdisc"),
		TCPRMem:           readKernelValue("/proc/sys/net/ipv4/tcp_rmem"),
		TCPWMem:           readKernelValue("/proc/sys/net/ipv4/tcp_wmem"),
		TCPSlowStart:      readKernelValue("/proc/sys/net/ipv4/tcp_slow_start_after_idle"),
	}
	_, err = os.Stat("/etc/sysctl.d/99-tcpfit.conf")
	profile.PersistentConfig = err == nil
	profile.Recommendations = recommendTCP(profile, readKernelValue("/proc/sys/net/ipv4/tcp_available_congestion_control"), fileExists("/sys/module/sch_fq"))
	result := nodediagnostics.Result{Host: profile}
	if err := result.Validate(nodediagnostics.HostProfileKind); err != nil {
		return nodediagnostics.Result{}, err
	}
	return result, ctx.Err()
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

func recommendTCP(profile *nodediagnostics.HostProfile, available string, fqLoaded bool) []nodediagnostics.ParameterRecommendation {
	algorithm := profile.CongestionControl
	algorithmReason := "preserve_current"
	if slices.Contains(strings.Fields(available), "bbr") {
		algorithm, algorithmReason = "bbr", "available_low_latency_control"
	}
	qdisc := profile.DefaultQdisc
	qdiscReason := "preserve_current"
	if fqLoaded || qdisc == "fq" {
		qdisc, qdiscReason = "fq", "available_pacing_queue"
	}
	return []nodediagnostics.ParameterRecommendation{
		{Parameter: "tcp_congestion_control", Current: profile.CongestionControl, Value: algorithm, Reason: algorithmReason},
		{Parameter: "default_qdisc", Current: profile.DefaultQdisc, Value: qdisc, Reason: qdiscReason},
		{Parameter: "tcp_slow_start_after_idle", Current: profile.TCPSlowStart, Value: "0", Reason: "avoid_idle_restart"},
		{Parameter: "tcp_rmem", Current: profile.TCPRMem, Value: profile.TCPRMem, Reason: "requires_path_measurement"},
		{Parameter: "tcp_wmem", Current: profile.TCPWMem, Value: profile.TCPWMem, Reason: "requires_path_measurement"},
	}
}

func readMemTotal(path string) (int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scan := bufio.NewScanner(file)
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) == 3 && fields[0] == "MemTotal:" && fields[2] == "kB" {
			value, err := strconv.ParseInt(fields[1], 10, 64)
			return value * 1024, err
		}
	}
	return 0, errors.New("agent: MemTotal unavailable")
}

func readKernelValue(path string) string {
	value, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(value)), " ")
}
