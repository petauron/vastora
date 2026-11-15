package osreinstall

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
)

// InstallerSpec is built only from a reviewed and rechecked host snapshot. It
// contains no password or private key. This generator does not execute or arm
// the installer; callers must first protect the Agent and network recovery data.
type InstallerSpec struct {
	StageDir         string
	TaskID           string
	ExecutionID      string
	Architecture     string
	BootMode         string
	Username         string
	SSHPort          int
	SSHHostKeySHA256 string
	// SudoPolicy must be independently established from the original host.
	// An empty or unknown value is rejected; reinstall must not grant new access.
	SudoPolicy     string
	Hostname       string
	AgentID        string
	IdentitySHA256 string
	Disk           Disk
	Network        StaticNetworkPlan
}

type InstallerScripts struct {
	Installer  []byte
	Preseed    []byte
	Network    []byte
	FirstBoot  []byte
	Link       []byte
	Interfaces []byte
	Resolver   []byte
}

var safeStagePath = regexp.MustCompile(`^/var/lib/[A-Za-z0-9_/-]+$`)
var safeUser = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)
var safeHostname = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
var safeAgentID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var safeAgentHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func replaceExactly(value, old, next string) (string, error) {
	if strings.Count(value, old) != 1 {
		return "", errors.New("reinstall: reviewed installer source changed")
	}
	return strings.Replace(value, old, next, 1), nil
}

func replaceReviewedFunction(value, name, replacement string) (string, error) {
	pattern := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(name) + `\(\) \{\n.*?^\}`)
	matches := pattern.FindAllStringIndex(value, -1)
	if len(matches) != 1 {
		return "", errors.New("reinstall: reviewed installer function changed")
	}
	m := matches[0]
	return value[:m[0]] + replacement + value[m[1]:], nil
}

// BuildInstallerScripts binds every executable input to the reviewed upstream
// digest before applying exact, fail-closed substitutions. This supports only
// the reviewed physical-network plan; installer transport uses its primary IPv4.
func BuildInstallerScripts(source map[string][]byte, spec InstallerSpec) (InstallerScripts, error) {
	fail := func() (InstallerScripts, error) {
		return InstallerScripts{}, errors.New("reinstall: unsafe or incomplete installer specification")
	}
	if !safeAgentID.MatchString(spec.TaskID) || !safeAgentID.MatchString(spec.ExecutionID) || spec.Architecture != "amd64" || spec.BootMode != "bios" || !deviceIDPattern.MatchString(spec.Disk.MajorMinor) || spec.Disk.Bytes < 4<<30 || !safeStagePath.MatchString(spec.StageDir) || path.Clean(spec.StageDir) != spec.StageDir || strings.HasSuffix(spec.StageDir, "/") || !safeUser.MatchString(spec.Username) || !hostFingerprintPattern.MatchString(spec.SSHHostKeySHA256) || spec.SSHPort < 1 || spec.SSHPort > 65535 || (spec.SudoPolicy != "none" && spec.SudoPolicy != "passwordless_all") || (spec.Username == "root" && spec.SudoPolicy != "none") || !safeHostname.MatchString(spec.Hostname) || !safeAgentID.MatchString(spec.AgentID) || !safeAgentHash.MatchString(spec.IdentitySHA256) || !devicePattern.MatchString(spec.Disk.Path) || !partitionIDPattern.MatchString(strings.ToLower(spec.Disk.PartitionTableID)) {
		return fail()
	}
	link, interfaces, resolver, err := spec.Network.RenderStaticNetwork()
	if err != nil {
		return fail()
	}
	for name, digest := range reinstallSourceHashes {
		if !sameSHA256(source[name], digest) {
			return InstallerScripts{}, errors.New("reinstall: installer source digest mismatch")
		}
	}
	var installer = string(source["reinstall.sh"])
	apply := func(old, next string) error {
		var e error
		installer, e = replaceExactly(installer, old, next)
		return e
	}
	applyFn := func(name, next string) error {
		var e error
		installer, e = replaceReviewedFunction(installer, name, next)
		return e
	}
	stage := spec.StageDir
	diskGuard := fmt.Sprintf(`command -v lsblk >/dev/null && command -v blkid >/dev/null || error_and_exit "disk verification tools unavailable"
[ "$(lsblk -dn -o MAJ:MIN %s | tr -d '[:space:]')" = "%s" ] || error_and_exit "system disk identity changed"
[ "$(blkid -s PTUUID -o value %s | tr '[:upper:]' '[:lower:]')" = "%s" ] || error_and_exit "system disk partition identity changed"
mkdir_clear "$tmp"`, spec.Disk.Path, spec.Disk.MajorMinor, spec.Disk.Path, strings.ToLower(spec.Disk.PartitionTableID))
	replacements := [][2]string{
		{"set -eE\n", "set -eE\numask 077\n"},
		{"mkdir_clear \"$tmp\"", diskGuard},
		{"if [ -f /etc/default/kexec ]; then\n    sed -i 's/LOAD_KEXEC=true/LOAD_KEXEC=false/' /etc/default/kexec\nfi", "if [ -f /etc/default/kexec ] && grep -q '^LOAD_KEXEC=true' /etc/default/kexec; then error_and_exit \"kexec requires review\"; fi"},
		{"\nremove_exist_reinstall\n# trap 'reset_and_exit true' SIGINT", "\n# The caller has verified no previous reinstall boot entry exists.\n# trap 'reset_and_exit true' SIGINT"},
		{"confhome=https://raw.githubusercontent.com/bin456789/reinstall/main", "confhome=file://" + stage},
		{"    setos nextos $distro $releasever\n", "    setos nextos $distro $releasever\n    nextos_vmlinuz=file://" + stage + "/debian13-amd64-linux\n    nextos_initrd=file://" + stage + "/debian13-amd64-initrd\n    nextos_udeb_mirror=deb.debian.org/debian\n    nextos_deb_mirror=deb.debian.org/debian\n"},
		{"        nextos_cmdline+=\" url=$nextos_ks\"", "        nextos_cmdline+=\" file=/preseed.cfg\""},
		{"    curl -Lo $initrd_dir/initrd-network.sh $confhome/initrd-network.sh", "    cp " + stage + "/network-adapter.sh $initrd_dir/initrd-network.sh"},
		{"    curl -LO \"$confhome/fix-eth-name.sh\"", "    cp " + stage + "/fix-eth-name.hardened.sh ./fix-eth-name.sh"},
		{"    mkdir -p $initrd_dir/configs\n", "    mkdir -p $initrd_dir/configs\n    cp " + stage + "/debian-hardened.cfg $initrd_dir/preseed.cfg\n    cp " + stage + "/network.rules $initrd_dir/configs/network.rules\n    cp " + stage + "/network.interfaces $initrd_dir/configs/network.interfaces\n    cp " + stage + "/agent-backup.enc $initrd_dir/configs/agent-backup.enc\n    cp " + stage + "/recovery.password $initrd_dir/configs/recovery.password\n    cp " + stage + "/resolv-before.conf $initrd_dir/configs/resolv-before.conf\n    cp " + stage + "/agent-unit-before.service $initrd_dir/configs/agent-unit-before.service\n    cp " + stage + "/reconnect.json $initrd_dir/configs/reconnect.json\n    cp " + stage + "/ssh-config.tar.gz $initrd_dir/configs/ssh-config.tar.gz\n    cp " + stage + "/root-authorized-keys $initrd_dir/configs/root-authorized-keys\n    cp /usr/local/bin/vastora $initrd_dir/configs/vastora\n    chmod 600 $initrd_dir/configs/agent-backup.enc $initrd_dir/configs/recovery.password\n"},
		{"# 检查 root\nif is_in_windows; then", "install_pkg() {\n    local cmd\n    for cmd in \"$@\"; do\n        command -v \"$cmd\" >/dev/null || error_and_exit \"required tool unavailable: $cmd\"\n    done\n}\n# 检查 root\nif is_in_windows; then"},
	}
	for _, pair := range replacements {
		if err := apply(pair[0], pair[1]); err != nil {
			return InstallerScripts{}, err
		}
	}
	for _, f := range [][2]string{{"get_ip_conf_cmd", fmt.Sprintf(`get_ip_conf_cmd() {
    echo "'/initrd-network.sh' '%s' '%s' '%s' '' '' 'false' ''"
}`, spec.Network.MAC, spec.Network.Address, spec.Network.Gateway)}, {"is_in_china", "is_in_china() { return 1; }"}, {"is_need_web_viewer", "is_need_web_viewer() { return 1; }"}, {"test_url", "test_url() { error_and_exit \"unexpected mutable image URL\"; }"}, {"curl", `curl() {
    local arg
    for arg in "$@"; do
        case "$arg" in
            http://*|https://*) error_and_exit "unexpected network download in verified preparation" ;;
            file://*) [[ "$arg" == file://` + stage + `/* ]] || error_and_exit "unexpected local installer source" ;;
        esac
    done
    command curl --fail --show-error "$@"
}`}} {
		if err := applyFn(f[0], f[1]); err != nil {
			return InstallerScripts{}, err
		}
	}
	installer = strings.ReplaceAll(installer, "command curl --insecure ", "command curl ")
	start := strings.Index(installer, "    # 提前下载 sshd\n")
	end := strings.Index(installer, "    # 提前下载 pci-hyperv\n")
	if start < 0 || end <= start || strings.Count(installer, "    # 提前下载 sshd\n") != 1 || strings.Count(installer, "    # 提前下载 pci-hyperv\n") != 1 {
		return fail()
	}
	installer = installer[:start] + "    mkdir_clear \"$tmp/fdisk\"\n    dpkg-deb -x " + stage + "/debian13-amd64-fdisk \"$tmp/fdisk\"\n    cp -f \"$tmp/fdisk/usr/sbin/fdisk\" usr/sbin/\n\n" + installer[end:]
	for _, line := range strings.Split(installer, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "--insecure") {
			return fail()
		}
	}

	preseed := string(source["debian.cfg"])
	patch := func(old, next string) error { var e error; preseed, e = replaceExactly(preseed, old, next); return e }
	targetDisk := strings.TrimPrefix(spec.Disk.Path, "/dev/")
	insertRestore := fmt.Sprintf(`d-i preseed/late_command string true; \
    ( set -e; \
      mkdir -p /target/usr/local/bin /target/var/lib/vastora /target/root/vastora-reinstall; \
      cp /configs/vastora /target/usr/local/bin/vastora; \
      chmod 0755 /target/usr/local/bin/vastora; \
      cp /configs/agent-backup.enc /target/root/vastora-reinstall/agent-backup.enc; \
      cp /configs/recovery.password /target/root/vastora-reinstall/recovery.password; \
      chmod 0700 /target/root/vastora-reinstall; \
      chmod 0600 /target/root/vastora-reinstall/agent-backup.enc /target/root/vastora-reinstall/recovery.password; \
      in-target /usr/local/bin/vastora recovery restore --input /root/vastora-reinstall/agent-backup.enc --data-dir /var/lib/vastora/agent --password-file /root/vastora-reinstall/recovery.password --component-id %s --identity-sha256 %s >/dev/null; \
      cp /configs/reconnect.json /target/var/lib/vastora/agent/os-reinstall-reconnect.json; \
      chmod 0600 /target/var/lib/vastora/agent/os-reinstall-reconnect.json; \
      mkdir -p /target/etc/udev/rules.d; \
      cp /configs/network.rules /target/etc/udev/rules.d/70-vastora-reinstall-net.rules; \
      cp /configs/network.interfaces /target/etc/network/interfaces; \
      chmod 0644 /target/etc/network/interfaces; \
      cp /configs/agent-unit-before.service /target/etc/systemd/system/vastora-agent.service; \
      in-target systemctl enable vastora-agent; \
      rm /target/etc/resolv.conf; \
      cp /configs/resolv-before.conf /target/etc/resolv.conf; \
      chmod 0644 /target/etc/resolv.conf; \
      rm /target/root/vastora-reinstall/agent-backup.enc /target/root/vastora-reinstall/recovery.password; \
      rmdir /target/root/vastora-reinstall; \
    ) || exit 1; \
`, spec.AgentID, spec.IdentitySHA256)
	if !spec.Network.DynamicDNS && (spec.Network.DHCP4 || spec.Network.DHCP6) {
		insertRestore += "      printf 'nohook resolv.conf\\n' >>/target/etc/dhcpcd.conf; \\\n"
	}
	diskInitrdGuard := fmt.Sprintf(`    [ "$(cat /sys/class/block/$xda/dev)" = "%s" ] || exit 1; \
    [ "$(blkid -s PTUUID -o value /dev/$xda | tr '[:upper:]' '[:lower:]')" = "%s" ] || exit 1; \
`, spec.Disk.MajorMinor, strings.ToLower(spec.Disk.PartitionTableID))
	// Restore the reviewed SSH configuration after upstream account setup.
	sshRestore := `    ( set -e; \
      rm -rf /target/etc/ssh; \
      tar -xzf /configs/ssh-config.tar.gz -C /target; \
      mkdir -p /target/root/.ssh /target/run/sshd; \
      cp /configs/root-authorized-keys /target/root/.ssh/authorized_keys; \
      chmod 0700 /target/root/.ssh; \
      chmod 0600 /target/root/.ssh/authorized_keys; \
      in-target /usr/sbin/sshd -t; \
    ) || exit 1; \
    cp /fix-eth-name.sh /target/; \
`
	preseedChanges := [][2]string{
		{"d-i pkgsel/upgrade select none\n", "d-i pkgsel/upgrade select none\nd-i pkgsel/include string sudo\n"},
		{"d-i netcfg/get_hostname string unassigned-hostname", "d-i netcfg/get_hostname string " + spec.Hostname},
		{"d-i netcfg/hostname string localhost", "d-i netcfg/hostname string " + spec.Hostname},
		{"    chmod +s /usr/bin/screen; \\\n", "    true; \\\n"},
		{"    screen -x root/ -X multiuser on; \\\n", "    true; \\\n"},
		{"    screen -x root/ -X acladd \"$username\"; \\\n", "    true; \\\n"},
		{"        if command -v websocketd && [ \"$mem\" -ge 400 ]; then \\\n", "        if false; then \\\n"},
		{"    ssh-keygen -A; \\\n    run_as_service_with_screen /usr/sbin/sshd -D; \\\n", "    true; \\\n"},
		{"    xda=$(sh /get-xda.sh); \\\n", "    xda=$(sh /get-xda.sh) || exit 1; \\\n    [ \"$xda\" = \"" + targetDisk + "\" ] || exit 1; \\\n" + diskInitrdGuard},
		{"d-i preseed/late_command string true; \\\n", insertRestore},
		{"    cp /fix-eth-name.sh /target/; \\\n", sshRestore},
	}
	if spec.SudoPolicy == "none" {
		// Upstream grants full passwordless sudo to every non-root user.
		// Never create it without independent proof it already existed.
		preseedChanges = append(preseedChanges, [2]string{`    if ! [ "$username" = root ]; then \
        printf '%s\n' "$username ALL=(ALL) NOPASSWD:ALL" >"/target/etc/sudoers.d/99-$username"; \
        chmod 0440 "/target/etc/sudoers.d/99-$username"; \
    fi; \
`, `    true; \
`})
	}
	for _, pair := range preseedChanges {
		if err := patch(pair[0], pair[1]); err != nil {
			return InstallerScripts{}, err
		}
	}
	// The launcher must not silently select a different reviewed SSH account
	// or port. Check them again inside the installer before touching disks.
	userLine := "    username=${username:-root}; \\\n"
	portLine := "    ssh_port=${ssh_port:-22}; \\\n"
	if strings.Count(preseed, userLine) != 3 || strings.Count(preseed, portLine) != 3 {
		return fail()
	}
	preseed = strings.ReplaceAll(preseed, userLine, userLine+"    [ \"$username\" = \""+spec.Username+"\" ] || exit 1; \\\n")
	preseed = strings.ReplaceAll(preseed, portLine, portLine+fmt.Sprintf("    [ \"$ssh_port\" = \"%d\" ] || exit 1; \\\n", spec.SSHPort))
	var firstBoot strings.Builder
	firstBoot.WriteString("#!/bin/sh\nset -eu\n")
	fmt.Fprintf(&firstBoot, "printf '%%s\\n' %s >/etc/hostname\nhostname %s\n", spec.Hostname, spec.Hostname)
	for _, nic := range planNICs(spec.Network) {
		fmt.Fprintf(&firstBoot, "[ \"$(cat /sys/class/net/%s/address)\" = \"%s\" ] || exit 1\n", nic.Interface, nic.MAC)
	}

	network := fmt.Sprintf(`#!/bin/sh
set -eu
[ "$#" = 7 ] && [ "$1" = %[1]s ] && [ "$2" = %[2]s ] && [ "$3" = %[3]s ] && [ -z "$4" ] && [ -z "$5" ] && [ "$6" = false ] && [ -z "$7" ] || exit 1
ethx=''
for path in /sys/class/net/*; do
  [ -f "$path/address" ] || continue
  value=$(cat "$path/address")
  if [ "$value" = "$1" ]; then [ -z "$ethx" ] || exit 1; ethx=${path##*/}; fi
done
[ -n "$ethx" ] || exit 1
ip link set lo up
ip link set "$ethx" up
ip -4 addr add "$2" dev "$ethx"
ip -4 route add default via "$3" dev "$ethx" onlink
cat /configs/resolv-before.conf >/etc/resolv.conf
wget -q -T 15 -O /dev/null http://deb.debian.org/debian/dists/trixie/InRelease || exit 1
mkdir -p "/dev/netconf/$ethx"
write() { printf '%%s' "$2" >"/dev/netconf/$ethx/$1"; }
write is_in_china 0
write dhcpv4 0
write dhcpv6_or_slaac 0
write should_disable_dhcpv4 1
write should_disable_accept_ra 0
write should_disable_autoconf 0
write should_add_ipv4_static_dns 1
write should_add_ipv6_static_dns 0
write ipv4_has_internet 1
write ipv6_has_internet 0
write ethx "$ethx"
write mac_addr "$1"
write ipv4_addr "$2"
write ipv4_gateway "$3"
write ipv6_addr ''
write ipv6_gateway ''
write ipv6_extra_addrs ''
`, spec.Network.MAC, spec.Network.Address, spec.Network.Gateway)
	return InstallerScripts{Installer: []byte(installer), Preseed: []byte(preseed), Network: []byte(network), FirstBoot: []byte(firstBoot.String()), Link: link, Interfaces: interfaces, Resolver: resolver}, nil
}
