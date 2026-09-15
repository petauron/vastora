package landing

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func subscriptionFixture() SubscriptionGrant {
	grant := clientGrantFixture("combination-a", FixedMode)
	query := "?type=tcp&security=reality&flow=xtls-rprx-vision&sni=example.com&pbk=public-key&sid=deadbeef"
	return SubscriptionGrant{Grant: grant, EntryName: "入口 A", LandingRegionCode: "US", BaseLink: "vless://11111111-2222-4333-8444-555555555555@entry.example.test:443" + query + "#original", FixedLink: "vless://aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee@entry.example.test:443" + query}
}

func TestCompositionPrimitiveCanOmitOwnExit(t *testing.T) {
	item := subscriptionFixture()
	item.Grant.HideBase = true
	out, err := ComposeLinks([]byte(item.BaseLink+"\n"), item.Grant.ParentID, FixedMode, []SubscriptionGrant{item}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "11111111-2222-4333-8444-555555555555") || strings.Count(string(out), "vless://") != 1 {
		t.Fatal("own exit remained in subscription")
	}
	out, err = ComposeMihomo([]byte(subscriptionRealityFixture), item.Grant.ParentID, FixedMode, []SubscriptionGrant{item})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "11111111-2222-4333-8444-555555555555") {
		t.Fatal("own exit remained in Mihomo")
	}
	var config map[string]any
	if err := yaml.Unmarshal(out, &config); err != nil {
		t.Fatal(err)
	}
	if len(config["proxies"].([]any)) != 1 {
		t.Fatal("expected only the selected fixed exit")
	}
	for _, value := range config["proxy-groups"].([]any) {
		for _, member := range value.(map[string]any)["proxies"].([]any) {
			if member == "Original" {
				t.Fatal("dangling original group member")
			}
		}
	}
}

func TestFixedSubscriptionPreservesNativeAndDistinctCredentials(t *testing.T) {
	item := subscriptionFixture()
	native := item.BaseLink + "\n"
	for _, encoded := range []bool{false, true} {
		input := []byte(native)
		if encoded {
			input = []byte(base64.StdEncoding.EncodeToString(input))
		}
		out, err := ComposeLinks(input, item.Grant.ParentID, FixedMode, []SubscriptionGrant{item}, encoded)
		if err != nil {
			t.Fatal(err)
		}
		if encoded {
			out, err = base64.StdEncoding.DecodeString(string(out))
			if err != nil {
				t.Fatal(err)
			}
		}
		if !strings.HasPrefix(string(out), native) || strings.Count(string(out), "vless://") != 2 || !strings.Contains(string(out), "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee") {
			t.Fatal("lost original or independent child identity")
		}
	}
	if _, err := ComposeLinks([]byte(native), Identity("another-user"), FixedMode, []SubscriptionGrant{item}, false); err == nil {
		t.Fatal("another parent obtained the fixed credential")
	}
	if _, err := ComposeLinks([]byte("vless://other@entry.example.test:443?security=reality"), item.Grant.ParentID, FixedMode, []SubscriptionGrant{item}, false); err == nil {
		t.Fatal("mismatched native user obtained a fixed credential")
	}
	_, err := ComposeLinks([]byte(native), item.Grant.ParentID, PublishingMode("advanced"), []SubscriptionGrant{item}, false)
	if err == nil {
		t.Fatal("ordinary format pretended to support advanced chains")
	}
}

func TestSubscriptionTransportComparisonUsesValuesNotQueryOrder(t *testing.T) {
	item := subscriptionFixture()
	base, err := parseVLESSLink(item.BaseLink)
	if err != nil {
		t.Fatal(err)
	}
	base.RawQuery = base.Query().Encode()
	if !sameLinkIdentity(item.BaseLink, base.String()) {
		t.Fatal("query ordering rejected identical native credentials")
	}
	query := base.Query()
	query.Set("pbk", "another-key")
	base.RawQuery = query.Encode()
	if sameLinkIdentity(item.BaseLink, base.String()) {
		t.Fatal("accepted another REALITY key")
	}
}

func TestMihomoFixedCombinationsNeedNoClientChain(t *testing.T) {
	item := subscriptionFixture()
	other := item
	other.Grant.ID = "combination-b"
	other.Grant.Peer = PeerIdentity{ID: "landing-b", PublicKey: "key-b", Address: "100.64.0.9"}
	other.Grant.FixedUser = FixedUser(other.Grant.ID)
	other.Grant.FixedIdentity = Identity("bbbbbbbb-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	other.FixedLink = strings.Replace(item.FixedLink, "aaaaaaaa-bbbb", "bbbbbbbb-bbbb", 1)
	other.LandingRegionCode = "TW"
	items := []SubscriptionGrant{item, other}
	out, err := ComposeMihomo([]byte(subscriptionRealityFixture), item.Grant.ParentID, FixedMode, items)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if yaml.Unmarshal(out, &config) != nil {
		t.Fatal("invalid YAML")
	}
	if len(config["proxies"].([]any)) != 3 || len(config["proxy-groups"].([]any)) != 1 {
		t.Fatal("expected original plus two independent nodes, without added strategy groups")
	}
	if strings.Contains(string(out), "dialer-proxy") || strings.Contains(string(out), "socks5") {
		t.Fatal("client-side chain remains")
	}
	if !strings.Contains(string(out), "🇺🇸｜入口 A") || !strings.Contains(string(out), "🇹🇼｜入口 A") || strings.Contains(string(out), "入口 A A") || strings.Contains(string(out), "入口 A B") || strings.Contains(string(out), "落地") || strings.Contains(string(out), "combination-") {
		t.Fatal("combination names did not use flag-only landing labels")
	}
	links, err := ComposeLinks([]byte(item.BaseLink+"\n"), item.Grant.ParentID, FixedMode, items, false)
	if err != nil || strings.Count(string(links), "vless://") != 3 {
		t.Fatalf("ordinary subscription lost a combination: %v", err)
	}
}

const subscriptionRealityFixture = `proxies:
  - {name: Original, type: vless, uuid: 11111111-2222-4333-8444-555555555555, server: entry.example.test, port: 443, tls: true, servername: example.com, flow: xtls-rprx-vision, reality-opts: {public-key: public-key, short-id: deadbeef}, udp: true}
proxy-groups:
  - {name: Select, type: select, proxies: [Original, DIRECT]}
rules: ["MATCH,Select"]
`

func TestSubscriptionMihomoRejectsChangedRealityMaterial(t *testing.T) {
	item := subscriptionFixture()
	for name, change := range map[string]func(map[string]any){
		"tls":     func(p map[string]any) { p["tls"] = false },
		"sni":     func(p map[string]any) { p["servername"] = "another.test" },
		"key":     func(p map[string]any) { p["reality-opts"].(map[string]any)["public-key"] = "another-key" },
		"flow":    func(p map[string]any) { p["flow"] = "" },
		"network": func(p map[string]any) { p["network"] = "ws" },
	} {
		t.Run(name, func(t *testing.T) {
			var native map[string]any
			if err := yaml.Unmarshal([]byte(subscriptionRealityFixture), &native); err != nil {
				t.Fatal(err)
			}
			change(native["proxies"].([]any)[0].(map[string]any))
			data, _ := yaml.Marshal(native)
			if _, err := ComposeMihomo(data, item.Grant.ParentID, FixedMode, []SubscriptionGrant{item}); err == nil {
				t.Fatal("changed native transport accepted")
			}
		})
	}
}
