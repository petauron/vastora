// Package osreinstall contains the bounded host inspection used before a
// destructive operating-system reinstall. Inspection never changes the host.
package osreinstall

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"sort"
	"strings"
)

var (
	devicePattern          = regexp.MustCompile(`^/dev/(vd[a-z]+|sd[a-z]+|xvd[a-z]+|nvme[0-9]+n[0-9]+)$`)
	ifacePattern           = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,32}$`)
	deviceIDPattern        = regexp.MustCompile(`^[0-9]{1,6}:[0-9]{1,6}$`)
	bootIDPattern          = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	hostFingerprintPattern = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	partitionIDPattern     = regexp.MustCompile(`^([0-9a-f]{8}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)
)

// Inspection is evidence for an operator review, not permission to erase a
// disk. A later operation must compare fresh evidence before making changes.
type Inspection struct {
	Virtualization       string   `json:"virtualization"`
	Architecture         string   `json:"architecture"`
	Hostname             string   `json:"hostname,omitempty"`
	BootID               string   `json:"bootId,omitempty"`
	OSID                 string   `json:"osId,omitempty"`
	OSVersion            string   `json:"osVersion,omitempty"`
	BootMode             string   `json:"bootMode,omitempty"`
	NetworkCandidate     string   `json:"networkCandidate,omitempty"`
	SSHHostKeySHA256     string   `json:"sshHostKeySha256,omitempty"`
	Disk                 Disk     `json:"disk"`
	Network              Network  `json:"network"`
	Limitations          []string `json:"limitations,omitempty"`
	MissingPrerequisites []string `json:"missingPrerequisites,omitempty"`
}

type Disk struct {
	Path             string `json:"path"`
	Bytes            uint64 `json:"bytes"`
	MajorMinor       string `json:"majorMinor"`
	PartitionTableID string `json:"partitionTableId,omitempty"`
}

type Network struct {
	Interface  string    `json:"interface"`
	MAC        string    `json:"mac"`
	Addresses  []Address `json:"addresses"`
	Gateways   []Gateway `json:"gateways"`
	DNS        []string  `json:"dns"`
	Additional []Network `json:"additional,omitempty"`
}

type Address struct {
	Prefix  string `json:"prefix"`
	Dynamic bool   `json:"dynamic"`
}

type Gateway struct {
	Family  string `json:"family"`
	Address string `json:"address,omitempty"`
	OnLink  bool   `json:"onLink,omitempty"`
	Dynamic bool   `json:"dynamic,omitempty"`
}

type Evidence struct {
	Virtualization string
	Architecture   string
	BlockDevices   []byte
	Addresses      []byte
	RoutesV4       []byte
	RoutesV6       []byte
	ResolvConf     []byte
}

type blockDevice struct {
	Path             string        `json:"path"`
	Type             string        `json:"type"`
	Size             uint64        `json:"size"`
	MajorMinor       string        `json:"maj:min"`
	PartitionTableID string        `json:"ptuuid"`
	Mountpoints      []string      `json:"mountpoints"`
	Children         []blockDevice `json:"children"`
}

type addrInfo struct {
	Family    string   `json:"family"`
	Local     string   `json:"local"`
	Prefixlen int      `json:"prefixlen"`
	Scope     string   `json:"scope"`
	Dynamic   bool     `json:"dynamic"`
	Flags     []string `json:"flags"`
}

type iface struct {
	Ifname   string     `json:"ifname"`
	Address  string     `json:"address"`
	AddrInfo []addrInfo `json:"addr_info"`
	LinkInfo struct {
		Kind string `json:"info_kind"`
	} `json:"linkinfo"`
}

type route struct {
	Dst      string   `json:"dst"`
	Dev      string   `json:"dev"`
	Gateway  string   `json:"gateway"`
	Protocol string   `json:"protocol"`
	Flags    []string `json:"flags"`
}

// InspectEvidence accepts only one identifiable system disk and one default
// interface. Ambiguous layouts are rejected rather than guessed.
func InspectEvidence(e Evidence) (Inspection, error) {
	var result Inspection
	virt := strings.ToLower(strings.TrimSpace(e.Virtualization))
	if virt != "kvm" && virt != "qemu" {
		return result, errors.New("reinstall: only KVM virtual machines are supported")
	}
	if e.Architecture != "amd64" && e.Architecture != "arm64" {
		return result, errors.New("reinstall: unsupported architecture")
	}
	result.Virtualization, result.Architecture = virt, e.Architecture
	// Active addresses do not prove how DHCP, SLAAC, routes or provider NAT
	// should be recreated. This inspection is not a complete network plan.
	result.Limitations = append(result.Limitations, "network_configuration_requires_review")

	var blocks struct {
		Blockdevices []blockDevice `json:"blockdevices"`
	}
	if len(e.BlockDevices) > 2<<20 || json.Unmarshal(e.BlockDevices, &blocks) != nil || len(blocks.Blockdevices) == 0 {
		return result, errors.New("reinstall: disk inventory is unavailable")
	}
	var root, boot []blockDevice
	for _, d := range blocks.Blockdevices {
		if d.Type != "disk" {
			continue
		}
		if hasMount(d, "/") {
			root = append(root, d)
		}
		if hasMount(d, "/boot") || hasMount(d, "/boot/efi") {
			boot = append(boot, d)
		}
	}
	if len(root) != 1 || len(boot) > 1 || len(boot) == 1 && boot[0].Path != root[0].Path {
		return result, errors.New("reinstall: system disk or boot disk is ambiguous")
	}
	d := root[0]
	if !devicePattern.MatchString(d.Path) || !deviceIDPattern.MatchString(d.MajorMinor) || d.Size < 4<<30 {
		return result, errors.New("reinstall: unsupported system disk")
	}
	result.Disk = Disk{Path: d.Path, Bytes: d.Size, MajorMinor: d.MajorMinor, PartitionTableID: strings.ToLower(d.PartitionTableID)}
	if !partitionIDPattern.MatchString(result.Disk.PartitionTableID) {
		result.Limitations = append(result.Limitations, "disk_identity_requires_review")
	}

	var interfaces []iface
	if len(e.Addresses) > 1<<20 || json.Unmarshal(e.Addresses, &interfaces) != nil {
		return Inspection{}, errors.New("reinstall: interface inventory is unavailable")
	}
	var routes4, routes6 []route
	if len(e.RoutesV4) > 1<<20 || len(e.RoutesV6) > 1<<20 || json.Unmarshal(e.RoutesV4, &routes4) != nil || json.Unmarshal(e.RoutesV6, &routes6) != nil {
		return Inspection{}, errors.New("reinstall: default routes are unavailable")
	}
	if len(routes4) > 2 || len(routes6) > 4 || len(routes4)+len(routes6) == 0 {
		return Inspection{}, errors.New("reinstall: ambiguous or missing default route")
	}
	if len(routes4) > 0 && routes4[0].Dst != "default" || len(routes6) > 0 && routes6[0].Dst != "default" {
		return Inspection{}, errors.New("reinstall: invalid default route")
	}
	name := ""
	for _, routes := range [][]route{routes4, routes6} {
		for _, r := range routes {
			if !ifacePattern.MatchString(r.Dev) || name != "" && name != r.Dev {
				return Inspection{}, errors.New("reinstall: default routes use different interfaces")
			}
			name = r.Dev
		}
	}
	var selected *iface
	for i := range interfaces {
		if interfaces[i].Ifname == name {
			if selected != nil {
				return Inspection{}, errors.New("reinstall: interface identity is ambiguous")
			}
			selected = &interfaces[i]
		}
	}
	if selected == nil || selected.Address == "" || len(selected.Address) > 64 {
		return Inspection{}, errors.New("reinstall: default interface is unavailable")
	}
	for _, item := range interfaces {
		if item.Ifname == "lo" || item.Ifname == "tailscale0" && item.LinkInfo.Kind == "tun" || item.Ifname == "docker0" && item.LinkInfo.Kind == "bridge" {
			continue
		}
		hasGlobal := false
		for _, addr := range item.AddrInfo {
			hasGlobal = hasGlobal || addr.Scope == "global"
		}
		if item.Ifname != name && !hasGlobal {
			continue
		}
		if item.LinkInfo.Kind != "" {
			if !contains(result.Limitations, "additional_network_requires_review") {
				result.Limitations = append(result.Limitations, "additional_network_requires_review")
			}
			continue
		}
		nic, err := inspectNIC(item, routes4, routes6)
		if err != nil {
			return Inspection{}, err
		}
		if item.Ifname == name {
			result.Network = nic
		} else {
			result.Network.Additional = append(result.Network.Additional, nic)
		}
	}
	// Select by the reviewed default interface, with stable ordering for peers.
	// Assignment above must not discard peers that appeared before the default.
	var peers []Network
	for _, item := range interfaces {
		if item.Ifname == name || item.Ifname == "lo" || item.Ifname == "tailscale0" || item.Ifname == "docker0" || item.LinkInfo.Kind != "" {
			continue
		}
		hasGlobal := false
		for _, a := range item.AddrInfo {
			hasGlobal = hasGlobal || a.Scope == "global"
		}
		if hasGlobal {
			nic, e := inspectNIC(item, routes4, routes6)
			if e != nil {
				return Inspection{}, e
			}
			peers = append(peers, nic)
		}
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].Interface < peers[j].Interface })
	result.Network.Additional = peers
	if len(e.ResolvConf) > 65536 {
		return Inspection{}, errors.New("reinstall: DNS configuration is too large")
	}
	for _, line := range strings.Split(string(e.ResolvConf), "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		addr, err := netip.ParseAddr(fields[1])
		if err != nil {
			return Inspection{}, errors.New("reinstall: invalid DNS address")
		}
		result.Network.DNS = append(result.Network.DNS, addr.String())
		if (addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.String() == "100.100.100.100") && !contains(result.Limitations, "local_dns_requires_review") {
			result.Limitations = append(result.Limitations, "local_dns_requires_review")
		}
	}
	if len(result.Network.DNS) == 0 || len(result.Network.DNS) > 8 {
		return Inspection{}, errors.New("reinstall: DNS servers are unavailable")
	}
	sort.Slice(result.Network.Gateways, func(i, j int) bool { return result.Network.Gateways[i].Family < result.Network.Gateways[j].Family })
	sort.Slice(result.Network.Addresses, func(i, j int) bool { return result.Network.Addresses[i].Prefix < result.Network.Addresses[j].Prefix })
	if path.Clean(result.Disk.Path) != result.Disk.Path {
		return Inspection{}, fmt.Errorf("reinstall: invalid disk path")
	}
	if err := result.Validate(); err != nil {
		return Inspection{}, err
	}
	return result, nil
}

func hasMount(d blockDevice, mount string) bool {
	for _, p := range d.Mountpoints {
		if p == mount {
			return true
		}
	}
	for _, c := range d.Children {
		if hasMount(c, mount) {
			return true
		}
	}
	return false
}

// Validate constrains observations received from an Agent before they are
// persisted or used for a destructive operation.
func (i Inspection) Validate() error {
	knownTools := map[string]bool{"bash": true, "curl": true, "grep": true, "gzip": true, "cpio": true, "ip": true, "lsblk": true, "blkid": true, "dpkg-deb": true, "grub-editenv": true, "grub-reboot": true, "ca-certificates": true}
	if len(i.MissingPrerequisites) > len(knownTools) {
		return errors.New("reinstall: invalid prerequisite evidence")
	}
	seenTools := map[string]bool{}
	for _, tool := range i.MissingPrerequisites {
		if !knownTools[tool] || seenTools[tool] {
			return errors.New("reinstall: invalid prerequisite evidence")
		}
		seenTools[tool] = true
	}
	if i.BootID != "" && !bootIDPattern.MatchString(i.BootID) {
		return errors.New("reinstall: invalid boot identity")
	}
	if len(i.OSID) > 32 || len(i.OSVersion) > 32 || strings.ContainsAny(i.OSID+i.OSVersion, " \t\r\n") {
		return errors.New("reinstall: invalid OS identity")
	}
	if i.BootMode != "" && i.BootMode != "bios" && i.BootMode != "efi" {
		return errors.New("reinstall: invalid boot mode evidence")
	}
	if i.NetworkCandidate != "" && i.NetworkCandidate != "netplan_static_ipv4" && i.NetworkCandidate != "ifupdown_static_ipv4" {
		return errors.New("reinstall: invalid network source evidence")
	}
	if i.Hostname != "" && (len(i.Hostname) > 253 || strings.ContainsAny(i.Hostname, "\r\n\t")) {
		return errors.New("reinstall: invalid hostname evidence")
	}
	if i.SSHHostKeySHA256 != "" && !hostFingerprintPattern.MatchString(i.SSHHostKeySHA256) {
		return errors.New("reinstall: invalid SSH host key fingerprint")
	}
	if i.Virtualization != "kvm" && i.Virtualization != "qemu" {
		return errors.New("reinstall: invalid virtualization")
	}
	if i.Architecture != "amd64" && i.Architecture != "arm64" {
		return errors.New("reinstall: invalid architecture")
	}
	if !devicePattern.MatchString(i.Disk.Path) || !deviceIDPattern.MatchString(i.Disk.MajorMinor) || i.Disk.Bytes < 4<<30 {
		return errors.New("reinstall: invalid disk evidence")
	}
	if validateObservedNIC(i.Network, true) != nil || len(i.Network.Additional) > 7 || len(i.Network.DNS) == 0 || len(i.Network.DNS) > 8 {
		return errors.New("reinstall: invalid network evidence")
	}
	seenNIC := map[string]bool{i.Network.Interface: true}
	seenMAC := map[string]bool{i.Network.MAC: true}
	for _, nic := range i.Network.Additional {
		if validateObservedNIC(nic, false) != nil || len(nic.Additional) > 0 || seenNIC[nic.Interface] || seenMAC[nic.MAC] {
			return errors.New("reinstall: invalid additional interface")
		}
		seenNIC[nic.Interface] = true
		seenMAC[nic.MAC] = true
	}
	for _, d := range i.Network.DNS {
		addr, err := netip.ParseAddr(d)
		if err != nil || addr.String() != d {
			return errors.New("reinstall: invalid DNS evidence")
		}
	}
	seen := map[string]bool{}
	for _, reason := range i.Limitations {
		if (reason != "disk_identity_requires_review" && reason != "local_dns_requires_review" && reason != "additional_network_requires_review" && reason != "network_configuration_requires_review") || seen[reason] {
			return errors.New("reinstall: invalid limitation")
		}
		seen[reason] = true
	}
	if !seen["network_configuration_requires_review"] {
		return errors.New("reinstall: network configuration was not inspected")
	}
	if !partitionIDPattern.MatchString(i.Disk.PartitionTableID) && !seen["disk_identity_requires_review"] {
		return errors.New("reinstall: missing disk identity limitation")
	}
	for _, d := range i.Network.DNS {
		addr, _ := netip.ParseAddr(d)
		if (addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.String() == "100.100.100.100") && !seen["local_dns_requires_review"] {
			return errors.New("reinstall: missing DNS limitation")
		}
	}
	return nil
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

// ErrorCode exposes only fixed, non-sensitive reasons to the controller.
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	switch err.Error() {
	case "reinstall: only KVM virtual machines are supported", "reinstall: KVM detection failed":
		return "unsupported_virtualization"
	case "reinstall: unsupported architecture":
		return "unsupported_architecture"
	case "reinstall: system disk or boot disk is ambiguous", "reinstall: unsupported system disk", "reinstall: disk inventory is unavailable":
		return "disk_unavailable"
	case "reinstall: ambiguous or missing default route", "reinstall: default routes use different interfaces", "reinstall: default routes are unavailable", "reinstall: invalid default route", "reinstall: interface inventory is unavailable", "reinstall: interface identity is ambiguous", "reinstall: default interface is unavailable", "reinstall: default interface MAC is invalid", "reinstall: unsupported interface address", "reinstall: interface addresses are unavailable", "reinstall: invalid gateway":
		return "network_unavailable"
	case "reinstall: DNS configuration is unavailable", "reinstall: DNS configuration is too large", "reinstall: invalid DNS address", "reinstall: DNS servers are unavailable":
		return "dns_unavailable"
	default:
		return "inspection_unavailable"
	}
}

func ValidErrorCode(code string) bool {
	switch code {
	case "unsupported_virtualization", "unsupported_architecture", "disk_unavailable", "network_unavailable", "dns_unavailable", "inspection_unavailable":
		return true
	default:
		return false
	}
}
