package landing

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const routeFixture = `{
 "api":{"tag":"api"},
 "stats":{},
 "policy":{"system":{"statsInboundUplink":true}},
 "outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}],
 "routing":{"domainStrategy":"IPIfNonMatch","rules":[
  {"type":"field","inboundTag":["api"],"outboundTag":"api"},
  {"type":"field","ip":["geoip:private"],"outboundTag":"blocked"},
  {"type":"field","inboundTag":["business","other"],"port":"25","outboundTag":"blocked"},
  {"type":"field","inboundTag":["fallback-guard"],"outboundTag":"blocked"},
  {"type":"field","inboundTag":["custom"],"outboundTag":"direct"}
 ]}}
`

func fixtureRouteChange(t *testing.T, raw string) RouteChange {
	t.Helper()
	plan := testServerPlan()
	change, err := PrepareRouteChange(json.RawMessage(raw), 7, []string{"business"}, PeerIdentity{ID: "peer", PublicKey: "key", Address: plan.Address})
	if err != nil {
		t.Fatal(err)
	}
	return change
}

func TestLandingRoutePreservesUnselectedRulesAndSettings(t *testing.T) {
	change := fixtureRouteChange(t, routeFixture)
	before, _, _ := decodeRouteSettings(change.Before)
	after, _, _ := decodeRouteSettings(change.After)
	for _, key := range []string{"api", "stats", "policy"} {
		if !reflect.DeepEqual(before[key], after[key]) {
			t.Fatalf("changed unrelated %s", key)
		}
	}
	original := before["routing"].(map[string]any)["rules"].([]any)
	rules := after["routing"].(map[string]any)["rules"].([]any)
	if len(rules) != len(original)+3 || !reflect.DeepEqual(rules[0], original[0]) || !reflect.DeepEqual(rules[4:], original[1:]) {
		t.Fatal("original rule ordering or content was changed")
	}
	for i, outbound := range []string{"blocked", "blocked", proxyOutboundTag} {
		rule := rules[i+1].(map[string]any)
		if rule["outboundTag"] != outbound || !reflect.DeepEqual(rule["inboundTag"], []any{"business"}) {
			t.Fatal("new rule escaped selected business scope")
		}
	}
	outbounds := after["outbounds"].([]any)
	if !reflect.DeepEqual(outbounds[:2], before["outbounds"]) || outbounds[2].(map[string]any)["protocol"] != "socks" {
		t.Fatal("changed original outbounds or omitted SOCKS")
	}
}

func TestLandingRouteAcceptsThreeXUIAPIRuleRelocation(t *testing.T) {
	change := fixtureRouteChange(t, routeFixture)
	_, saved, err := decodeRouteSettings(change.After)
	if err != nil {
		t.Fatal(err)
	}
	if _, write, err := change.NextWrite(saved, true); err != nil || write {
		t.Fatalf("3x-ui API rule relocation must not cause a retry: write=%v err=%v", write, err)
	}
	if _, write, err := change.NextWrite(saved, false); err != nil || !write {
		t.Fatalf("normalized configuration must remain restorable: write=%v err=%v", write, err)
	}
	var config map[string]any
	if err := json.Unmarshal(saved, &config); err != nil {
		t.Fatal(err)
	}
	rules := config["routing"].(map[string]any)["rules"].([]any)
	rules[1], rules[3] = rules[3], rules[1]
	drift, _ := json.Marshal(config)
	if _, _, err := change.NextWrite(drift, true); err == nil {
		t.Fatal("business rule reordering must remain a conflict")
	}
}

func TestLandingRouteRetryAndDisableRequireExactCheckpoint(t *testing.T) {
	change := fixtureRouteChange(t, routeFixture)
	for _, test := range []struct {
		current json.RawMessage
		enable  bool
		write   bool
		want    json.RawMessage
	}{{change.Before, true, true, change.After}, {change.After, true, false, change.After}, {change.After, false, true, change.Before}, {change.Before, false, false, change.Before}} {
		got, write, err := change.NextWrite(test.current, test.enable)
		if err != nil || write != test.write || string(got) != string(test.want) {
			t.Fatalf("unexpected retry decision: write=%v err=%v", write, err)
		}
	}
	drift := json.RawMessage(strings.Replace(string(change.After), `"stats":{}`, `"stats":{"custom":true}`, 1))
	for _, enable := range []bool{true, false} {
		if _, _, err := change.NextWrite(drift, enable); err == nil {
			t.Fatal("overwrote a concurrent settings change")
		}
	}
}

func TestLandingRouteRefusesDNSLeaksAndAmbiguousPolicies(t *testing.T) {
	plan := testServerPlan()
	for _, raw := range []string{
		strings.Replace(routeFixture, "IPIfNonMatch", "IPOnDemand", 1),
		strings.Replace(routeFixture, `"outboundTag":"direct"`, `"balancerTag":"custom"`, 1),
		strings.Replace(routeFixture, `"tag":"direct"`, `"tag":"vastora-landing-egress"`, 1),
		strings.Replace(routeFixture, `"outboundTag":"direct"`, `"outboundTag":"unknown"`, 1),
	} {
		if _, err := PrepareRouteChange(json.RawMessage(raw), 1, []string{"business"}, PeerIdentity{ID: "peer", PublicKey: "key", Address: plan.Address}); err == nil {
			t.Fatal("accepted unsafe original routing")
		}
	}
}

func TestLandingRouteRoundTripPreservesLargeIntegers(t *testing.T) {
	raw := strings.Replace(routeFixture, `"stats":{}`, `"stats":{"counter":9007199254740993}`, 1)
	change := fixtureRouteChange(t, raw)
	if !strings.Contains(string(change.After), "9007199254740993") {
		t.Fatal("rounded unrelated settings while changing routing")
	}
}
