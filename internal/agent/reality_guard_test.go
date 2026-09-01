package agent

import (
	"encoding/json"
	"testing"
)

func TestApplyRealityGuardRoutingIsFirstAndPreservesUserConfiguration(t *testing.T) {
	config := map[string]any{
		"outbounds": []any{
			map[string]any{"tag": "user-direct", "protocol": "freedom"},
			map[string]any{"tag": realityGuardDirectTag, "protocol": "socks"},
		},
		"routing": map[string]any{
			"domainStrategy": "IPIfNonMatch",
			"rules": []any{
				map[string]any{"type": "field", "inboundTag": []any{"user-inbound"}, "outboundTag": "user-direct"},
				map[string]any{"type": "field", "inboundTag": []any{"vastora-test-guard"}, "outboundTag": "stale"},
			},
		},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true}}},
	}
	applyRealityGuardRouting(config, "vastora-test-guard", "www.example.com")
	routing := config["routing"].(map[string]any)
	rules := routing["rules"].([]any)
	if len(rules) != 3 || !routingRuleMatches(rules[0], "vastora-test-guard", "full:www.example.com", realityGuardDirectTag) || !routingRuleMatches(rules[1], "vastora-test-guard", "", realityGuardBlackholeTag) || !routingRuleUsesInbound(rules[2], "user-inbound") {
		t.Fatalf("guard routes = %#v", rules)
	}
	if routing["domainStrategy"] != "IPIfNonMatch" || config["policy"] == nil {
		t.Fatalf("user Xray configuration was overwritten: %#v", config)
	}
	outbounds := config["outbounds"].([]any)
	if len(outbounds) != 3 || outbounds[0].(map[string]any)["protocol"] != "freedom" || outbounds[1].(map[string]any)["protocol"] != "blackhole" || outbounds[2].(map[string]any)["tag"] != "user-direct" {
		t.Fatalf("managed outbounds = %#v", outbounds)
	}
}

func TestRealityGuardPortMappingIsDeterministicAndBounded(t *testing.T) {
	guardPort, err := realityGuardPort(threeXUIRealityPort)
	if err != nil || guardPort != threeXUIRealityGuardPort {
		t.Fatalf("REALITY port mapped to %d: %v", guardPort, err)
	}
	if _, err := realityGuardPort(threeXUIRealityPort - 1); err == nil {
		t.Fatal("an unmanaged port was accepted")
	}
	if _, err := realityGuardPort(threeXUIRealityPort + 1); err == nil {
		t.Fatal("an unmanaged port was accepted")
	}
}

func TestRealityGuardTunnelAllowsOnlyPinnedIP443(t *testing.T) {
	settings, _ := json.Marshal(map[string]any{"network": "tcp", "address": "203.0.113.10", "port": 443})
	inbound := threeXUIRealityInbound{Protocol: "tunnel", Listen: "127.0.0.1", Port: 21000, Settings: settings}
	if !realityGuardTunnelTargets(inbound, "203.0.113.10") {
		t.Fatal("pinned target was rejected")
	}
	if realityGuardTunnelTargets(inbound, "203.0.113.11") {
		t.Fatal("a different target IP was accepted")
	}
}

func TestApplyRealityGuardRoutingUsesObservedRemoteCompanionTag(t *testing.T) {
	config := map[string]any{}
	applyRealityGuardRouting(config, "n7-vastora-test-guard", "www.example.com")
	rules := config["routing"].(map[string]any)["rules"].([]any)
	if !routingRuleMatches(rules[0], "n7-vastora-test-guard", "full:www.example.com", realityGuardDirectTag) || !routingRuleMatches(rules[1], "n7-vastora-test-guard", "", realityGuardBlackholeTag) {
		t.Fatalf("remote guard routes = %#v", rules)
	}
	if routingRuleUsesInbound(rules[0], "vastora-test-guard") {
		t.Fatal("remote route incorrectly used the unprefixed inbound tag")
	}
}

func TestParseTeamCymruASN(t *testing.T) {
	if asn, err := parseTeamCymruASN("64500 | 203.0.113.0/24 | ZZ | test | 2026-01-01"); err != nil || asn != 64500 {
		t.Fatalf("ASN = %d, err = %v", asn, err)
	}
	for _, invalid := range []string{"", "AS0 | none", "not-an-asn | none"} {
		if _, err := parseTeamCymruASN(invalid); err == nil {
			t.Fatalf("invalid ASN record %q was accepted", invalid)
		}
	}
}
