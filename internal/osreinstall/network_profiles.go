package osreinstall

import (
	"bytes"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
)

func networkError() error { return errors.New("reinstall: network configuration requires review") }
func networkNICs(n Network) []Network {
	return append([]Network{{Interface: n.Interface, MAC: n.MAC, Addresses: n.Addresses, Gateways: n.Gateways}}, n.Additional...)
}
func planNICs(p StaticNetworkPlan) []StaticNetworkPlan {
	return append([]StaticNetworkPlan{p}, p.Additional...)
}
func resolverSettings(data []byte) (dns, search []string, err error) {
	if len(data) == 0 || len(data) > 64<<10 {
		return nil, nil, networkError()
	}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(strings.SplitN(strings.SplitN(line, "#", 2)[0], ";", 2)[0])
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "nameserver":
			if len(f) != 2 {
				return nil, nil, networkError()
			}
			ip, e := netip.ParseAddr(f[1])
			if e != nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.String() == "100.100.100.100" || ip.String() == "fd7a:115c:a1e0::53" || slices.Contains(dns, ip.String()) {
				return nil, nil, networkError()
			}
			dns = append(dns, ip.String())
		case "search":
			if search != nil || len(f) < 2 {
				return nil, nil, networkError()
			}
			for _, v := range f[1:] {
				if !validSearchDomain(v) {
					return nil, nil, networkError()
				}
			}
			search = append(search, f[1:]...)
		default:
			return nil, nil, networkError()
		}
	}
	if len(dns) == 0 || len(dns) > 8 || len(search) > 6 {
		return nil, nil, networkError()
	}
	return dns, search, nil
}
func validateNIC(p StaticNetworkPlan, primary bool) error {
	mac, e := net.ParseMAC(p.MAC)
	if e != nil || len(mac) != 6 || mac[0]&1 != 0 || mac.String() != p.MAC || p.MAC == "00:00:00:00:00:00" || !ifacePattern.MatchString(p.Interface) || strings.ContainsAny(p.Interface, ".:/") || p.Interface == "lo" || p.MTU != 0 && (p.MTU < 1280 || p.MTU > 9000) || p.IPv6AcceptRA != "" && p.IPv6AcceptRA != "0" && p.IPv6AcceptRA != "1" && p.IPv6AcceptRA != "2" || len(p.IPv6) > 16 {
		return networkError()
	}
	if p.Address != "" {
		ip, e := netip.ParsePrefix(p.Address)
		if e != nil || !ip.Addr().Is4() || !ip.Addr().IsGlobalUnicast() || ip.String() != p.Address {
			return networkError()
		}
	}
	if primary && (p.Address == "" || p.Gateway == "") {
		return networkError()
	} // Installer transport is still IPv4; IPv6 is restored on the target.
	if p.Gateway != "" {
		gw, e := netip.ParseAddr(p.Gateway)
		ip, _ := netip.ParsePrefix(p.Address)
		if e != nil || !gw.Is4() || !gw.IsGlobalUnicast() || !ip.IsValid() || !ip.Masked().Contains(gw) && !p.GatewayOnLink {
			return networkError()
		}
	}
	seenRoutes := map[string]bool{}
	for _, v := range p.DirectRoutes {
		prefix, e := netip.ParsePrefix(v)
		if e != nil || prefix.Bits() == 0 || !prefix.Addr().IsGlobalUnicast() || prefix.Masked().String() != v || seenRoutes[v] {
			return networkError()
		}
		seenRoutes[v] = true
	}
	if len(p.DirectRoutes) > 16 {
		return networkError()
	}
	seen := map[string]bool{}
	for _, v := range p.IPv6 {
		ip, e := netip.ParsePrefix(v)
		if e != nil || !ip.Addr().Is6() || !ip.Addr().IsGlobalUnicast() || ip.String() != v || seen[v] {
			return networkError()
		}
		seen[v] = true
	}
	if p.Gateway6 != "" {
		g, e := netip.ParseAddr(p.Gateway6)
		if e != nil || !g.Is6() || !g.IsGlobalUnicast() && !g.IsLinkLocalUnicast() || len(p.IPv6) == 0 {
			return networkError()
		}
		reachable := g.IsLinkLocalUnicast()
		for _, v := range p.IPv6 {
			ip, _ := netip.ParsePrefix(v)
			reachable = reachable || ip.Masked().Contains(g)
		}
		if !reachable && !p.Gateway6OnLink {
			return networkError()
		}
	}
	if p.IPv6Autoconf != "" && p.IPv6Autoconf != "0" && p.IPv6Autoconf != "1" {
		return networkError()
	}
	if p.DHCP6 && p.IPv6AcceptRA == "0" {
		return networkError()
	}
	return nil
}
func sameNetworkPlan(a, b StaticNetworkPlan) bool {
	// A dynamically assigned lease is evidence, never a static configuration.
	normalize := func(p StaticNetworkPlan) StaticNetworkPlan {
		p.Additional = append([]StaticNetworkPlan(nil), p.Additional...)
		if p.DHCP4 {
			p.Address = ""
			p.Gateway = ""
		}
		if p.DynamicDNS {
			p.DNS = nil
			p.Search = nil
			p.Resolver = nil
		}
		for i := range p.Additional {
			if p.Additional[i].DHCP4 {
				p.Additional[i].Address = ""
				p.Additional[i].Gateway = ""
			}
		}
		return p
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}
func (p StaticNetworkPlan) RenderStaticNetwork() (link, interfaces, resolver []byte, err error) {
	bad := func() ([]byte, []byte, []byte, error) { return nil, nil, nil, networkError() }
	dns, search, e := resolverSettings(p.Resolver)
	if e != nil || !slices.Equal(dns, p.DNS) || !slices.Equal(search, p.Search) || len(p.Additional) > 7 {
		return bad()
	}
	var links, config strings.Builder
	config.WriteString("source /etc/network/interfaces.d/*\n\nauto lo\niface lo inet loopback\n")
	seenNames, seenMAC := map[string]bool{}, map[string]bool{}
	for index, n := range planNICs(p) {
		if validateNIC(n, index == 0) != nil || index > 0 && (len(n.Additional) > 0 || len(n.DNS) > 0 || len(n.Resolver) > 0) || seenNames[n.Interface] || seenMAC[n.MAC] {
			return bad()
		}
		seenNames[n.Interface] = true
		seenMAC[n.MAC] = true
		// One bounded udev rules file binds every physical interface by MAC.
		fmt.Fprintf(&links, "SUBSYSTEM==\"net\", ACTION==\"add\", ATTR{address}==\"%s\", NAME=\"%s\"\n", n.MAC, n.Interface)
		fmt.Fprintf(&config, "\n# mac %s\nauto %s\n", n.MAC, n.Interface)
		method := "static"
		if n.DHCP4 {
			method = "dhcp"
		} else if n.Address == "" {
			method = "manual"
		}
		fmt.Fprintf(&config, "iface %s inet %s\n", n.Interface, method)
		if n.MTU != 0 {
			fmt.Fprintf(&config, "    mtu %d\n", n.MTU)
		}
		ra := n.IPv6AcceptRA
		if ra == "" {
			ra = "0"
		}
		fmt.Fprintf(&config, "    pre-up /sbin/sysctl -q -w net.ipv6.conf.%s.accept_ra=%s\n", n.Interface, ra)
		if !n.DHCP4 && n.Address != "" {
			fmt.Fprintf(&config, "    address %s\n", n.Address)
			if n.Gateway != "" {
				if n.GatewayOnLink {
					fmt.Fprintf(&config, "    up /sbin/ip -4 route replace default via %s dev %s onlink\n", n.Gateway, n.Interface)
				} else {
					fmt.Fprintf(&config, "    gateway %s\n", n.Gateway)
				}
			}
		}
		for _, destination := range n.DirectRoutes {
			prefix, _ := netip.ParsePrefix(destination)
			family := "-6"
			if prefix.Addr().Is4() {
				family = "-4"
			}
			fmt.Fprintf(&config, "    up /sbin/ip %s route replace %s dev %s\n", family, destination, n.Interface)
		}
		if index == 0 && !p.DynamicDNS {
			fmt.Fprintf(&config, "    dns-nameservers %s\n", strings.Join(p.DNS, " "))
			if len(p.Search) > 0 {
				fmt.Fprintf(&config, "    dns-search %s\n", strings.Join(p.Search, " "))
			}
		}
		for j, v := range n.IPv6 {
			fmt.Fprintf(&config, "iface %s inet6 static\n    address %s\n", n.Interface, v)
			if j == 0 && n.Gateway6 != "" {
				if n.Gateway6OnLink {
					fmt.Fprintf(&config, "    up /sbin/ip -6 route replace default via %s dev %s onlink\n", n.Gateway6, n.Interface)
				} else {
					fmt.Fprintf(&config, "    gateway %s\n", n.Gateway6)
				}
			}
		}
		if ra != "0" {
			fmt.Fprintf(&config, "iface %s inet6 auto\n    dhcp %d\n", n.Interface, map[bool]int{true: 1, false: 0}[n.DHCP6])
		}
		if n.IPv6Autoconf != "" {
			fmt.Fprintf(&config, "    post-up /sbin/sysctl -q -w net.ipv6.conf.%s.autoconf=%s\n", n.Interface, n.IPv6Autoconf)
		}
	}
	return []byte(links.String()), []byte(config.String()), bytes.Clone(p.Resolver), nil
}

type netplanNIC struct {
	DHCP4     bool     `yaml:"dhcp4"`
	DHCP6     bool     `yaml:"dhcp6"`
	AcceptRA  *bool    `yaml:"accept-ra"`
	Addresses []string `yaml:"addresses"`
	Gateway4  string   `yaml:"gateway4"`
	Gateway6  string   `yaml:"gateway6"`
	MTU       int      `yaml:"mtu"`
	Optional  bool     `yaml:"optional"`
	Match     struct {
		MAC string `yaml:"macaddress"`
	} `yaml:"match"`
	SetName     string `yaml:"set-name"`
	Nameservers struct {
		Addresses []string `yaml:"addresses"`
		Search    []string `yaml:"search"`
	} `yaml:"nameservers"`
	Routes []struct {
		To     string `yaml:"to"`
		Via    string `yaml:"via"`
		OnLink bool   `yaml:"on-link"`
	} `yaml:"routes"`
}

func ParseStaticNetplan(config, resolver []byte, observed Network) (StaticNetworkPlan, error) {
	var d struct {
		Network struct {
			Version   int                   `yaml:"version"`
			Renderer  string                `yaml:"renderer"`
			Ethernets map[string]netplanNIC `yaml:"ethernets"`
		} `yaml:"network"`
	}
	if len(config) == 0 || len(config) > 64<<10 {
		return StaticNetworkPlan{}, networkError()
	}
	dec := yaml.NewDecoder(bytes.NewReader(config))
	dec.KnownFields(true)
	if dec.Decode(&d) != nil {
		return StaticNetworkPlan{}, networkError()
	}
	var more any
	if dec.Decode(&more) != io.EOF || d.Network.Version != 2 || d.Network.Renderer != "" && d.Network.Renderer != "networkd" || len(d.Network.Ethernets) != len(networkNICs(observed)) {
		return StaticNetworkPlan{}, networkError()
	}
	var plans []StaticNetworkPlan
	for _, seen := range networkNICs(observed) {
		var found *netplanNIC
		for name, c := range d.Network.Ethernets {
			if c.SetName == seen.Interface || name == seen.Interface && c.SetName == "" {
				if found != nil {
					return StaticNetworkPlan{}, networkError()
				}
				v := c
				found = &v
			}
		}
		if found == nil {
			return StaticNetworkPlan{}, networkError()
		}
		c := *found
		if c.Match.MAC != "" && strings.ToLower(c.Match.MAC) != seen.MAC {
			return StaticNetworkPlan{}, networkError()
		}
		p := StaticNetworkPlan{Interface: seen.Interface, MAC: seen.MAC, Gateway: c.Gateway4, Gateway6: c.Gateway6, DHCP4: c.DHCP4, DHCP6: c.DHCP6, MTU: c.MTU, IPv6AcceptRA: ""}
		if c.AcceptRA != nil {
			p.IPv6AcceptRA = "0"
			if *c.AcceptRA {
				p.IPv6AcceptRA = "1"
			}
		}
		if c.DHCP6 {
			if c.AcceptRA != nil && !*c.AcceptRA {
				return StaticNetworkPlan{}, networkError()
			}
			p.IPv6AcceptRA = "1"
		}
		for _, v := range c.Addresses {
			ip, e := netip.ParsePrefix(v)
			if e != nil {
				return StaticNetworkPlan{}, networkError()
			}
			if ip.Addr().Is4() {
				if p.Address != "" || p.DHCP4 {
					return StaticNetworkPlan{}, networkError()
				}
				p.Address = ip.String()
			} else {
				p.IPv6 = append(p.IPv6, ip.String())
			}
		}
		for _, r := range c.Routes {
			if r.Via == "" {
				prefix, e := netip.ParsePrefix(r.To)
				if e != nil || prefix.Bits() == 0 || r.OnLink {
					return StaticNetworkPlan{}, networkError()
				}
				p.DirectRoutes = append(p.DirectRoutes, prefix.Masked().String())
				continue
			}
			gw, e := netip.ParseAddr(r.Via)
			if e != nil || r.To != "default" && r.To != "0.0.0.0/0" && r.To != "::/0" {
				return StaticNetworkPlan{}, networkError()
			}
			if gw.Is4() {
				if p.Gateway != "" || r.To == "::/0" {
					return StaticNetworkPlan{}, networkError()
				}
				p.Gateway = r.Via
				p.GatewayOnLink = r.OnLink
			} else {
				if p.Gateway6 != "" || r.To == "0.0.0.0/0" {
					return StaticNetworkPlan{}, networkError()
				}
				p.Gateway6 = r.Via
				p.Gateway6OnLink = r.OnLink
			}
		}
		if p.DHCP4 {
			for _, a := range seen.Addresses {
				ip, _ := netip.ParsePrefix(a.Prefix)
				if ip.Addr().Is4() && a.Dynamic {
					p.Address = a.Prefix
				}
			}
			for _, g := range seen.Gateways {
				if g.Family == "ipv4" {
					p.Gateway = g.Address
				}
			}
		}
		if len(c.Nameservers.Addresses) > 0 && !slices.Equal(c.Nameservers.Addresses, observed.DNS) {
			return StaticNetworkPlan{}, networkError()
		}
		if len(c.Nameservers.Search) > 0 {
			_, search, e := resolverSettings(resolver)
			if e != nil || !slices.Equal(c.Nameservers.Search, search) {
				return StaticNetworkPlan{}, networkError()
			}
		}
		p.DynamicDNS = p.DHCP4 && len(c.Nameservers.Addresses) == 0
		plans = append(plans, p)
	}
	return finishNetworkPlan(plans, resolver, observed)
}
func finishNetworkPlan(plans []StaticNetworkPlan, resolver []byte, observed Network) (StaticNetworkPlan, error) {
	if len(plans) == 0 {
		return StaticNetworkPlan{}, networkError()
	}
	p := plans[0]
	p.Additional = plans[1:]
	dns, search, e := resolverSettings(resolver)
	if e != nil || !slices.Equal(dns, observed.DNS) {
		return StaticNetworkPlan{}, networkError()
	}
	p.DNS = dns
	p.Search = search
	p.Resolver = bytes.Clone(resolver)
	if _, _, _, e = p.RenderStaticNetwork(); e != nil {
		return StaticNetworkPlan{}, e
	}
	if matchPlanObservation(p, observed) != nil {
		return StaticNetworkPlan{}, networkError()
	}
	return p, nil
}
func matchPlanObservation(plan StaticNetworkPlan, observed Network) error {
	plans, seen := planNICs(plan), networkNICs(observed)
	if len(plans) != len(seen) {
		return networkError()
	}
	for i, p := range plans {
		n := seen[i]
		if p.Interface != n.Interface || p.MAC != n.MAC {
			return networkError()
		}
		var static []string
		v4 := 0
		for _, a := range n.Addresses {
			ip, e := netip.ParsePrefix(a.Prefix)
			if e != nil {
				return networkError()
			}
			if a.Dynamic {
				if ip.Addr().Is4() && !p.DHCP4 || ip.Addr().Is6() && p.IPv6AcceptRA == "0" && !p.DHCP6 {
					return networkError()
				}
				continue
			}
			if ip.Addr().Is4() {
				v4++
				if p.DHCP4 || a.Prefix != p.Address {
					return networkError()
				}
			} else {
				static = append(static, a.Prefix)
			}
		}
		if !p.DHCP4 && (p.Address != "") != (v4 == 1) {
			return networkError()
		}
		sort.Strings(static)
		expected := slices.Clone(p.IPv6)
		sort.Strings(expected)
		if !slices.Equal(static, expected) {
			return networkError()
		}
		for _, g := range n.Gateways {
			if g.Family == "ipv4" {
				if !p.DHCP4 && g.Address != p.Gateway || g.OnLink != p.GatewayOnLink && !p.DHCP4 {
					return networkError()
				}
			} else {
				if g.Dynamic {
					if p.IPv6AcceptRA == "0" {
						return networkError()
					}
				} else if g.Address != p.Gateway6 || g.OnLink != p.Gateway6OnLink {
					return networkError()
				}
			}
		}
		if p.Gateway != "" && !p.DHCP4 && !slices.ContainsFunc(n.Gateways, func(g Gateway) bool { return g.Family == "ipv4" && g.Address == p.Gateway }) {
			return networkError()
		}
		if p.Gateway6 != "" && !slices.ContainsFunc(n.Gateways, func(g Gateway) bool { return g.Family == "ipv6" && !g.Dynamic && g.Address == p.Gateway6 }) {
			return networkError()
		}
	}
	return nil
}

// Only declarative stanzas and exact generated onlink/RA commands are accepted.
// Arbitrary provider hooks are never copied or executed on the replacement OS.
func ParseStaticInterfaces(config, resolver []byte, observed Network) (StaticNetworkPlan, error) {
	if len(config) == 0 || len(config) > 64<<10 {
		return StaticNetworkPlan{}, networkError()
	}
	plans := make([]StaticNetworkPlan, len(networkNICs(observed)))
	indices := map[string]int{}
	for i, n := range networkNICs(observed) {
		plans[i] = StaticNetworkPlan{Interface: n.Interface, MAC: n.MAC, IPv6AcceptRA: ""}
		indices[n.Interface] = i
	}
	hasExplicitDNS := false
	active := -1
	family, method := "", ""
	seenStanza := map[string]bool{}
	auto := map[string]bool{}
	for _, line := range strings.Split(string(config), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 2 && (f[0] == "source" && f[1] == "/etc/network/interfaces.d/*" || f[0] == "source-directory" && f[1] == "/etc/network/interfaces.d") {
			continue
		}
		if (f[0] == "auto" || f[0] == "allow-hotplug") && len(f) >= 2 {
			for _, n := range f[1:] {
				if n != "lo" {
					if _, ok := indices[n]; !ok {
						return StaticNetworkPlan{}, networkError()
					}
					auto[n] = true
				}
			}
			continue
		}
		if f[0] == "iface" && len(f) == 4 {
			if f[1] == "lo" && f[2] == "inet" && f[3] == "loopback" {
				active = -1
				continue
			}
			idx, ok := indices[f[1]]
			if !ok || f[2] != "inet" && f[2] != "inet6" || f[3] != "static" && f[3] != "dhcp" && f[3] != "auto" && f[3] != "manual" {
				return StaticNetworkPlan{}, networkError()
			}
			active = idx
			family = f[2]
			method = f[3]
			key := f[1] + ":" + family + ":" + method
			if seenStanza[key] && !(family == "inet6" && method == "static") {
				return StaticNetworkPlan{}, networkError()
			}
			seenStanza[key] = true
			if family == "inet" && method == "dhcp" {
				plans[idx].DHCP4 = true
			}
			if family == "inet6" && (method == "auto" || method == "dhcp") {
				if plans[idx].IPv6AcceptRA == "" {
					plans[idx].IPv6AcceptRA = "1"
				}
				plans[idx].DHCP6 = method == "dhcp"
			}
			continue
		}
		if active < 0 {
			return StaticNetworkPlan{}, networkError()
		}
		p := &plans[active]
		switch f[0] {
		case "address":
			if len(f) != 2 || method != "static" {
				return StaticNetworkPlan{}, networkError()
			}
			ip, e := netip.ParsePrefix(f[1])
			if e != nil || ip.Addr().Is4() != (family == "inet") {
				return StaticNetworkPlan{}, networkError()
			}
			if family == "inet" {
				if p.Address != "" {
					return StaticNetworkPlan{}, networkError()
				}
				p.Address = ip.String()
			} else {
				p.IPv6 = append(p.IPv6, ip.String())
			}
		case "gateway":
			if len(f) != 2 {
				return StaticNetworkPlan{}, networkError()
			}
			if family == "inet" {
				if p.Gateway != "" {
					return StaticNetworkPlan{}, networkError()
				}
				p.Gateway = f[1]
			} else {
				if p.Gateway6 != "" {
					return StaticNetworkPlan{}, networkError()
				}
				p.Gateway6 = f[1]
			}
		case "mtu":
			if len(f) != 2 {
				return StaticNetworkPlan{}, networkError()
			}
			v, e := strconv.Atoi(f[1])
			if e != nil {
				return StaticNetworkPlan{}, networkError()
			}
			p.MTU = v
		case "dns-nameservers", "dns-search":
			if f[0] == "dns-nameservers" {
				hasExplicitDNS = true
			} // Resolver contents are compared and restored independently below.
			if len(f) < 2 {
				return StaticNetworkPlan{}, networkError()
			}
			if f[0] == "dns-nameservers" && !slices.Equal(f[1:], observed.DNS) {
				return StaticNetworkPlan{}, networkError()
			}
			if f[0] == "dns-search" {
				_, search, e := resolverSettings(resolver)
				if e != nil || !slices.Equal(f[1:], search) {
					return StaticNetworkPlan{}, networkError()
				}
			}
		case "dhcp":
			if family != "inet6" || method != "auto" || len(f) != 2 || f[1] != "0" && f[1] != "1" {
				return StaticNetworkPlan{}, networkError()
			}
			p.DHCP6 = f[1] == "1"
		case "post-up":
			if len(f) != 5 || f[1] != "/sbin/sysctl" || f[2] != "-q" || f[3] != "-w" {
				return StaticNetworkPlan{}, networkError()
			}
			prefix := "net.ipv6.conf." + p.Interface + ".autoconf="
			if !strings.HasPrefix(f[4], prefix) {
				return StaticNetworkPlan{}, networkError()
			}
			p.IPv6Autoconf = strings.TrimPrefix(f[4], prefix)
		case "pre-up":
			if len(f) != 5 || f[1] != "/sbin/sysctl" || f[2] != "-q" || f[3] != "-w" {
				return StaticNetworkPlan{}, networkError()
			}
			prefix := "net.ipv6.conf." + p.Interface + ".accept_ra="
			if !strings.HasPrefix(f[4], prefix) {
				return StaticNetworkPlan{}, networkError()
			}
			p.IPv6AcceptRA = strings.TrimPrefix(f[4], prefix)
		case "up":
			if len(f) == 8 && f[1] == "/sbin/ip" && (f[2] == "-4" || f[2] == "-6") && f[3] == "route" && f[4] == "replace" && f[6] == "dev" && f[7] == p.Interface {
				prefix, e := netip.ParsePrefix(f[5])
				if e != nil || prefix.Bits() == 0 || prefix.Addr().Is4() != (f[2] == "-4") {
					return StaticNetworkPlan{}, networkError()
				}
				p.DirectRoutes = append(p.DirectRoutes, prefix.Masked().String())
				continue
			}
			if len(f) != 11 || f[1] != "/sbin/ip" || f[2] != "-4" && f[2] != "-6" || f[3] != "route" || f[4] != "replace" || f[5] != "default" || f[6] != "via" || f[8] != "dev" || f[9] != p.Interface || f[10] != "onlink" {
				return StaticNetworkPlan{}, networkError()
			}
			if f[2] == "-4" {
				if p.Gateway != "" {
					return StaticNetworkPlan{}, networkError()
				}
				p.Gateway = f[7]
				p.GatewayOnLink = true
			} else {
				if p.Gateway6 != "" {
					return StaticNetworkPlan{}, networkError()
				}
				p.Gateway6 = f[7]
				p.Gateway6OnLink = true
			}
		default:
			return StaticNetworkPlan{}, networkError()
		}
	}
	for i, p := range plans {
		if !auto[p.Interface] {
			return StaticNetworkPlan{}, networkError()
		}
		if p.DHCP4 {
			for _, a := range networkNICs(observed)[i].Addresses {
				ip, _ := netip.ParsePrefix(a.Prefix)
				if ip.Addr().Is4() && a.Dynamic {
					plans[i].Address = a.Prefix
				}
			}
			for _, g := range networkNICs(observed)[i].Gateways {
				if g.Family == "ipv4" {
					plans[i].Gateway = g.Address
				}
			}
		}
	}
	plans[0].DynamicDNS = plans[0].DHCP4 && !hasExplicitDNS
	return finishNetworkPlan(plans, resolver, observed)
}

func inspectNIC(n iface, r4, r6 []route) (Network, error) {
	result := Network{Interface: n.Ifname, MAC: strings.ToLower(n.Address)}
	for _, a := range n.AddrInfo {
		if a.Scope != "global" {
			continue
		}
		ip, e := netip.ParseAddr(a.Local)
		if e != nil || !ip.IsGlobalUnicast() || a.Prefixlen < 0 || a.Prefixlen > ip.BitLen() {
			return Network{}, networkError()
		}
		dynamic := a.Dynamic || slices.Contains(a.Flags, "dynamic") || slices.Contains(a.Flags, "temporary")
		result.Addresses = append(result.Addresses, Address{Prefix: netip.PrefixFrom(ip, a.Prefixlen).String(), Dynamic: dynamic})
	}
	for family, routes := range map[string][]route{"ipv4": r4, "ipv6": r6} {
		for _, r := range routes {
			if r.Dev != n.Ifname {
				continue
			}
			if r.Dst != "default" {
				return Network{}, networkError()
			}
			result.Gateways = append(result.Gateways, Gateway{Family: family, Address: r.Gateway, OnLink: slices.Contains(r.Flags, "onlink"), Dynamic: r.Protocol == "dhcp" || r.Protocol == "ra"})
		}
	}
	sort.Slice(result.Addresses, func(i, j int) bool { return result.Addresses[i].Prefix < result.Addresses[j].Prefix })
	sort.Slice(result.Gateways, func(i, j int) bool {
		a, b := result.Gateways[i], result.Gateways[j]
		return a.Family+strconv.FormatBool(a.Dynamic)+a.Address < b.Family+strconv.FormatBool(b.Dynamic)+b.Address
	})
	if validateObservedNIC(result, len(result.Gateways) > 0) != nil {
		return Network{}, networkError()
	}
	return result, nil
}
func validateObservedNIC(n Network, primary bool) error {
	mac, e := net.ParseMAC(n.MAC)
	if e != nil || len(mac) != 6 || mac[0]&1 != 0 || mac.String() != n.MAC || n.MAC == "00:00:00:00:00:00" || !ifacePattern.MatchString(n.Interface) || len(n.Addresses) == 0 || len(n.Addresses) > 16 || len(n.Gateways) > 6 || primary && len(n.Gateways) == 0 {
		return networkError()
	}
	for _, a := range n.Addresses {
		p, e := netip.ParsePrefix(a.Prefix)
		if e != nil || !p.Addr().IsGlobalUnicast() || p.String() != a.Prefix {
			return networkError()
		}
	}
	counts := map[string]int{}
	seen := map[Gateway]bool{}
	for _, g := range n.Gateways {
		ip, e := netip.ParseAddr(g.Address)
		if e != nil || g.Family != "ipv4" && g.Family != "ipv6" || ip.Is4() != (g.Family == "ipv4") || seen[g] {
			return networkError()
		}
		seen[g] = true
		key := g.Family
		if g.Dynamic {
			key += "-dynamic"
		}
		counts[key]++
		if counts[key] > 1 || g.Family == "ipv4" && len(n.Gateways) > 0 && (counts["ipv4"]+counts["ipv4-dynamic"] > 1) {
			return networkError()
		}
	}
	return nil
}

// ValidateObservation binds the entire physical plan to authenticated evidence.
func (p StaticNetworkPlan) ValidateObservation(n Network) error {
	if _, _, _, e := p.RenderStaticNetwork(); e != nil {
		return e
	}
	if !slices.Equal(p.DNS, n.DNS) {
		return networkError()
	}
	return matchPlanObservation(p, n)
}
