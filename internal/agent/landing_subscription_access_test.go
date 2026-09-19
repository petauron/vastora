package agent

import (
	"slices"
	"testing"
)

func TestLandingSubscriptionInputIsRestrictedToOwnedBridgeAndPrivateListener(t *testing.T) {
	rule, err := landingSubscriptionInputRule("br-0123456789ab", "172.25.0.0/16", "100.64.0.2")
	want := []string{"-i", "br-0123456789ab", "-s", "172.25.0.0/16", "-d", "100.64.0.2/32", "-p", "tcp", "--dport", "2097", "-m", "comment", "--comment", "vastora-subscription-origin", "-j", "ACCEPT"}
	if err != nil || !slices.Equal(rule, want) {
		t.Fatal("input scope widened", rule, err)
	}
	for _, tc := range [][3]string{
		{"br-test", "0.0.0.0/0", "100.64.0.2"}, {"br-test", "172.25.0.0/16", "203.0.113.2"},
		{"br-test;", "172.25.0.0/16", "100.64.0.2"}, {"br-test", "172.25.1.1/16", "100.64.0.2"},
		{"br-test", "::/0", "100.64.0.2"},
	} {
		if _, err := landingSubscriptionInputRule(tc[0], tc[1], tc[2]); err == nil {
			t.Fatal("invalid input scope accepted", tc)
		}
	}
}

func TestStaleSubscriptionCleanupMatchesOnlyExactOwnedRule(t *testing.T) {
	valid := []string{"-A", "INPUT", "-i", "br-0123456789ab", "-s", "172.25.0.0/16", "-d", "100.64.0.2/32", "-p", "tcp", "--dport", "2097", "-m", "comment", "--comment", "vastora-subscription-origin", "-j", "ACCEPT"}
	if !landingSubscriptionRuleMatches(valid, "100.64.0.2") {
		t.Fatal("owned stale subscription rule was not recognized")
	}
	for _, mutate := range []func([]string){
		func(value []string) { value[1] = "FORWARD" },
		func(value []string) { value[3] = "invalid;bridge" },
		func(value []string) { value[5] = "0.0.0.0/0" },
		func(value []string) { value[9] = "udp" },
		func(value []string) { value[13] = "tcp" },
		func(value []string) { value[11] = "443" },
		func(value []string) { value[15] = "another-owner" },
		func(value []string) { value[17] = "DROP" },
	} {
		candidate := append([]string(nil), valid...)
		mutate(candidate)
		if landingSubscriptionRuleMatches(candidate, "100.64.0.2") {
			t.Fatalf("unowned firewall rule matched: %#v", candidate)
		}
	}
}
