// Package networking contains the provider-neutral node network model shared
// by Center and Agent. It deliberately describes addresses and reachability;
// VPN, DNS, gateway, and tunnel implementations stay in their own components.
package networking

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const (
	KindLAN       = "lan"
	KindHeadscale = "headscale"
	KindPublic    = "public"
)

type Candidate struct {
	Address    string    `json:"address"`
	Interface  string    `json:"interface"`
	Family     string    `json:"family"`
	Kind       string    `json:"kind"`
	ObservedAt time.Time `json:"observedAt"`
}

type Profile struct {
	ServiceAddress    string    `json:"serviceAddress"`
	LANAddress        string    `json:"lanAddress,omitempty"`
	HeadscaleAddress  string    `json:"headscaleAddress,omitempty"`
	PublicAddress     string    `json:"publicAddress,omitempty"`
	EnabledKinds      []string  `json:"enabledKinds"`
	DirectPublic      bool      `json:"directPublic"`
	ConfirmedAt       time.Time `json:"confirmedAt"`
	CandidateObserved time.Time `json:"candidateObservedAt"`
}

// Discover enumerates addresses assigned to local interfaces. It never calls
// an external "what is my IP" service, because a NAT egress address does not
// prove that the node can accept inbound traffic on that address.
func Discover(now time.Time) ([]Candidate, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	result := make([]Candidate, 0)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
				continue
			}
			kind := Classify(iface.Name, ip)
			if kind == "" {
				continue
			}
			family := "ipv6"
			if ip.To4() != nil {
				family = "ipv4"
			}
			result = append(result, Candidate{Address: ip.String(), Interface: iface.Name, Family: family, Kind: kind, ObservedAt: now.UTC()})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		if result[i].Interface != result[j].Interface {
			return result[i].Interface < result[j].Interface
		}
		return result[i].Address < result[j].Address
	})
	return result, nil
}

// DefaultRouteAddress returns the local address selected by the kernel for
// ordinary outbound traffic in the same address family as remoteAddress. A UDP
// connect performs route selection without sending a packet.
func DefaultRouteAddress(remoteAddress string) (string, error) {
	remote, err := netip.ParseAddr(strings.TrimSpace(remoteAddress))
	if err != nil {
		return "", errors.New("network: invalid remote address")
	}
	network, target := "udp6", "[2606:4700:4700::1111]:53"
	if remote.Unmap().Is4() {
		network, target = "udp4", "1.1.1.1:53"
	}
	connection, err := net.Dial(network, target)
	if err != nil {
		return "", fmt.Errorf("network: select default route: %w", err)
	}
	defer connection.Close()
	address, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok || address.IP == nil {
		return "", errors.New("network: default route did not provide a local address")
	}
	return address.IP.String(), nil
}

func Classify(interfaceName string, ip net.IP) string {
	if ip == nil || !ip.IsGlobalUnicast() {
		return ""
	}
	name := strings.ToLower(strings.TrimSpace(interfaceName))
	if strings.Contains(name, "tailscale") {
		return KindHeadscale
	}
	if IsVirtualInterface(name) {
		return ""
	}
	if ip.IsPrivate() || inCGNAT(ip) {
		return KindLAN
	}
	return KindPublic
}

// IsVirtualInterface reports interfaces that represent a container, VM, or
// separate overlay network rather than a LAN that Vastora should offer as a
// service address. Tailscale is handled before this check because it is a
// first-class network kind in Vastora.
func IsVirtualInterface(interfaceName string) bool {
	name := strings.ToLower(strings.TrimSpace(interfaceName))
	if name == "" {
		return true
	}
	prefixes := []string{
		"docker", "br-", "veth", "cni", "flannel", "cali", "kube-ipvs",
		"podman", "lxcbr", "virbr", "wg", "tun", "tap", "utun", "zt", "nebula",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func ValidateProfile(candidates []Candidate, profile Profile) error {
	serviceIP := net.ParseIP(strings.TrimSpace(profile.ServiceAddress))
	if serviceIP == nil {
		return errors.New("network: a valid private service address is required")
	}
	byAddress := make(map[string]Candidate, len(candidates))
	for _, candidate := range candidates {
		byAddress[candidate.Address] = candidate
	}
	selected, exists := byAddress[serviceIP.String()]
	serviceIsLoopback := serviceIP.IsLoopback()
	if !serviceIsLoopback && (!exists || selected.Kind == KindPublic) {
		return errors.New("network: service address must be loopback or a discovered LAN or Headscale address")
	}
	kinds := map[string]bool{}
	for _, kind := range profile.EnabledKinds {
		if kind != KindLAN && kind != KindHeadscale && kind != KindPublic {
			return errors.New("network: invalid enabled network kind")
		}
		kinds[kind] = true
	}
	if !serviceIsLoopback && !kinds[selected.Kind] {
		return errors.New("network: service address network must be enabled")
	}
	if err := validateSelectedAddress(byAddress, strings.TrimSpace(profile.LANAddress), KindLAN, kinds[KindLAN]); err != nil {
		return err
	}
	if err := validateSelectedAddress(byAddress, strings.TrimSpace(profile.HeadscaleAddress), KindHeadscale, kinds[KindHeadscale]); err != nil {
		return err
	}
	if profile.DirectPublic {
		if !kinds[KindPublic] {
			return errors.New("network: direct public ingress requires the public network")
		}
		publicIP := net.ParseIP(strings.TrimSpace(profile.PublicAddress))
		if publicIP == nil {
			return errors.New("network: direct public ingress requires a discovered local public address")
		}
		publicCandidate, exists := byAddress[publicIP.String()]
		if !exists || publicCandidate.Kind != KindPublic {
			return errors.New("network: direct public ingress requires a discovered local public address")
		}
	} else if strings.TrimSpace(profile.PublicAddress) != "" {
		return errors.New("network: public address requires direct public ingress")
	}
	return nil
}

func validateSelectedAddress(candidates map[string]Candidate, value, kind string, enabled bool) error {
	if !enabled {
		if value != "" {
			return errors.New("network: disabled network kind cannot have a selected address")
		}
		return nil
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return errors.New("network: enabled network kind requires a matching discovered address")
	}
	candidate, exists := candidates[ip.String()]
	if !exists || candidate.Kind != kind {
		return errors.New("network: enabled network kind requires a matching discovered address")
	}
	return nil
}

func inCGNAT(ip net.IP) bool {
	v4 := ip.To4()
	return v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 0x40
}
