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
