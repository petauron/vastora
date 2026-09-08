package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/petauron/vastora/internal/dantebundle"
	"github.com/petauron/vastora/internal/landing"
)

const (
	landingHostJournalPath  = "/var/lib/vastora/landing-host.json"
	landingConfigPath       = "/etc/vastora-landing/danted.conf"
	landingUnitPath         = "/etc/systemd/system/vastora-landing.service"
	landingFirewallUnitPath = "/etc/systemd/system/vastora-landing-firewall.service"
	landingBinaryPath       = "/etc/vastora-landing/danted"
)

// This journal owns host files and the dedicated account. Agent's encrypted
// database separately owns desired/applied task revisions and proxy cutovers.
// No SOCKS secrets exist. Persist intent before touching files so interrupted
// writes are recognized on retry without adopting unrelated host resources.
type landingHostJournal struct {
	NodeID         string                  `json:"nodeId"`
	UID            uint32                  `json:"uid"`
	Revision       uint64                  `json:"revision"`
	Files          map[string]string       `json:"files"`
	PendingFiles   map[string]string       `json:"pendingFiles,omitempty"`
	Policy         *landing.ServerFirewall `json:"policy,omitempty"`
	PreviousPolicy *landing.ServerFirewall `json:"previousPolicy,omitempty"`
	Removed        bool                    `json:"removed"`
	PlanDigest     string                  `json:"planDigest,omitempty"`
}

func landingPaths() []string {
	return []string{landingConfigPath, landingBinaryPath, "/etc/vastora-landing/Dante-LICENSE", "/etc/vastora-landing/Dante-NOTICE", "/etc/vastora-landing/resolv.conf", "/etc/vastora-landing/nsswitch.conf", landingUnitPath, landingFirewallUnitPath}
}

func landingDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func readLandingHostJournal() (*landingHostJournal, error) {
	data, err := readLandingOwnedFile(landingHostJournalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var journal landingHostJournal
	if json.Unmarshal(data, &journal) != nil || journal.NodeID == "" || journal.Files == nil {
		return nil, errors.New("agent: invalid landing host ownership record")
	}
	allowed := map[string]bool{}
	for _, path := range landingPaths() {
		allowed[path] = true
	}
	for _, files := range []map[string]string{journal.Files, journal.PendingFiles} {
		for path, digest := range files {
			decoded, err := hex.DecodeString(digest)
			if !allowed[path] || err != nil || len(decoded) != sha256.Size {
				return nil, errors.New("agent: invalid landing file ownership record")
			}
		}
	}
	return &journal, nil
}

// Reject symlink ancestors and non-root/writable configuration. This code is
// Linux host installation only; it never follows an arbitrary request path.
func verifyLandingParent(path string) error {
	for parent := filepath.Dir(path); parent != "/"; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return errors.New("agent: cannot inspect landing directory")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
			return errors.New("agent: landing directory is not privately owned")
		}
	}
	return nil
}

func readLandingOwnedFile(path string) ([]byte, error) {
	if err := verifyLandingParent(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	limit := int64(128 << 10)
	if path == landingBinaryPath {
		limit = dantebundle.MaxBinarySize
	}
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || info.Size() > limit {
		return nil, errors.New("agent: landing file ownership is invalid")
	}
	return os.ReadFile(path)
}

func saveLandingHostJournal(journal *landingHostJournal) error {
	if err := verifyLandingParent(landingHostJournalPath); err != nil {
		return err
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return errors.New("agent: cannot encode landing ownership record")
	}
	return writeHostFileAtomic(landingHostJournalPath, data, 0o600)
}

func verifyLandingFiles(journal *landingHostJournal) error {
	for _, path := range landingPaths() {
		data, err := readLandingOwnedFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		digest := landingDigest(data)
		if digest != journal.Files[path] && digest != journal.PendingFiles[path] {
			return errors.New("agent: landing files changed outside Vastora")
		}
	}
	return nil
}

type landingCommandOutput struct{ bytes.Buffer }

func (output *landingCommandOutput) Write(data []byte) (int, error) {
	// Apt output is not returned to users, but must not grow Agent memory
	// without bound on a small landing machine. Keep only a bounded prefix.
	length := len(data)
	if remaining := (64 << 10) - output.Len(); remaining > 0 {
		_, _ = output.Buffer.Write(data[:min(length, remaining)])
	}
	return length, nil
}

func landingCommand(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
	// Command arguments are fixed service/package names. Do not return raw
	// package-manager output as a user-facing task error.
	var output landingCommandOutput
	command.Stdout = &output
	if err := command.Run(); err != nil {
		return "", errors.New("agent: landing host operation failed")
	}
	return strings.TrimSpace(output.String()), nil
}

func ensureLandingPackage(ctx context.Context) error {
	nftState, _ := landingCommand(ctx, "dpkg-query", "-W", "-f=${Status}", "nftables")
	if nftState == "install ok installed" {
		return nil
	}
	if _, err := landingCommand(ctx, "apt-get", "update", "-qq"); err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "apt-get", "install", "-y", "--no-install-recommends", "nftables"); err != nil {
		return err
	}
	return nil
}

func landingHostAddresses(privateAddress string) (string, string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", "", err
	}
	device := ""
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, value := range addresses {
			address, _, err := net.ParseCIDR(value.String())
			if err == nil && address.String() == privateAddress {
				device = iface.Name
			}
		}
	}
	if device == "" {
		return "", "", errors.New("agent: landing private address is not present on this host")
	}
	// UDP connect selects a source route without transmitting a packet.
	connection, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.ParseIP("1.1.1.1"), Port: 53})
	if err != nil {
		return "", "", errors.New("agent: landing egress route is unavailable")
	}
	defer connection.Close()
	return device, connection.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func stopLandingService(ctx context.Context) error {
	if _, err := os.Lstat(landingUnitPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "systemctl", "stop", "vastora-landing.service"); err != nil {
		return err
	}
	state, err := landingCommand(ctx, "systemctl", "show", "-p", "ActiveState", "--value", "vastora-landing.service")
	if err != nil || state != "inactive" && state != "failed" {
		return errors.New("agent: landing service stop was not confirmed")
	}
	return nil
}

// RemoveLanding disables only the native instance owned by this Agent. Keep
// the dedicated account and package: deleting shared packages or recycling its
// UID could affect unrelated services. A tombstone fences older task retries.
func RemoveLanding(ctx context.Context, nodeID string, revision uint64) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || nodeID == "" || revision == 0 {
		return errors.New("agent: landing removal requires a managed Linux root Agent")
	}
	journal, err := readLandingHostJournal()
	if err != nil {
		return err
	}
	if journal == nil {
		return saveLandingHostJournal(&landingHostJournal{NodeID: nodeID, Revision: revision, Files: map[string]string{}, Removed: true})
	}
	if journal.NodeID != nodeID || revision < journal.Revision || revision == journal.Revision && !journal.Removed {
		return errors.New("agent: landing removal identity or revision does not match")
	}
	if err := verifyLandingFiles(journal); err != nil {
		return err
	}
	if err := stopLandingService(ctx); err != nil {
		return err
	}
	// Persist the stop intent before removing the prerequisite or any policy;
	// a reboot during cleanup must not permit the old service to start again.
	journal.Removed, journal.Revision = true, revision
	if err := saveLandingHostJournal(journal); err != nil {
		return err
	}
	if _, err := os.Lstat(landingUnitPath); err == nil {
		if _, err := landingCommand(ctx, "systemctl", "disable", "vastora-landing.service"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := os.Lstat(landingFirewallUnitPath); err == nil {
		if _, err := landingCommand(ctx, "systemctl", "stop", "vastora-landing-firewall.service"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, policy := range []*landing.ServerFirewall{journal.PreviousPolicy, journal.Policy} {
		if policy != nil {
			if err := policy.Remove(ctx); err != nil {
				return err
			}
		}
	}
	for _, path := range landingPaths() {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := landingCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	journal.Files, journal.PendingFiles = map[string]string{}, nil
	journal.Policy, journal.PreviousPolicy = nil, nil
	return saveLandingHostJournal(journal)
}

// ApplyLanding installs a native service; it never installs Docker, x-ui or
// compilers. The caller has persisted the desired revision and serialized tasks.
func ApplyLanding(ctx context.Context, nodeID string, plan landing.ServerPlan) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || nodeID == "" {
		return errors.New("agent: landing installation requires a managed Linux root Agent")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	binary, license, notice, err := dantebundle.Read()
	if err != nil {
		return err
	}
	journal, err := readLandingHostJournal()
	if err != nil {
		return err
	}
	if journal == nil {
		for _, path := range landingPaths() {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return errors.New("agent: refusing to adopt existing landing files")
			}
		}
		if _, err := user.Lookup("vastora-landing"); err == nil {
			return errors.New("agent: refusing to adopt an existing landing account")
		}
		journal = &landingHostJournal{NodeID: nodeID, Files: map[string]string{}}
		if err := saveLandingHostJournal(journal); err != nil {
			return err
		}
	}
	if journal.NodeID != nodeID || journal.Revision > plan.Revision {
		return errors.New("agent: landing host identity or revision does not match")
	}
	if journal.Removed && plan.Revision <= journal.Revision {
		return errors.New("agent: landing revision was removed")
	}
	planJSON, _ := json.Marshal(plan)
	digest := landingDigest(planJSON)
	if journal.Revision == plan.Revision && journal.PlanDigest != "" && journal.PlanDigest != digest {
		return errors.New("agent: landing plan changed without a new revision")
	}
	if err := verifyLandingFiles(journal); err != nil {
		return err
	}
	journal.Revision, journal.PlanDigest, journal.Removed = plan.Revision, digest, false
	if err := saveLandingHostJournal(journal); err != nil {
		return err
	}
	if err := ensureLandingPackage(ctx); err != nil {
		return err
	}
	account, err := user.Lookup("vastora-landing")
	if err != nil {
		if _, err := landingCommand(ctx, "useradd", "--system", "--user-group", "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "vastora-landing"); err != nil {
			return err
		}
		account, err = user.Lookup("vastora-landing")
	}
	if err != nil {
		return errors.New("agent: landing service account is unavailable")
	}
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil || uid == 0 || uid == 65534 || journal.UID != 0 && journal.UID != uint32(uid) {
		return errors.New("agent: landing service account identity changed")
	}
	journal.UID = uint32(uid)
	if err := saveLandingHostJournal(journal); err != nil {
		return err
	}
	device, egress, err := landingHostAddresses(plan.Address)
	if err != nil {
		return err
	}
	configuration, err := plan.RenderDante(egress)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil || !filepath.IsAbs(executable) || strings.ContainsAny(executable, " \t\n\r%\"\\") {
		return errors.New("agent: unsupported Agent executable path for landing boot service")
	}
	firewallUnit := "# Managed by Vastora\n[Unit]\nDescription=Vastora landing firewall\nBefore=vastora-landing.service\nAfter=tailscaled.service\n[Service]\nType=oneshot\nRemainAfterExit=yes\nExecStart=" + executable + " agent landing-firewall\n"
	files := map[string][]byte{landingConfigPath: configuration, "/etc/vastora-landing/resolv.conf": []byte(landing.NativeResolver), "/etc/vastora-landing/nsswitch.conf": []byte(landing.NativeNSS), landingUnitPath: []byte(landing.NativeUnit), landingFirewallUnitPath: []byte(firewallUnit)}
	files[landingBinaryPath] = binary
	files["/etc/vastora-landing/Dante-LICENSE"] = license
	files["/etc/vastora-landing/Dante-NOTICE"] = notice
	policy := landing.ServerFirewall{Revision: plan.Revision, Address: plan.Address, Interface: device, UID: journal.UID}
	for _, source := range plan.Sources {
		policy.Sources = append(policy.Sources, source.Address)
	}
	if err := stopLandingService(ctx); err != nil {
		return err
	}
	// Resume an interrupted firewall transition while the daemon is stopped.
	// Keep both policies recorded until the old table removal is confirmed.
	if journal.PreviousPolicy != nil {
		if journal.Policy == nil {
			return errors.New("agent: incomplete landing firewall ownership")
		}
		if err := journal.Policy.Install(ctx); err != nil {
			return err
		}
		oldJSON, _ := json.Marshal(journal.PreviousPolicy)
		newJSON, _ := json.Marshal(journal.Policy)
		if !bytes.Equal(oldJSON, newJSON) {
			if err := journal.PreviousPolicy.Remove(ctx); err != nil {
				return err
			}
		}
		journal.PreviousPolicy = nil
		if err := saveLandingHostJournal(journal); err != nil {
			return err
		}
	}
	// Preserve hashes of partially written files before replacing pending intent.
	for path, digest := range journal.PendingFiles {
		if data, err := readLandingOwnedFile(path); err == nil && landingDigest(data) == digest {
			journal.Files[path] = digest
		}
	}
	journal.PreviousPolicy, journal.Policy = journal.Policy, &policy
	journal.PendingFiles = map[string]string{}
	for path, data := range files {
		journal.PendingFiles[path] = landingDigest(data)
	}
	journal.Revision, journal.Removed = plan.Revision, false
	if err := saveLandingHostJournal(journal); err != nil {
		return err
	}
	if err := policy.Install(ctx); err != nil {
		return err
	}
	if journal.PreviousPolicy != nil {
		oldJSON, _ := json.Marshal(journal.PreviousPolicy)
		newJSON, _ := json.Marshal(policy)
		if !bytes.Equal(oldJSON, newJSON) {
			if err := journal.PreviousPolicy.Remove(ctx); err != nil {
				return err
			}
		}
	}
	journal.PreviousPolicy = nil
	if err := saveLandingHostJournal(journal); err != nil {
		return err
	}
	for _, path := range landingPaths() {
		if err := verifyLandingParent(path); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if path == landingBinaryPath {
			mode = 0o755
		}
		if err := writeHostFileAtomic(path, files[path], mode); err != nil {
			return err
		}
	}
	if _, err := landingCommand(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "systemctl", "enable", "--now", "vastora-landing.service"); err != nil {
		return err
	}
	if _, err := landingCommand(ctx, "systemctl", "is-active", "--quiet", "vastora-landing.service"); err != nil {
		return err
	}
	// A self-dial is intentionally denied by the source allowlist. Inspect the
	// kernel listener owned by the dedicated UID instead; actual remote TCP/UDP
	// business health is established by the selected proxy node's probe.
	listeners, err := os.ReadFile("/proc/net/tcp")
	if err != nil || !landingListenerPresent(listeners, plan.Address, journal.UID) {
		return errors.New("agent: landing private listener did not become ready")
	}
	journal.Files, journal.PendingFiles = journal.PendingFiles, nil
	return saveLandingHostJournal(journal)
}

func landingListenerPresent(data []byte, address string, uid uint32) bool {
	ip := net.ParseIP(address).To4()
	if ip == nil {
		return false
	}
	wanted := fmt.Sprintf("%02X%02X%02X%02X:%04X", ip[3], ip[2], ip[1], ip[0], landing.SOCKSPort)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 10 && fields[1] == wanted && fields[3] == "0A" && fields[7] == strconv.FormatUint(uint64(uid), 10) {
			return true
		}
	}
	return false
}

// RestoreLandingFirewall is called by a root oneshot prerequisite. It does
// not fetch configuration or contact Center and never starts the proxy itself.
func RestoreLandingFirewall(ctx context.Context) error {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("agent: landing firewall requires Linux root")
	}
	journal, err := readLandingHostJournal()
	if err != nil || journal == nil || journal.Policy == nil || journal.Removed {
		return errors.New("agent: landing firewall has no owned configuration")
	}
	if err := verifyLandingFiles(journal); err != nil {
		return err
	}
	for _, path := range landingPaths() {
		if _, err := readLandingOwnedFile(path); err != nil {
			return errors.New("agent: landing boot configuration is incomplete")
		}
	}
	account, err := user.Lookup("vastora-landing")
	if err != nil || account.Uid != strconv.FormatUint(uint64(journal.UID), 10) || journal.Policy.UID != journal.UID {
		return errors.New("agent: landing firewall account identity changed")
	}
	return journal.Policy.Install(ctx)
}
