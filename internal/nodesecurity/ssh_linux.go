//go:build linux

package nodesecurity

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// VerifySSH is deliberately a local root command, not an Agent task. It must
// run under a real sshd ancestor started AFTER the current pending application.
// Neither keeping the previous connection open nor clicking a UI checkbox
// creates this receipt. Root is already a trusted host administrator.
func (h Host) VerifySSH(ctx context.Context, revision string) error {
	release, err := locked()
	if err != nil {
		return err
	}
	defer release()
	s, err := loadCheckpoint()
	if err != nil {
		return err
	}
	if s == nil || (s.View.State != "pending_confirmation" && s.View.State != "preview") || s.View.Revision != revision {
		return errors.New("security: no matching pending policy")
	}
	deadline, e := time.Parse(time.RFC3339Nano, s.View.Deadline)
	if e != nil || !h.now().Before(deadline) {
		return errors.New("security: confirmation deadline expired")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || s.View.State == "pending_confirmation" && strings.TrimSpace(string(boot)) != s.BootID {
		return errors.New("security: boot changed before SSH verification")
	}
	if err = h.verifyFiles(s); err != nil {
		return err
	}
	parts := strings.Fields(os.Getenv("SSH_CONNECTION"))
	if len(parts) != 4 {
		return errors.New("security: invoke through a fresh SSH session with SSH_CONNECTION preserved by sudo")
	}
	address, e := netip.ParseAddr(parts[0])
	if e != nil {
		return errors.New("security: invalid SSH source")
	}
	allowed := false
	for _, raw := range s.View.Policy.ManagementSources {
		p, _ := netip.ParsePrefix(raw)
		allowed = allowed || p.Contains(address)
	}
	if !allowed {
		return errors.New("security: SSH source is not an approved management source")
	}
	port, e := strconv.Atoi(parts[3])
	if e != nil {
		return e
	}
	matched := false
	for _, p := range s.View.Policy.SSHPorts {
		matched = matched || p == port
	}
	if !matched {
		return errors.New("security: SSH used an unapproved port")
	}
	hzRaw, err := h.run(ctx, "getconf", "CLK_TCK")
	if err != nil {
		return err
	}
	hz, err := strconv.ParseFloat(hzRaw, 64)
	if err != nil || hz <= 0 {
		return errors.New("security: cannot determine process clock")
	}
	// LXCFS virtualizes /proc/uptime to container age, while /proc/PID/stat
	// start ticks use the kernel boot clock. Mixing them makes old sshd
	// connections appear to start in the future. Use the matching kernel clock.
	var bootClock unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &bootClock); err != nil {
		return errors.New("security: cannot determine process boot clock")
	}
	uptime := float64(bootClock.Sec) + float64(bootClock.Nsec)/1e9
	ancestors := map[int]time.Time{}
	for pid, depth := os.Getppid(), 0; pid > 1 && depth < 32; depth++ {
		raw, e := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if e != nil {
			return e
		}
		parent, ticks, e := processStart(string(raw))
		if e != nil {
			return e
		}
		comm, e := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
		if e != nil {
			return e
		}
		name := strings.TrimSpace(string(comm))
		started := h.now().Add(-time.Duration((uptime - ticks/hz) * float64(time.Second)))
		if name == "sshd" || name == "sshd-session" {
			ancestors[pid] = started
		}
		pid = parent
	}
	clientPort, e := strconv.Atoi(parts[1])
	if e != nil || clientPort < 1 || clientPort > 65535 {
		return errors.New("security: invalid SSH client port")
	}
	sockets, e := h.run(ctx, "ss", "-H", "-tnp", fmt.Sprintf("( sport = :%d and dport = :%d )", port, clientPort))
	if e != nil {
		return e
	}
	if !freshSSHSocket(sockets, parts, ancestors, s.AppliedAt) {
		return errors.New("security: open a NEW SSH connection after applying the policy (do not reuse an SSH multiplexed connection)")
	}
	if s.View.State == "pending_confirmation" {
		if err = h.verifyRuntime(ctx, s); err != nil {
			return err
		}
	}
	s.SSHVerified = true
	return saveCheckpoint(s)
}

var socketPID = regexp.MustCompile(`pid=([0-9]+),`)

func freshSSHSocket(raw string, peer []string, ancestors map[int]time.Time, after time.Time) bool {
	if len(peer) != 4 {
		return false
	}
	for _, line := range strings.Split(raw, "\n") {
		local, remote := false, false
		for _, field := range strings.Fields(line) {
			host, port, err := net.SplitHostPort(field)
			if err != nil {
				continue
			}
			ip, err := netip.ParseAddr(host)
			if err != nil {
				continue
			}
			source, e1 := netip.ParseAddr(peer[0])
			target, e2 := netip.ParseAddr(peer[2])
			local = local || e2 == nil && ip.Unmap() == target.Unmap() && port == peer[3]
			remote = remote || e1 == nil && ip.Unmap() == source.Unmap() && port == peer[1]
		}
		if !local || !remote {
			continue
		}
		found := false
		for _, match := range socketPID.FindAllStringSubmatch(line, -1) {
			pid, err := strconv.Atoi(match[1])
			if err != nil {
				continue
			}
			if started, ok := ancestors[pid]; ok {
				found = true
				if !started.After(after.Add(time.Second)) {
					return false
				}
			}
		}
		if found {
			return true
		}
	}
	return false
}

func processStart(raw string) (int, float64, error) {
	at := strings.LastIndex(raw, ") ")
	if at < 0 {
		return 0, 0, errors.New("security: invalid process evidence")
	}
	fields := strings.Fields(raw[at+2:])
	if len(fields) < 20 {
		return 0, 0, errors.New("security: invalid process evidence")
	}
	parent, e := strconv.Atoi(fields[1])
	if e != nil {
		return 0, 0, e
	}
	ticks, e := strconv.ParseFloat(fields[19], 64)
	return parent, ticks, e
}

func (h Host) verifyRuntime(ctx context.Context, s *checkpoint) error {
	status, err := h.run(ctx, "ufw", "status")
	if err != nil || !strings.HasPrefix(status, "Status: active") {
		return errors.New("security: UFW is not active")
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		rules := strings.Split(strings.TrimSpace(inputRules(s.View.Policy, tool == "ip6tables")), "\n")
		count := 0
		for _, rule := range rules {
			args := strings.Fields(rule)
			args[0] = "-C"
			if _, err = h.run(ctx, tool, args...); err != nil {
				return errors.New("security: public ingress guard rule is missing")
			}
			if args[1] == chain {
				count++
			}
		}
		actual, e := h.run(ctx, tool, "-S", chain)
		if e != nil || len(strings.Split(strings.TrimSpace(actual), "\n")) != count+1 {
			return errors.New("security: public ingress guard changed")
		}
		beforeChain := ufwBeforeInput(tool == "ip6tables")
		before, e := h.run(ctx, tool, "-S", beforeChain)
		if e != nil {
			return e
		}
		lines := strings.Split(before, "\n")
		for i, iface := range s.View.Policy.PublicInterfaces {
			if len(lines) <= i+1 || lines[i+1] != "-A "+beforeChain+" -i "+iface+" -j "+chain {
				return errors.New("security: public guard is no longer first")
			}
		}
	}
	if _, err = h.run(ctx, "fail2ban-client", "status", "sshd"); err != nil {
		return errors.New("security: SSH jail is not active")
	}
	jails, e := h.run(ctx, "fail2ban-client", "status")
	if e != nil || !strings.Contains(jails, "Jail list:") || strings.TrimSpace(strings.SplitN(jails, "Jail list:", 2)[1]) != "sshd" {
		return errors.New("security: unexpected Fail2ban jail ownership")
	}
	if s.Docker {
		for _, tool := range []string{"iptables", "ip6tables"} {
			if _, e := h.run(ctx, tool, "-S", "DOCKER-USER"); e != nil {
				return errors.New("security: Docker user-chain backend is unavailable")
			}
			if _, e := h.run(ctx, tool, "-C", "FORWARD", "-j", "DOCKER-USER"); e != nil {
				return errors.New("security: Docker user chain is detached")
			}
			if _, e := h.run(ctx, tool, "-C", "DOCKER-USER", "-j", "VASTORA-DOCKER"); e != nil {
				return errors.New("security: Docker guard is missing")
			}
			parent, e := h.run(ctx, tool, "-S", "DOCKER-USER")
			lines := strings.Split(parent, "\n")
			if e != nil || len(lines) < 2 || lines[1] != "-A DOCKER-USER -j VASTORA-DOCKER" {
				return errors.New("security: Docker guard is no longer first")
			}
			if e := h.verifyDockerRules(ctx, tool, s.View.Policy, false); e != nil {
				return e
			}
		}
	}
	return nil
}
