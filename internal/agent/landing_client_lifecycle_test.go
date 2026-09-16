package agent

import "testing"

func TestNativeSubscriptionCredentialOwnershipIgnoresNamesButRejectsMaterialChanges(t *testing.T) {
	base := "vless://11111111-2222-4333-8444-555555555555@entry.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=example.com&pbk=public-key&sid=deadbeef"
	if !sameNativeSubscriptionCredentials([]string{base + "#Old"}, []string{base + "#New"}, false) {
		t.Fatal("a display-only rename changed credential ownership")
	}
	if sameNativeSubscriptionCredentials([]string{base + "#Old"}, []string{base + "-changed#Old"}, false) {
		t.Fatal("changed REALITY material was accepted")
	}
	other := "vless://11111111-2222-4333-8444-555555555555@other.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=example.com&pbk=another-key&sid=cafebabe#Other"
	if !sameNativeSubscriptionCredentials([]string{base + "#Old"}, []string{base + "#Old", other}, true) {
		t.Fatal("an additional explicitly managed entry was rejected")
	}
	if sameNativeSubscriptionCredentials([]string{base + "#Old"}, []string{base + "#Old", other}, false) {
		t.Fatal("background observation changed the authoritative route inventory")
	}
}
