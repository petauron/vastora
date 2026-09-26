package agent

import (
	"net"
	"sort"
	"strings"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func discoverLandingEgressAddresses() ([]landing.EgressAddress, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	return landingEgressAddresses(interfaces, func(iface net.Interface) ([]net.Addr, error) { return iface.Addrs() })
}

func landingEgressAddresses(interfaces []net.Interface, addresses func(net.Interface) ([]net.Addr, error)) ([]landing.EgressAddress, error) {
	result := []landing.EgressAddress{}
	seen := map[string]bool{}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || networking.IsVirtualInterface(iface.Name) || strings.Contains(strings.ToLower(iface.Name), "tailscale") {
			continue
		}
		assigned, err := addresses(iface)
		if err != nil {
			return nil, err
		}
		for _, address := range assigned {
			ip, _, err := net.ParseCIDR(address.String())
			if err != nil || !landing.ValidEgressIP(ip.String()) || seen[ip.String()] {
				continue
			}
			seen[ip.String()] = true
			result = append(result, landing.EgressAddress{Address: ip.String(), Interface: iface.Name})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		iv6, jv6 := strings.Contains(result[i].Address, ":"), strings.Contains(result[j].Address, ":")
		if iv6 != jv6 {
			return !iv6
		}
		if result[i].Interface != result[j].Interface {
			return result[i].Interface < result[j].Interface
		}
		return result[i].Address < result[j].Address
	})
	if err := landing.ValidateEgressAddresses(result); err != nil {
		return nil, err
	}
	return result, nil
}
