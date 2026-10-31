package osreinstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type limitedOutput struct {
	buf bytes.Buffer
	max int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > w.max-w.buf.Len() {
		return 0, errors.New("reinstall: inspection output is too large")
	}
	return w.buf.Write(p)
}

func readCommand(ctx context.Context, command string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	output := &limitedOutput{max: 2 << 20}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return nil, errors.New("reinstall: required host inspection failed")
	}
	return output.buf.Bytes(), nil
}

// InspectHost executes fixed, read-only system queries. It never runs the
// reinstall tool, opens a listener, changes the network, or touches the disk.
func InspectHost(parent context.Context) (Inspection, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	virt, err := readCommand(ctx, "systemd-detect-virt", "--vm")
	if err != nil {
		return Inspection{}, errors.New("reinstall: KVM detection failed")
	}
	blocks, err := readCommand(ctx, "lsblk", "--tree", "-b", "-J", "-o", "PATH,TYPE,SIZE,MAJ:MIN,PTUUID")
	if err != nil {
		return Inspection{}, err
	}
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return Inspection{}, errors.New("reinstall: mount inventory is unavailable")
	}
	blocks, err = BindCriticalMounts(blocks, mountInfo)
	if err != nil {
		return Inspection{}, err
	}
	addresses, err := readCommand(ctx, "ip", "-d", "-j", "address", "show")
	if err != nil {
		return Inspection{}, err
	}
	routes4, err := readCommand(ctx, "ip", "-j", "-4", "route", "show", "default")
	if err != nil {
		return Inspection{}, err
	}
	routes6, err := readCommand(ctx, "ip", "-j", "-6", "route", "show", "default")
	if err != nil {
		return Inspection{}, err
	}
	resolver, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return Inspection{}, errors.New("reinstall: DNS configuration is unavailable")
	}
	inspection, err := InspectEvidence(Evidence{
		Virtualization: strings.TrimSpace(string(virt)), Architecture: runtime.GOARCH,
		BlockDevices: blocks, Addresses: addresses, RoutesV4: routes4,
		RoutesV6: routes6, ResolvConf: resolver,
	})
	if err != nil {
		return Inspection{}, err
	}
	inspection.Hostname, _ = os.Hostname()
	if data, e := os.ReadFile("/proc/sys/kernel/random/boot_id"); e == nil {
		inspection.BootID = strings.ToLower(strings.TrimSpace(string(data)))
	}
	if data, e := os.ReadFile("/etc/os-release"); e == nil && len(data) <= 64<<10 {
		inspection.OSID, inspection.OSVersion = readOSReleaseIdentity(data)
	}
	if _, e := os.Stat("/sys/firmware/efi"); e == nil {
		inspection.BootMode = "efi"
	} else if errors.Is(e, os.ErrNotExist) {
		inspection.BootMode = "bios"
	}
	inspection.NetworkCandidate = findStaticNetworkCandidate(inspection.Network, resolver)
	inspection.MissingPrerequisites = MissingPrerequisites()
	// An authenticated report is useful for comparing the host identity after
	// reinstallation. This is only the public fingerprint, never a private key.
	if data, e := os.ReadFile("/etc/ssh/ssh_host_ed25519_key.pub"); e == nil && len(data) <= 4096 {
		key, _, _, rest, e := ssh.ParseAuthorizedKey(data)
		if e == nil && len(bytes.TrimSpace(rest)) == 0 && key.Type() == ssh.KeyAlgoED25519 {
			inspection.SSHHostKeySHA256 = ssh.FingerprintSHA256(key)
		}
	}
	if err := inspection.Validate(); err != nil {
		return Inspection{}, err
	}
	return inspection, nil
}

// This reports a candidate only. It does not remove the network review fence:
// other routing policy, active managers and provider NAT still need checking.
func findStaticNetworkCandidate(observed Network, resolver []byte) string {
	return readNetworkCandidate(observed, resolver)
}

// readOSReleaseIdentity extracts only simple ID/VERSION_ID values; it never
// executes shell expressions from a potentially modified host file.
func readOSReleaseIdentity(data []byte) (string, string) {
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "ID" && key != "VERSION_ID") {
			continue
		}
		if _, duplicate := values[key]; duplicate {
			return "", ""
		}
		if len(value) > 0 && value[0] == '"' {
			if len(value) < 2 || value[len(value)-1] != '"' {
				return "", ""
			}
			value = value[1 : len(value)-1]
		}
		if value == "" || len(value) > 32 {
			return "", ""
		}
		for _, r := range value {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
				return "", ""
			}
		}
		values[key] = value
	}
	return values["ID"], values["VERSION_ID"]
}

// MissingPrerequisites reports only tools needed by the reviewed installer.
// It does not install packages or change software sources.
func MissingPrerequisites() []string {
	missing := []string{}
	for _, name := range []string{"bash", "curl", "grep", "gzip", "cpio", "ip", "lsblk", "blkid", "dpkg-deb", "grub-editenv", "grub-reboot"} {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	roots, err := loadSystemRoots()
	if err != nil || roots == nil || len(roots.Subjects()) == 0 {
		missing = append(missing, "ca-certificates")
	}
	return missing
}
