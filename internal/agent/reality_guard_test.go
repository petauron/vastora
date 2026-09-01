package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
				map[string]any{"type": "field", "domain": []any{"full:stale.example.com"}, "outboundTag": realityGuardDirectTag},
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

func TestDecodeThreeXUIXraySettingsUsesCurrentAPIEncoding(t *testing.T) {
	inner, _ := json.Marshal(map[string]any{
		"xraySetting":     map[string]any{"routing": map[string]any{"rules": []any{}}},
		"outboundTestUrl": "https://example.com/",
	})
	payload, _ := json.Marshal(string(inner))
	settings, err := decodeThreeXUIXraySettings(payload)
	if err != nil || settings.XraySetting["routing"] == nil || settings.OutboundTestURL != "https://example.com/" {
		t.Fatalf("decoded Xray settings = %#v, err = %v", settings, err)
	}
	if _, err := decodeThreeXUIXraySettings(json.RawMessage(`{"xraySetting":{}}`)); err == nil {
		t.Fatal("obsolete object response was accepted")
	}
}

func TestEnsureRealityGuardCompanionReplacesStaleManagedPort(t *testing.T) {
	deleted, added := false, false
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			if added {
				_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":2,"enable":true,"remark":"Vastora REALITY fallback guard","listen":"127.0.0.1","port":21000,"protocol":"tunnel","tag":"vastora-new-guard","settings":{"network":"tcp","address":"203.0.113.10","port":443}}]}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":1,"enable":true,"remark":"Vastora REALITY fallback guard","listen":"127.0.0.1","port":21000,"protocol":"tunnel","tag":"vastora-new-guard","settings":{"network":"tcp","address":"203.0.113.11","port":443}}]}`))
		case "POST /panel/api/inbounds/del/1":
			deleted = true
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/inbounds/add":
			if !deleted {
				t.Fatal("replacement was created before stale guard deletion")
			}
			added = true
			_, _ = response.Write([]byte(`{"success":true,"obj":{"id":2,"tag":"vastora-new-guard"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	companion, err := ensureRealityGuardCompanion(context.Background(), server.URL, "token", 0, 443, "vastora-new", "203.0.113.10")
	if err != nil || !deleted || !added || companion.ID != 2 || companion.Tag != "vastora-new-guard" {
		t.Fatalf("companion = %#v, deleted=%t, added=%t, err=%v", companion, deleted, added, err)
	}
}

func TestRealityGuardRulesSurviveThreeXUISystemRuleNormalization(t *testing.T) {
	rules := []any{
		map[string]any{"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api"},
		map[string]any{"type": "field", "inboundTag": []any{"vastora-test-guard"}, "domain": []any{"full:www.example.com"}, "outboundTag": realityGuardDirectTag},
		map[string]any{"type": "field", "inboundTag": []any{"vastora-test-guard"}, "outboundTag": realityGuardBlackholeTag},
		map[string]any{"type": "field", "protocol": []any{"bittorrent"}, "outboundTag": "blocked"},
	}
	if !realityGuardRulesSurvived(rules, "vastora-test-guard", "www.example.com") {
		t.Fatal("3x-ui system rule normalization rejected a valid guard")
	}
	rules[0] = map[string]any{"type": "field", "outboundTag": "direct"}
	if realityGuardRulesSurvived(rules, "vastora-test-guard", "www.example.com") {
		t.Fatal("an unrestricted route was allowed ahead of the guard")
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

func TestRealityTargetHostnameRequiresDotCom(t *testing.T) {
	for _, hostname := range []string{"www.intel.com", "download.amd.com", "EXAMPLE.COM."} {
		if !validRealityTargetHostname(hostname) {
			t.Fatalf("valid .com hostname %q was rejected", hostname)
		}
	}
	for _, hostname := range []string{"example.xyz", "example.net", "com", "bad..com"} {
		if validRealityTargetHostname(hostname) {
			t.Fatalf("non-.com or invalid hostname %q was accepted", hostname)
		}
	}
}
