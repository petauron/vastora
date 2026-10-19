package agent

import (
	"errors"
	"net/netip"

	"regexp"
	"strings"
)

func landingSubscriptionInputRule(bridge, subnet, address string) ([]string, error) {
	prefix, err := netip.ParsePrefix(subnet)
	ip, ipErr := netip.ParseAddr(address)
	if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || prefix.Bits() < 8 || prefix != prefix.Masked() || ipErr != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`).MatchString(bridge) {
		return nil, errors.New("agent: invalid private subscription input scope")
	}
	return []string{"-i", bridge, "-s", subnet, "-d", address + "/32", "-p", "tcp", "--dport", "2097", "-m", "comment", "--comment", "vastora-subscription-origin", "-j", "ACCEPT"}, nil
}

func landingSubscriptionRuleMatches(fields []string, address string) bool {
	if len(fields) < 3 || fields[0] != "-A" || fields[1] != "INPUT" {
		return false
	}
	required := map[string]string{"-d": address + "/32", "-p": "tcp", "--dport": "2097", "--comment": "vastora-subscription-origin", "-j": "ACCEPT"}
	seen := make(map[string]bool, len(required))
	bridge, subnet, commentMatch := "", "", false
	for index := 2; index+1 < len(fields); index++ {
		if fields[index] == "-i" {
			bridge = fields[index+1]
		}
		if fields[index] == "-s" {
			subnet = fields[index+1]
		}
		if fields[index] == "-m" && fields[index+1] == "comment" {
			commentMatch = true
		}
		value, exists := required[fields[index]]
		if !exists {
			continue
		}
		if seen[fields[index]] || strings.Trim(fields[index+1], `"`) != value {
			return false
		}
		seen[fields[index]] = true
		index++
	}
	prefix, err := netip.ParsePrefix(subnet)
	return len(seen) == len(required) && commentMatch && regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`).MatchString(bridge) && err == nil && prefix.Addr().Is4() && prefix.Addr().IsPrivate() && prefix.Bits() >= 8 && prefix == prefix.Masked()
}
