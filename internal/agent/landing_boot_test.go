package agent

import (
	"strings"
	"testing"
)

func TestLegacyLandingDNSHookIsNarrowlyRecognized(t *testing.T) {
	path := "/etc/legacy-landing-dns.nft"
	hook := "[Service]\nExecStartPost=/usr/sbin/nft -f " + path + "\n"
	v4 := `insert rule inet vastora_landing_server_0123456789abcdef destinations ip daddr 100.100.100.100 meta l4proto { tcp, udp } th dport 53 return comment "allow designated tailscale DNS"`
	v6 := `insert rule inet vastora_landing_server_0123456789abcdef destinations oifname "tailscale0" ip6 daddr fd7a:115c:a1e0::53 meta l4proto { tcp, udp } th dport 53 counter return comment "designated tailnet DNS"`
	for _, rules := range []string{v4, v4 + "\n" + v6 + "\n"} {
		if got := legacyLandingDNSPath([]byte(hook), []byte(rules)); got != path {
			t.Fatalf("known DNS-only workaround not recognized: %q", got)
		}
	}
	for name, pair := range map[string][2]string{
		"extra command":  {hook + "ExecStartPost=/usr/bin/true\n", v4},
		"shell":          {strings.Replace(hook, "/usr/sbin/nft -f", "/bin/sh -c", 1), v4},
		"path traversal": {strings.Replace(hook, path, "/etc/../tmp/rules.nft", 1), v4},
		"other section":  {strings.Replace(hook, "[Service]", "[Unit]", 1), v4},
		"missing rules":  {hook, ""},
		"extra rule":     {hook, v4 + "\nflush ruleset"},
		"other target":   {hook, strings.Replace(v4, "100.100.100.100", "1.1.1.1", 1)},
		"other port":     {hook, strings.Replace(v4, "dport 53", "dport 443", 1)},
		"other table":    {hook, strings.Replace(v4, "vastora_landing_server_", "custom_", 1)},
		"mixed tables":   {hook, v4 + "\n" + strings.Replace(v6, "0123456789abcdef", "1123456789abcdef", 1)},
		"duplicate":      {hook, v4 + "\n" + v4},
	} {
		t.Run(name, func(t *testing.T) {
			if got := legacyLandingDNSPath([]byte(pair[0]), []byte(pair[1])); got != "" {
				t.Fatal("unrecognized administrator configuration accepted", got)
			}
		})
	}
}
