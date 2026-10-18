//go:build linux

package nodesecurity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const stateDirectory = "/var/lib/vastora-node-security"
const watchdog = "vastora-node-security-watchdog"
const jailPath = "/etc/fail2ban/jail.d/90-vastora-sshd.local"
const chain = "ufw-vastora-security"

// Host is intentionally not configurable by remote tasks. Its hooks permit
// deterministic unprivileged tests without touching the test runner's firewall.
type Host struct {
	run func(context.Context, string, ...string) (string, error)
	now func() time.Time
}

type checkpoint struct {
	View        View              `json:"view"`
	BootID      string            `json:"bootId"`
	AppliedAt   time.Time         `json:"appliedAt"`
	Original    map[string][]byte `json:"original"`
	Written     map[string][]byte `json:"written"`
	SSHVerified bool              `json:"sshVerified"`
	Docker      bool              `json:"docker"`
}

func NewHost() Host { return Host{run: command, now: time.Now} }

func command(ctx context.Context, name string, args ...string) (string, error) {
	limit := 30 * time.Second
	if name == "apt-get" {
		limit = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive", "NEEDRESTART_MODE=l")
	var output limitedOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("security: %s failed (output withheld): %w", name, err)
	}
	return strings.TrimSpace(output.value.String()), nil
}

type limitedOutput struct{ value strings.Builder }

func (o *limitedOutput) Write(p []byte) (int, error) {
	if o.value.Len()+len(p) > 128*1024 {
		return 0, errors.New("security: command output exceeds limit")
	}
	return o.value.Write(p)
}

func locked() (func(), error) {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return nil, errors.New("security: Linux root is required")
	}
	if err := os.MkdirAll(stateDirectory, 0700); err != nil {
		return nil, err
	}
	if err := securePath(stateDirectory, true); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(stateDirectory+"/lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, errors.New("security: another operation is in progress")
	}
	return func() { _ = f.Close() }, nil
}

func securePath(path string, directory bool) error {
	for p := path; ; p = filepath.Dir(p) {
		s, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := s.Sys().(*syscall.Stat_t)
		if !ok || st.Uid != 0 || s.Mode()&os.ModeSymlink != 0 || s.Mode().Perm()&0022 != 0 || p == path && (directory != s.IsDir() || !directory && (!s.Mode().IsRegular() || s.Size() > 256*1024)) {
			return errors.New("security: untrusted host file ownership or type")
		}
		if p == "/" {
			break
		}
	}
	return nil
}

func readOwned(path string) ([]byte, error) {
	if err := securePath(path, false); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}
func writeOwned(path string, data []byte, mode os.FileMode) error {
	if err := securePath(filepath.Dir(path), true); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if err = securePath(path, false); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".vastora-security-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func loadCheckpoint() (*checkpoint, error) {
	b, err := readOwned(stateDirectory + "/state.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s checkpoint
	if json.Unmarshal(b, &s) != nil || s.View.Validate() != nil {
		return nil, errors.New("security: invalid recovery checkpoint")
	}
	return &s, nil
}
func saveCheckpoint(s *checkpoint) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeOwned(stateDirectory+"/state.json", b, 0600)
}

func (h Host) Execute(ctx context.Context, t Task) (View, error) {
	if err := t.Validate(); err != nil {
		return View{}, err
	}
	release, err := locked()
	if err != nil {
		return View{}, err
	}
	defer release()
	s, err := loadCheckpoint()
	if err != nil {
		return View{}, err
	}
	if s != nil && s.View.State == "pending_confirmation" {
		boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
		deadline, _ := time.Parse(time.RFC3339Nano, s.View.Deadline)
		if !h.now().Before(deadline) || strings.TrimSpace(string(boot)) != s.BootID {
			if err = h.rollback(ctx, s); err != nil {
				return View{}, err
			}
		}
	}
	if s != nil && s.View.State == "verified" {
		if err = h.verifyFiles(s); err != nil {
			return View{}, err
		}
		if err = h.restoreDockerGuard(ctx, s); err != nil {
			return View{}, err
		}
	}
	switch t.Action {
	case "prepare":
		return h.prepare(ctx, s)
	case "inspect":
		if s == nil {
			v := View{State: "unconfigured", Listeners: []string{}, Banned: []string{}, Reasons: []string{}}
			if _, e := os.Lstat(preparationPath); e == nil {
				v.State = "action_required"
				v.Reasons = []string{"Dependency preparation was interrupted. Retry preparation to reconcile the owned Fail2ban mask; do not enable manually."}
			}
			return v, nil
		}
		v := s.View
		if v.State == "verified" || v.State == "pending_confirmation" {
			if h.verifyFiles(s) != nil {
				v.State = "action_required"
				v.Reasons = []string{"Managed security configuration changed; manual review required."}
			}
			if h.verifyRuntime(ctx, s) != nil {
				v.State = "action_required"
				v.Reasons = append(v.Reasons, "Runtime security verification failed; do not treat this node as protected.")
			}
			if raw, e := h.run(ctx, "fail2ban-client", "get", "sshd", "banip"); e == nil {
				for _, ip := range strings.Fields(raw) {
					if _, e := netip.ParseAddr(ip); e == nil && len(v.Banned) < 256 {
						v.Banned = append(v.Banned, ip)
					}
				}
			}
		}
		return v, nil
	case "preview":
		v, e := h.preview(ctx, t.Policy, s)
		v.Revision = t.Revision
		if e == nil && v.Supported {
			v.Deadline = h.now().UTC().Add(ConfirmationWindowSeconds * time.Second).Format(time.RFC3339Nano)
			e = saveCheckpoint(&checkpoint{View: v, AppliedAt: h.now().UTC()})
		}
		return v, e
	case "apply":
		if s != nil && s.View.Revision == t.Revision && (s.View.State == "pending_confirmation" || s.View.State == "verified") {
			return s.View, nil
		}
		return h.apply(ctx, t, s)
	default:
		if s == nil || t.Revision != s.View.Revision {
			return View{}, errors.New("security: stale security revision")
		}
		if t.Action == "disable" {
			err = h.rollback(ctx, s)
			return s.View, err
		}
		if err = h.verifyFiles(s); err != nil {
			return View{}, err
		}
		if t.Action == "confirm" {
			if s.View.State == "verified" {
				return s.View, nil
			}
			if s.View.State != "pending_confirmation" || !s.SSHVerified {
				return View{}, errors.New("security: fresh SSH verification is required before confirming")
			}
			s.View.State = "verified"
			s.View.Deadline = ""
			err = saveCheckpoint(s)
			return s.View, err
		}
		if s.View.State != "verified" {
			return View{}, errors.New("security: only a verified SSH jail can be managed")
		}
		_, err = h.run(ctx, "fail2ban-client", "set", "sshd", "unbanip", t.Address)
		return s.View, err
	}
}

func (h Host) preview(ctx context.Context, p Policy, s *checkpoint) (View, error) {
	v := View{State: "preview", Revision: p.Digest(), Policy: p, Listeners: []string{}, Banned: []string{}, Reasons: []string{}, Supported: true}
	reject := func(reason string) {
		v.Supported = false
		if len(v.Reasons) < 16 {
			v.Reasons = append(v.Reasons, reason)
		}
	}
	if s != nil && (s.View.State == "pending_confirmation" || s.View.State == "verified" || s.View.State == "action_required") {
		reject("Disable the existing baseline before replacing its policy.")
		return v, nil
	}
	distro, err := os.ReadFile("/etc/os-release")
	if err != nil || !supportedDistro(string(distro)) {
		reject("Only supported Debian/Ubuntu systemd hosts are supported.")
	}
	for _, tool := range []string{"ufw", "fail2ban-client", "iptables", "ip6tables", "systemctl", "ss", "sshd"} {
		if _, e := exec.LookPath(tool); e != nil {
			reject("Missing prerequisite: " + tool + ". Install reviewed distribution packages before enabling.")
		}
	}
	if changed, e := h.run(ctx, "dpkg", "--verify", "ufw", "fail2ban"); e != nil || changed != "" {
		reject("UFW/Fail2ban package configuration differs from distribution defaults; refusing takeover.")
	}
	for _, path := range []string{"/etc/fail2ban/jail.local", "/etc/fail2ban/fail2ban.local"} {
		if _, e := os.Lstat(path); e == nil {
			reject("Existing Fail2ban overrides require manual review.")
		}
	}
	if entries, e := os.ReadDir("/etc/fail2ban/jail.d"); e != nil {
		reject("Cannot inspect Fail2ban configuration ownership.")
	} else {
		for _, entry := range entries {
			if entry.Name() != "defaults-debian.conf" {
				reject("Unowned Fail2ban jail configuration requires review.")
			}
		}
	}
	// Package postinst can start a jail before management exemptions exist.
	// Provisioning therefore uses the separate, masked prepare operation.
	if _, e := h.run(ctx, "systemctl", "is-active", "firewalld"); e == nil {
		reject("An existing firewall manager must not be taken over.")
	}
	if _, e := h.run(ctx, "systemctl", "is-active", "fail2ban"); e == nil {
		reject("An existing Fail2ban service must not be taken over.")
	}
	if status, _ := h.run(ctx, "systemctl", "is-enabled", "fail2ban"); status == "enabled" {
		reject("Fail2ban must not already have boot ownership.")
	}
	if _, e := h.run(ctx, "systemctl", "is-enabled", "ufw"); e != nil {
		reject("UFW must have its distribution boot service enabled.")
	}
	if status, e := h.run(ctx, "ufw", "status"); e != nil || status != "Status: inactive" {
		reject("UFW must be inactive and unowned.")
	}
	if added, e := h.run(ctx, "ufw", "show", "added"); e != nil || strings.Contains(added, "ufw ") {
		reject("Existing UFW user rules require manual review.")
	}
	for _, path := range []string{jailPath, "/etc/systemd/system/" + watchdog + ".service", "/etc/systemd/system/" + watchdog + ".timer"} {
		if _, e := os.Lstat(path); e == nil && s == nil {
			reject("Security files already exist; refusing to adopt them.")
		}
	}
	for _, name := range p.PublicInterfaces {
		if _, e := net.InterfaceByName(name); e != nil {
			reject("A public interface is unavailable.")
		}
	}
	interfaces, interfaceErr := net.Interfaces()
	if interfaceErr != nil {
		reject("Cannot inventory network interfaces.")
	} else {
		for _, iface := range interfaces {
			addresses, e := iface.Addrs()
			if e != nil {
				reject("Cannot inventory interface addresses.")
				continue
			}
			for _, raw := range addresses {
				prefix, e := netip.ParsePrefix(raw.String())
				if e == nil {
					ip := prefix.Addr()
					if ip.IsGlobalUnicast() && !ip.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) && !slices.Contains(p.PublicInterfaces, iface.Name) {
						reject("Every globally addressed interface must be reviewed as public.")
					}
				}
			}
		}
	}
	for _, family := range []string{"-4", "-6"} {
		routes, e := h.run(ctx, "ip", family, "-o", "route", "show", "default")
		if e != nil {
			reject("Cannot inspect default routes.")
			continue
		}
		for _, line := range strings.Split(routes, "\n") {
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "dev" && i+1 < len(fields) && !slices.Contains(p.PublicInterfaces, fields[i+1]) {
					reject("Every default-route interface must be reviewed as public.")
				}
			}
		}
	}
	// Snapshot all listening ports for operator review, without process arguments.
	if _, e := os.Lstat("/etc/vastora-landing/danted.conf"); e == nil {
		reject("This MVP refuses native landing hosts until dynamic UDP return traffic has independent acceptance.")
	}
	if listeners, e := h.run(ctx, "ss", "-H", "-lntu"); e == nil {
		for _, line := range strings.Split(listeners, "\n") {
			if len(v.Listeners) < 256 && len(line) <= 256 {
				v.Listeners = append(v.Listeners, line)
			}
		}
	} else {
		reject("Cannot inventory host listeners.")
	}
	ssh, e := h.run(ctx, "sshd", "-T")
	if e != nil {
		reject("Cannot verify actual SSH ports.")
	} else {
		ports := []int{}
		for _, line := range strings.Split(ssh, "\n") {
			if strings.HasPrefix(line, "port ") {
				n, _ := strconv.Atoi(strings.TrimPrefix(line, "port "))
				ports = append(ports, n)
			}
		}
		slices.Sort(ports)
		expected := slices.Clone(p.SSHPorts)
		slices.Sort(expected)
		if !slices.Equal(ports, expected) {
			reject("Approved SSH ports do not match sshd configuration.")
		}
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		rules, e := h.run(ctx, tool, "-S")
		if e != nil {
			reject("Cannot inspect both IP-family firewall backends.")
			continue
		}
		if strings.Contains(rules, chain) || strings.Contains(rules, "VASTORA-DOCKER") {
			reject("A stale security chain needs explicit recovery.")
		}
		for _, line := range strings.Split(rules, "\n") {
			if strings.HasPrefix(line, "-P INPUT ") && line != "-P INPUT ACCEPT" {
				reject("Existing INPUT policy requires manual review.")
			}
		}
		for _, line := range strings.Split(rules, "\n") {
			if strings.HasPrefix(line, "-A INPUT ") && line != "-A INPUT -j ts-input" {
				reject("Unowned INPUT rules require manual review.")
			}
		}
		// Docker's nftables backend has no DOCKER-USER; never switch backends.
		if strings.Contains(rules, "DOCKER") && !strings.Contains(rules, "-N DOCKER-USER") {
			reject("Docker firewall backend lacks a supported user chain.")
		}
	}
	if _, e := exec.LookPath("docker"); e == nil {
		if _, e = h.run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); e != nil {
			reject("Docker is installed but its backend cannot be verified.")
		}
		if rules, e := h.run(ctx, "iptables", "-S", "DOCKER-USER"); e != nil || !strings.Contains(rules, "DOCKER-USER") {
			reject("MVP Docker support requires the verified iptables DOCKER-USER backend.")
		}
		if err := h.preserveDockerPorts(ctx, p); err != nil {
			reject("Approved rules omit an existing public Docker mapping, or its bindings cannot be verified.")
		}
	}
	if tables, e := h.run(ctx, "nft", "list", "tables"); e != nil {
		reject("Cannot inspect nftables ownership.")
	} else {
		for _, line := range strings.Split(tables, "\n") {
			if strings.Contains(line, "docker-bridges") {
				reject("Docker nftables backend is not supported by this MVP.")
			}
		}
	}
	// Preserve the actual encrypted private transport UDP listener, not an
	// assumed default port. Its public allow rule must be reviewed explicitly.
	if _, e := h.run(ctx, "systemctl", "is-active", "tailscaled"); e == nil {
		listeners, e := h.run(ctx, "ss", "-H", "-lunp")
		if e != nil {
			reject("Cannot verify Tailscale transport.")
		} else {
			found := false
			for _, line := range strings.Split(listeners, "\n") {
				if strings.Contains(line, "tailscaled") {
					fields := strings.Fields(line)
					if len(fields) > 4 {
						_, port, e := net.SplitHostPort(fields[4])
						if e == nil {
							n, _ := strconv.Atoi(port)
							if slices.Contains(p.PublicRules, Rule{Protocol: "udp", Port: n}) {
								found = true
							}
						}
					}
				}
			}
			if !found {
				reject("Approve the actual Tailscale UDP transport port before applying.")
			}
		}
	}
	// Distribution defaults are not assumed safe after administrator edits.
	defaults, e := readOwned("/etc/default/ufw")
	if e != nil || !strings.Contains(string(defaults), "IPV6=yes") {
		reject("UFW IPv6 support must be enabled before applying.")
	}
	return v, nil
}

func supportedDistro(raw string) bool {
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			values[key] = strings.Trim(value, "\"'")
		}
	}
	return values["ID"] == "debian" && slices.Contains([]string{"12", "13"}, values["VERSION_ID"]) || values["ID"] == "ubuntu" && slices.Contains([]string{"22.04", "24.04", "26.04"}, values["VERSION_ID"])
}

func inputRules(p Policy, ipv6 bool) string {
	var b strings.Builder
	for _, iface := range p.PublicInterfaces {
		fmt.Fprintf(&b, "-A ufw-before-input -i %s -j %s\n", iface, chain)
	}
	fmt.Fprintf(&b, "-A %s -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN\n", chain)
	if ipv6 {
		fmt.Fprintf(&b, "-A %s -p ipv6-icmp -j RETURN\n", chain)
	} else {
		fmt.Fprintf(&b, "-A %s -p icmp -j RETURN\n", chain)
	}
	if ipv6 {
		fmt.Fprintf(&b, "-A %s -p udp --sport 547 --dport 546 -j RETURN\n", chain)
	} else {
		fmt.Fprintf(&b, "-A %s -p udp --sport 67 --dport 68 -j RETURN\n", chain)
	}
	for _, source := range p.ManagementSources {
		prefix, _ := netip.ParsePrefix(source)
		if prefix.Addr().Is6() != ipv6 {
			continue
		}
		for _, port := range p.SSHPorts {
			fmt.Fprintf(&b, "-A %s -s %s -p tcp --dport %d -j RETURN\n", chain, source, port)
		}
	}
	for _, rule := range p.PublicRules {
		fmt.Fprintf(&b, "-A %s -p %s --dport %d -j RETURN\n", chain, rule.Protocol, rule.Port)
	}
	fmt.Fprintf(&b, "-A %s -j DROP\n", chain)
	return b.String()
}

func integrateBefore(original []byte, p Policy, ipv6 bool) ([]byte, error) {
	raw := string(original)
	at := strings.Index(raw, "\n-A ")
	if strings.Count(raw, "*filter") != 1 || at < 0 || !strings.Contains(raw, "\n:ufw-before-input ") || strings.Contains(raw, chain) {
		return nil, errors.New("security: unsupported UFW before-rules format")
	}
	return []byte(raw[:at] + "\n:" + chain + " - [0:0]\n" + inputRules(p, ipv6) + raw[at:]), nil
}

func jail(p Policy) []byte {
	ports := []string{}
	for _, port := range p.SSHPorts {
		ports = append(ports, strconv.Itoa(port))
	}
	return []byte("# Managed by Vastora node security\n[DEFAULT]\nenabled = false\n[sshd]\nenabled = true\nbackend = systemd\nfilter = sshd\nport = " + strings.Join(ports, ",") + "\nprotocol = tcp\nbanaction = nftables-multiport\nmaxretry = 5\nfindtime = 10m\nbantime = 10m\nignoreip = 127.0.0.1/8 ::1 " + strings.Join(p.ManagementSources, " ") + "\n")
}

func (h Host) apply(ctx context.Context, t Task, previous *checkpoint) (out View, applyErr error) {
	if previous == nil || previous.View.State != "preview" || previous.View.Revision != t.Revision || previous.View.Policy.Digest() != t.Policy.Digest() || !previous.SSHVerified {
		return View{}, errors.New("security: verify a NEW approved SSH connection against this preview before applying")
	}
	previewDeadline, e := time.Parse(time.RFC3339Nano, previous.View.Deadline)
	if e != nil || !h.now().Before(previewDeadline) {
		return View{}, errors.New("security: preview verification expired; request a new preview")
	}
	v, err := h.preview(ctx, t.Policy, previous)
	if err != nil {
		return v, err
	}
	if !v.Supported {
		return v, errors.New("security: preflight rejected; inspect preview reasons")
	}
	v.Revision = t.Revision
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return v, err
	}
	s := &checkpoint{View: v, BootID: strings.TrimSpace(string(boot)), AppliedAt: h.now().UTC(), Original: map[string][]byte{}, Written: map[string][]byte{}}
	if _, e := exec.LookPath("docker"); e == nil {
		s.Docker = true
	}
	// Keep default UFW policy permissive for existing private/routed traffic;
	// the dedicated public-interface chain is deny-by-default. Never set a
	// blanket routed DROP that would change Docker/Tailscale semantics.
	for _, path := range []string{"/etc/default/ufw", "/etc/ufw/before.rules", "/etc/ufw/before6.rules", "/etc/ufw/ufw.conf", "/etc/ufw/sysctl.conf"} {
		data, e := readOwned(path)
		if e != nil {
			return v, e
		}
		s.Original[path] = data
		s.Written[path] = data
	}
	for _, family := range []struct {
		path string
		v6   bool
	}{{"/etc/ufw/before.rules", false}, {"/etc/ufw/before6.rules", true}} {
		s.Written[family.path], err = integrateBefore(s.Original[family.path], t.Policy, family.v6)
		if err != nil {
			return v, err
		}
	}
	raw := string(s.Written["/etc/default/ufw"])
	for _, key := range []string{"DEFAULT_INPUT_POLICY", "DEFAULT_OUTPUT_POLICY", "DEFAULT_FORWARD_POLICY"} {
		found := false
		lines := strings.Split(raw, "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, key+"=") {
				lines[i] = key + "=\"ACCEPT\""
				found = true
			}
		}
		if !found {
			return v, errors.New("security: unsupported UFW defaults")
		}
		raw = strings.Join(lines, "\n")
	}
	s.Written["/etc/default/ufw"] = []byte(raw)
	s.Written["/etc/ufw/sysctl.conf"] = []byte("# Vastora: preserve existing kernel network tunables.\n")
	s.Written["/etc/ufw/ufw.conf"] = []byte(strings.ReplaceAll(string(s.Original["/etc/ufw/ufw.conf"]), "ENABLED=no", "ENABLED=yes"))
	s.Original[jailPath] = nil
	s.Written[jailPath] = jail(t.Policy)
	s.View.State = "pending_confirmation"
	s.View.Deadline = h.now().UTC().Add(ConfirmationWindowSeconds * time.Second).Format(time.RFC3339Nano)
	if err = saveCheckpoint(s); err != nil {
		return v, err
	}
	defer func() {
		if applyErr != nil {
			recovery, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if rollbackErr := h.rollback(recovery, s); rollbackErr != nil {
				applyErr = errors.Join(applyErr, rollbackErr)
			}
		}
	}()
	if err = h.installWatchdog(ctx); err != nil {
		return s.View, err
	}
	// A running, boot-persistent watchdog precedes every firewall mutation.
	for path, data := range s.Written {
		if path == jailPath {
			continue
		}
		if err = writeOwned(path, data, 0644); err != nil {
			return s.View, err
		}
	}
	if err = writeOwned(jailPath, s.Written[jailPath], 0644); err != nil {
		return s.View, err
	}
	if _, err = h.run(ctx, "fail2ban-client", "-t"); err != nil {
		return s.View, err
	}
	if err = h.restoreDockerGuard(ctx, s); err != nil {
		return s.View, err
	}
	if _, err = h.run(ctx, "ufw", "--force", "enable"); err != nil {
		return s.View, err
	}
	if _, err = h.run(ctx, "systemctl", "enable", "--now", "fail2ban"); err != nil {
		return s.View, err
	}
	if err = h.verifyRuntime(ctx, s); err != nil {
		return s.View, err
	}
	s.AppliedAt = h.now().UTC()
	s.View.AppliedAt = s.AppliedAt.Format(time.RFC3339Nano)
	deadline, _ := time.Parse(time.RFC3339Nano, s.View.Deadline)
	if !h.now().Before(deadline) {
		return s.View, errors.New("security: application exceeded its rollback window")
	}
	return s.View, saveCheckpoint(s)
}

func (h Host) installWatchdog(ctx context.Context) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err = securePath(exe, false); err != nil {
		return err
	}
	if strings.ContainsAny(exe, " \t\n%\\\"") {
		return errors.New("security: unsupported executable path")
	}
	files := map[string]string{
		"/etc/systemd/system/" + watchdog + ".service": "[Unit]\nDescription=Vastora pending security rollback\nAfter=ufw.service\nBefore=docker.service\n[Service]\nType=oneshot\nExecStart=" + exe + " agent security-watchdog\n[Install]\nWantedBy=multi-user.target\n",
		"/etc/systemd/system/" + watchdog + ".timer":   "[Unit]\nDescription=Vastora security rollback deadline\n[Timer]\nOnBootSec=1s\nOnUnitActiveSec=10s\nAccuracySec=1s\n[Install]\nWantedBy=timers.target\n",
	}
	for path, data := range files {
		if existing, e := readOwned(path); e == nil && string(existing) != data {
			return errors.New("security: watchdog ownership conflict")
		} else if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if err = writeOwned(path, []byte(data), 0644); err != nil {
			return err
		}
	}
	if _, err = h.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err = h.run(ctx, "systemctl", "enable", watchdog+".service"); err != nil {
		return err
	}
	_, err = h.run(ctx, "systemctl", "enable", "--now", watchdog+".timer")
	return err
}

func (h Host) verifyFiles(s *checkpoint) error {
	for path, expected := range s.Written {
		data, err := readOwned(path)
		if err != nil || string(data) != string(expected) {
			return errors.New("security: managed configuration drift; inspect before changing policy")
		}
	}
	return nil
}

func (h Host) dockerRules(ctx context.Context, tool string, p Policy, enable bool) error {
	name := "VASTORA-DOCKER"
	if !enable {
		if _, err := h.run(ctx, tool, "-C", "DOCKER-USER", "-j", name); err != nil {
			return nil
		}
		if _, err := h.run(ctx, tool, "-D", "DOCKER-USER", "-j", name); err != nil {
			return err
		}
		if _, err := h.run(ctx, tool, "-F", name); err != nil {
			return err
		}
		_, err := h.run(ctx, tool, "-X", name)
		return err
	}
	if _, err := h.run(ctx, tool, "-N", name); err != nil {
		return err
	}
	for _, iface := range p.PublicInterfaces {
		for _, rule := range p.PublicRules {
			if _, err := h.run(ctx, tool, "-A", name, "-i", iface, "-p", rule.Protocol, "-m", "conntrack", "--ctdir", "ORIGINAL", "--ctorigdstport", strconv.Itoa(rule.Port), "-j", "RETURN"); err != nil {
				return err
			}
		}
		if _, err := h.run(ctx, tool, "-A", name, "-i", iface, "-m", "conntrack", "--ctdir", "ORIGINAL", "--ctstatus", "DNAT", "-j", "DROP"); err != nil {
			return err
		}
	}
	_, err := h.run(ctx, tool, "-I", "DOCKER-USER", "1", "-j", name)
	return err
}

func (h Host) rollback(ctx context.Context, s *checkpoint) error {
	if s.View.State == "rolled_back" {
		return nil
	}
	if s.View.State == "preview" || s.View.State == "unconfigured" {
		s.View.State = "rolled_back"
		s.View.Deadline = ""
		return saveCheckpoint(s)
	}
	// Refuse to overwrite administrator edits. Watchdog reports the conflict;
	// its recovery snapshot remains root-only for out-of-band intervention.
	for path, written := range s.Written {
		data, err := readOwned(path)
		original := s.Original[path]
		if errors.Is(err, os.ErrNotExist) && original == nil {
			continue
		}
		if err != nil || string(data) != string(written) && string(data) != string(original) {
			s.View.State = "action_required"
			s.View.Reasons = []string{"Configuration ownership conflict; protected rollback snapshot retained."}
			_ = saveCheckpoint(s)
			return errors.New("security: rollback ownership conflict")
		}
	}
	if _, err := h.run(ctx, "systemctl", "disable", "--now", "fail2ban"); err != nil {
		return err
	}
	if _, err := h.run(ctx, "ufw", "disable"); err != nil {
		return err
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		if err := h.dockerRules(ctx, tool, s.View.Policy, false); err != nil {
			return err
		}
	}
	for path, data := range s.Original {
		if data == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if err := writeOwned(path, data, 0644); err != nil {
			return err
		}
	}
	// Packages and unrelated configuration are deliberately retained.
	s.View.State = "rolled_back"
	s.View.Deadline = ""
	if err := saveCheckpoint(s); err != nil {
		return err
	}
	if _, err := h.run(ctx, "systemctl", "disable", "--now", watchdog+".timer"); err != nil {
		return err
	}
	_, err := h.run(ctx, "systemctl", "disable", watchdog+".service")
	return err
}

// Watchdog is run by systemd, not an Agent goroutine or Center callback.
func (h Host) Watchdog(ctx context.Context) error {
	_, err := h.Execute(ctx, Task{Action: "inspect"})
	return err
}

// CheckPorts fences publication changes before their runtime mutation. It does
// not silently widen the reviewed public allowlist for a new service.
func (h Host) CheckPorts(rules []Rule) error {
	s, err := loadCheckpoint()
	if err != nil {
		return err
	}
	if s == nil || s.View.State == "rolled_back" {
		return nil
	}
	if s.View.State != "verified" {
		return errors.New("security: finish or roll back the pending baseline before changing public services")
	}
	if err = h.verifyFiles(s); err != nil {
		return err
	}
	for _, rule := range rules {
		if !slices.Contains(s.View.Policy.PublicRules, rule) {
			return errors.New("security: public port is not in the reviewed baseline; review the security policy before publishing")
		}
	}
	return nil
}

func (h Host) CheckRemoval() error {
	s, err := loadCheckpoint()
	if err != nil {
		return err
	}
	if s != nil && s.View.State != "rolled_back" && s.View.State != "unconfigured" && s.View.State != "preview" {
		return errors.New("security: explicitly revert the node security baseline before removing its Agent/watchdog executable")
	}
	if _, err := os.Lstat(preparationPath); err == nil {
		return errors.New("security: reconcile interrupted dependency preparation before Agent removal")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (h Host) CheckDynamicUDP() error {
	s, err := loadCheckpoint()
	if err != nil {
		return err
	}
	if s == nil || s.View.State == "unconfigured" || s.View.State == "preview" || s.View.State == "rolled_back" {
		return nil
	}
	return errors.New("security: explicitly revert this MVP baseline before configuring a dynamic-UDP proxy or landing runtime")
}

func (h Host) restoreDockerGuard(ctx context.Context, s *checkpoint) error {
	if !s.Docker {
		return nil
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		if _, err := h.run(ctx, tool, "-S", "DOCKER-USER"); err != nil {
			if _, err = h.run(ctx, tool, "-N", "DOCKER-USER"); err != nil {
				return err
			}
		}
		if _, err := h.run(ctx, tool, "-C", "DOCKER-USER", "-j", "VASTORA-DOCKER"); err != nil {
			if err = h.dockerRules(ctx, tool, s.View.Policy, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h Host) preserveDockerPorts(ctx context.Context, p Policy) error {
	ids, err := h.run(ctx, "docker", "ps", "-q")
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(ids) {
		if len(id) > 64 || len(id) < 12 {
			return errors.New("security: invalid Docker inventory")
		}
		for _, c := range id {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return errors.New("security: invalid Docker identity")
			}
		}
		raw, err := h.run(ctx, "docker", "inspect", "--format", "{{json .NetworkSettings.Ports}}", id)
		if err != nil {
			return err
		}
		var ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		}
		if json.Unmarshal([]byte(raw), &ports) != nil {
			return errors.New("security: invalid Docker port inventory")
		}
		for containerPort, bindings := range ports {
			_, protocol, ok := strings.Cut(containerPort, "/")
			if !ok {
				return errors.New("security: invalid Docker protocol")
			}
			for _, binding := range bindings {
				ip, err := netip.ParseAddr(binding.HostIP)
				if err != nil {
					return err
				}
				if ip.IsLoopback() {
					continue
				}
				public := ip.IsUnspecified()
				for _, name := range p.PublicInterfaces {
					iface, err := net.InterfaceByName(name)
					if err != nil {
						return err
					}
					addresses, err := iface.Addrs()
					if err != nil {
						return err
					}
					for _, raw := range addresses {
						prefix, err := netip.ParsePrefix(raw.String())
						if err == nil && prefix.Addr() == ip {
							public = true
						}
					}
				}
				if public {
					port, err := strconv.Atoi(binding.HostPort)
					if err != nil || !slices.Contains(p.PublicRules, Rule{Protocol: protocol, Port: port}) {
						return errors.New("security: existing public Docker port is not approved")
					}
				}
			}
		}
	}
	return nil
}
