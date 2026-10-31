package osreinstall

import (
	"encoding/json"
	"net/netip"
	"slices"
	"strings"
)

// Only connected/local routes, declared defaults and RA/DHCP routes belonging
// to an enabled dynamic policy can be recreated. Source routing fails closed.
func ValidatePlanRouting(rulesJSON, routesJSON []byte, family string, plan StaticNetworkPlan, observed Network) error {
	var rules []map[string]json.RawMessage
	if len(rulesJSON) > 1<<20 || json.Unmarshal(rulesJSON, &rules) != nil {
		return networkError()
	}
	wanted := map[int]string{0: "local", 32766: "main", 32767: "default"}
	if family == "-6" {
		delete(wanted, 32767)
	}
	if len(rules) != len(wanted) {
		return networkError()
	}
	for _, r := range rules {
		var priority int
		var src, table string
		if len(r) != 3 || json.Unmarshal(r["priority"], &priority) != nil || json.Unmarshal(r["src"], &src) != nil || json.Unmarshal(r["table"], &table) != nil || src != "all" || wanted[priority] != table {
			return networkError()
		}
		delete(wanted, priority)
	}
	var routes []map[string]json.RawMessage
	if len(routesJSON) > 2<<20 || json.Unmarshal(routesJSON, &routes) != nil || len(routes) > 256 {
		return networkError()
	}
	get := func(r map[string]json.RawMessage, k string) string {
		var v string
		if x, ok := r[k]; ok {
			if json.Unmarshal(x, &v) != nil {
				return "!invalid"
			}
		}
		return v
	}
	plans := map[string]StaticNetworkPlan{}
	networks := map[string]Network{}
	for _, p := range planNICs(plan) {
		plans[p.Interface] = p
	}
	for _, n := range networkNICs(observed) {
		networks[n.Interface] = n
	}
	declaredRoutes := map[string]bool{}
	for _, p := range planNICs(plan) {
		for _, dst := range p.DirectRoutes {
			ip, _ := netip.ParsePrefix(dst)
			if ip.Addr().Is4() == (family == "-4") {
				declaredRoutes[p.Interface+":"+dst] = false
			}
		}
	}
	for _, r := range routes {
		for k := range r {
			if !slices.Contains([]string{"dst", "dev", "gateway", "table", "protocol", "scope", "prefsrc", "type", "flags", "metric", "pref", "expires", "nhid"}, k) {
				return networkError()
			}
		}
		dst, dev, via, table, proto, kind := get(r, "dst"), get(r, "dev"), get(r, "gateway"), get(r, "table"), get(r, "protocol"), get(r, "type")
		if table != "" && table != "main" && table != "local" {
			return networkError()
		}
		if raw, ok := r["metric"]; ok && (proto == "static" || proto == "boot" || proto == "") {
			var metric int
			if json.Unmarshal(raw, &metric) != nil || family == "-4" && metric != 0 || family == "-6" && metric != 1024 {
				return networkError()
			}
		}
		var flags []string
		if raw, ok := r["flags"]; ok && json.Unmarshal(raw, &flags) != nil {
			return networkError()
		}
		for _, f := range flags {
			if f != "onlink" {
				return networkError()
			}
		}
		p, ok := plans[dev]
		if !ok && dev != "lo" {
			return networkError()
		}
		if table == "local" && proto == "kernel" && (kind == "local" || kind == "broadcast" || kind == "multicast") {
			ip, e := netip.ParsePrefix(dst)
			if e != nil {
				a, e := netip.ParseAddr(dst)
				if e != nil {
					return networkError()
				}
				ip = netip.PrefixFrom(a, a.BitLen())
			}
			if dev == "lo" {
				if !ip.Addr().IsLoopback() {
					return networkError()
				}
			} else if !ip.Addr().IsLinkLocalUnicast() && !ip.Addr().IsMulticast() && !slices.ContainsFunc(networks[dev].Addresses, func(a Address) bool {
				prefix, e := netip.ParsePrefix(a.Prefix)
				return e == nil && prefix.Masked().Contains(ip.Addr())
			}) {
				return networkError()
			}
			continue
		}
		if table == "local" || kind != "" {
			return networkError()
		}
		if dst == "default" {
			if family == "-4" && via == p.Gateway && (proto == "" || proto == "boot" || proto == "static" || p.DHCP4 && proto == "dhcp") && slices.Contains(flags, "onlink") == p.GatewayOnLink {
				continue
			}
			if family == "-6" && via == p.Gateway6 && (proto == "" || proto == "boot" || proto == "static") && slices.Contains(flags, "onlink") == p.Gateway6OnLink {
				continue
			}
			if family == "-6" && proto == "ra" && p.IPv6AcceptRA != "0" && slices.ContainsFunc(networks[dev].Gateways, func(g Gateway) bool { return g.Dynamic && g.Address == via }) {
				continue
			}
			return networkError()
		}
		ip, e := netip.ParsePrefix(dst)
		if e != nil {
			a, e := netip.ParseAddr(dst)
			if e != nil {
				return networkError()
			}
			ip = netip.PrefixFrom(a, a.BitLen())
		}
		if _, declared := declaredRoutes[dev+":"+ip.String()]; declared && via == "" && (proto == "static" || proto == "boot" || proto == "") && get(r, "scope") == "link" {
			declaredRoutes[dev+":"+ip.String()] = true
			continue
		}
		if family == "-6" && ip.Addr().IsLoopback() && dev == "lo" && proto == "kernel" {
			continue
		}
		if family == "-6" && (ip.Addr().IsLinkLocalUnicast() || ip.Addr().IsMulticast()) && via == "" && proto == "kernel" {
			continue
		}
		if via == "" && (proto == "kernel" || proto == "ra" && p.IPv6AcceptRA != "0" || proto == "dhcp" && p.DHCP4) && slices.ContainsFunc(networks[dev].Addresses, func(a Address) bool {
			prefix, e := netip.ParsePrefix(a.Prefix)
			return e == nil && (ip == prefix.Masked() || ip.Addr() == prefix.Addr())
		}) {
			continue
		}
		// Explicit host reachability route used by /32 onlink gateways.
		if family == "-4" && p.GatewayOnLink && via == "" && ip.Bits() == 32 && ip.Addr().String() == p.Gateway && get(r, "scope") == "link" && (proto == "static" || proto == "boot" || proto == "") {
			continue
		}
		if strings.Contains(proto, "invalid") {
			return networkError()
		}
		return networkError()
	}
	for _, present := range declaredRoutes {
		if !present {
			return networkError()
		}
	}
	return nil
}
