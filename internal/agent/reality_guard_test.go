package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeRealityTargetNetworkChecker struct {
	cdnProvider string
	wafProvider string
	err         error
}

func (f fakeRealityTargetNetworkChecker) CheckCDN(net.IP) (bool, string, error) {
	return f.cdnProvider != "", f.cdnProvider, f.err
}

func (f fakeRealityTargetNetworkChecker) CheckWAF(net.IP) (bool, string, error) {
	return f.wafProvider != "", f.wafProvider, f.err
}

func TestRealityTargetNetworkPolicyRejectsSharedProvidersAndValidationFailure(t *testing.T) {
	previous := realityNetworkChecker
	t.Cleanup(func() { realityNetworkChecker = previous })
	ip := net.ParseIP("203.0.113.10")

	realityNetworkChecker = fakeRealityTargetNetworkChecker{cdnProvider: "cloudfront"}
	if provider, err := checkRealityTargetNetworkPolicy(ip); err != nil || provider != "cloudfront" {
		t.Fatalf("CDN result = %q, %v", provider, err)
	}
	realityNetworkChecker = fakeRealityTargetNetworkChecker{wafProvider: "cloudflare"}
	if provider, err := checkRealityTargetNetworkPolicy(ip); err != nil || provider != "cloudflare" {
		t.Fatalf("WAF result = %q, %v", provider, err)
	}
	realityNetworkChecker = fakeRealityTargetNetworkChecker{err: errors.New("dataset unavailable")}
	if _, err := checkRealityTargetNetworkPolicy(ip); err == nil {
		t.Fatal("CDN/WAF validation failure was accepted")
	}
	realityNetworkChecker = fakeRealityTargetNetworkChecker{}
	if provider, err := checkRealityTargetNetworkPolicy(ip); err != nil || provider != "" {
		t.Fatalf("independent target rejected: %q, %v", provider, err)
	}
}

func TestRealityASNHintIsOptionalAndBounded(t *testing.T) {
	previous := realityASNLookup
	t.Cleanup(func() { realityASNLookup = previous })
	for _, value := range []struct {
		name string
		asn  int64
		err  error
		want int64
	}{
		{name: "known", asn: 64500, want: 64500},
		{name: "different network", asn: 64501, want: 64501},
		{name: "unavailable", err: errors.New("DNS unavailable")},
		{name: "invalid", asn: -1},
	} {
		t.Run(value.name, func(t *testing.T) {
			realityASNLookup = func(ctx context.Context, _ net.IP) (int64, error) {
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("ASN hint lookup has no deadline")
				}
				return value.asn, value.err
			}
			if got := realityASNHint(context.Background(), net.ParseIP("203.0.113.10")); got != value.want {
				t.Fatalf("ASN hint = %d, want %d", got, value.want)
			}
		})
	}
	realityASNLookup = func(context.Context, net.IP) (int64, error) {
		t.Fatal("missing or private node address must not be sent to ASN lookup")
		return 0, nil
	}
	for _, ip := range []net.IP{nil, net.ParseIP("10.0.0.1")} {
		if got := realityASNHint(context.Background(), ip); got != 0 {
			t.Fatalf("missing ASN = %d, want unknown", got)
		}
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

func TestRemoveLegacyRealityGuardPreservesUserXrayConfiguration(t *testing.T) {
	deleted := false
	xrayConfig := map[string]any{
		"outbounds": []any{
			map[string]any{"tag": legacyRealityGuardDirectTag, "protocol": "freedom"},
			map[string]any{"tag": legacyRealityGuardBlackholeTag, "protocol": "blackhole"},
			map[string]any{"tag": "user-direct", "protocol": "freedom"},
		},
		"routing": map[string]any{
			"domainStrategy": "IPIfNonMatch",
			"rules": []any{
				map[string]any{"type": "field", "inboundTag": []any{"vastora-test-guard"}, "domain": []any{"full:www.example.com"}, "outboundTag": legacyRealityGuardDirectTag},
				map[string]any{"type": "field", "inboundTag": []any{"vastora-test-guard"}, "outboundTag": legacyRealityGuardBlackholeTag},
				map[string]any{"type": "field", "inboundTag": []any{"user-inbound"}, "outboundTag": "user-direct"},
			},
		},
		"policy": map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true}}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			inbounds := []any{}
			if !deleted {
				inbounds = append(inbounds, map[string]any{"id": 2, "enable": true, "remark": legacyRealityGuardRemark, "listen": "127.0.0.1", "port": legacyRealityGuardPort, "protocol": "tunnel", "tag": "vastora-test-guard"})
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "obj": inbounds})
		case "POST /panel/api/xray/":
			encoded, _ := json.Marshal(threeXUIXraySettings{XraySetting: xrayConfig, OutboundTestURL: "https://example.com/"})
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "obj": string(encoded)})
		case "POST /panel/api/xray/update":
			if err := request.ParseForm(); err != nil || json.Unmarshal([]byte(request.Form.Get("xraySetting")), &xrayConfig) != nil {
				t.Fatal("obsolete Xray cleanup payload was invalid")
			}
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "obj": map[string]any{}})
		case "POST /panel/api/inbounds/del/2":
			if rules := xrayConfig["routing"].(map[string]any)["rules"].([]any); len(rules) != 1 || !routingRuleUsesInbound(rules[0], "user-inbound") {
				t.Fatalf("legacy routes were not removed before the inbound: %#v", rules)
			}
			deleted = true
			_ = json.NewEncoder(response).Encode(map[string]any{"success": true, "obj": map[string]any{}})
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	if err := removeLegacyRealityGuard(context.Background(), server.URL, "token", 0, "vastora-test"); err != nil || !deleted {
		t.Fatalf("legacy guard cleanup: deleted=%t, err=%v", deleted, err)
	}
	routing := xrayConfig["routing"].(map[string]any)
	if routing["domainStrategy"] != "IPIfNonMatch" || len(routing["rules"].([]any)) != 1 || xrayConfig["policy"] == nil {
		t.Fatalf("user Xray configuration was overwritten: %#v", xrayConfig)
	}
	outbounds := xrayConfig["outbounds"].([]any)
	if len(outbounds) != 1 || outbounds[0].(map[string]any)["tag"] != "user-direct" {
		t.Fatalf("obsolete outbounds survived cleanup: %#v", outbounds)
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
