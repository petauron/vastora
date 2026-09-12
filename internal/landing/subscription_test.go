package landing

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func subscriptionFixture() SubscriptionGrant {
	grant := clientGrantFixture("combination-a", BothMode)
	query := "?type=tcp&security=reality&flow=xtls-rprx-vision&sni=example.com&pbk=public-key&sid=deadbeef"
	return SubscriptionGrant{Grant: grant, EntryName: "入口 A", LandingName: "落地 A", BaseLink: "vless://11111111-2222-4333-8444-555555555555@entry.example.test:443" + query + "#original", FixedLink: "vless://aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee@entry.example.test:443" + query}
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
	out, err := ComposeLinks([]byte(native), item.Grant.ParentID, AdvancedMode, []SubscriptionGrant{item}, false)
	if err != nil || string(out) != native {
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

func TestMihomoLandingGroupUsesOnlyGrantedBaseAndNoDirectFallback(t *testing.T) {
	item := subscriptionFixture()
	native := []byte(`proxies:
  - {name: Original, type: vless, uuid: 11111111-2222-4333-8444-555555555555, server: entry.example.test, port: 443, tls: true, servername: example.com, flow: xtls-rprx-vision, reality-opts: {public-key: public-key, short-id: deadbeef}, udp: true}
proxy-groups:
  - {name: Select, type: select, proxies: [Original, DIRECT]}
rules: ["MATCH,Select"]
`)
	for _, mode := range []PublishingMode{FixedMode, AdvancedMode, BothMode} {
		out, err := ComposeMihomo(native, item.Grant.ParentID, mode, []SubscriptionGrant{item})
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if yaml.Unmarshal(out, &config) != nil {
			t.Fatal("invalid YAML")
		}
		fixed, socks := 0, 0
		for _, value := range config["proxies"].([]any) {
			proxy := value.(map[string]any)
			if proxy["uuid"] == "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee" {
				fixed++
				if proxy["udp"] != false {
					t.Fatal("fixed UDP capability was advertised")
				}
			}
			if proxy["type"] == "socks5" {
				socks++
				if proxy["udp"] != false || proxy["server"] != "100.64.0.8" || proxy["port"] != 1080 {
					t.Fatal("incorrect private SOCKS target")
				}
				found := false
				for _, value := range config["proxy-groups"].([]any) {
					group := value.(map[string]any)
					if group["name"] == proxy["dialer-proxy"] {
						found = true
						members := group["proxies"].([]any)
						if len(members) != 1 || members[0] != "Original" {
							t.Fatal("chain can select a fixed identity or DIRECT")
						}
					}
				}
				if !found {
					t.Fatal("dialer-proxy did not resolve to an entry group")
				}
			}
		}
		if (fixed == 1) != mode.Fixed() || (socks == 1) != mode.Advanced() {
			t.Fatal("publishing mode leaked another mode")
		}
		if !strings.Contains(string(out), "MATCH,Select") {
			t.Fatal("replaced original business rules")
		}
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
			if _, err := ComposeMihomo(data, item.Grant.ParentID, BothMode, []SubscriptionGrant{item}); err == nil {
				t.Fatal("changed native transport accepted")
			}
		})
	}
}
