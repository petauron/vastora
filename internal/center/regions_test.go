package center

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"unicode/utf8"

	"github.com/petauron/vastora/internal/networking"
)

func TestComposeRealityDisplayNameUsesStableRegionPrefix(t *testing.T) {
	code, name, displayName, err := composeRealityDisplayName("us", " Oracle 9929 ")
	if err != nil {
		t.Fatal(err)
	}
	if code != "US" || name != "Oracle 9929" || displayName != "🇺🇸 美国Oracle 9929" {
		t.Fatalf("composed name = code %q, name %q, display %q", code, name, displayName)
	}
	if _, _, _, err := composeRealityDisplayName("ZZ", "Oracle"); err == nil {
		t.Fatal("unsupported ISO region was accepted")
	}
}

func TestRealityBaseNameUnderstandsOldAndCurrentPrefixes(t *testing.T) {
	for displayName, expected := range map[string]string{
		"🇺🇸 US · CloudLead": "CloudLead",
		"🇺🇸 美国CloudLead":    "CloudLead",
		"CloudLead-test":    "CloudLead-test",
	} {
		if actual := realityBaseName(displayName, "US"); actual != expected {
			t.Fatalf("base name for %q = %q, want %q", displayName, actual, expected)
		}
	}
}

func TestHongKongRealityNamesUseShortPrefix(t *testing.T) {
	code, name, displayName, err := composeRealityDisplayName("hk", "| VMISS DC2")
	if err != nil {
		t.Fatal(err)
	}
	if code != "HK" || name != "| VMISS DC2" || displayName != "🇭🇰 香港| VMISS DC2" {
		t.Fatalf("composed name = code %q, name %q, display %q", code, name, displayName)
	}
	if !validRegionPrefixedRealityName(code, displayName) || validRegionPrefixedRealityName(code, "🇭🇰 中国香港特别行政区| VMISS DC2") {
		t.Fatal("only the short Hong Kong prefix should be generated and accepted")
	}
	for _, stored := range []string{"🇭🇰 中国香港特别行政区| VMISS DC2", displayName} {
		base := realityBaseName(stored, code)
		_, _, reconciled, err := composeRealityDisplayName(code, base)
		if err != nil || base != name || reconciled != displayName {
			t.Fatalf("normalize %q = base %q, display %q, error %v", stored, base, reconciled, err)
		}
	}
	for _, region := range (&Store{}).Regions() {
		if region.Code == "HK" {
			if region.NameZH != "香港" || region.Prefix != "🇭🇰 香港" {
				t.Fatalf("Hong Kong region = %#v", region)
			}
			return
		}
	}
	t.Fatal("Hong Kong is missing from the region list")
}

func TestSubscriptionRegionNamesStayShortAndDistinct(t *testing.T) {
	for code, expected := range map[string]string{
		"AE": "阿联酋", "BA": "波黑", "MO": "澳门", "PG": "巴新", "PS": "巴勒斯坦",
		"DO": "多米尼加", "DM": "多米尼克", "VG": "英属维尔京", "VI": "美属维尔京",
		"CD": "刚果（金）", "CG": "刚果（布）", "GS": "GS", "US": "美国", "JP": "日本",
	} {
		if actual := regionNameZH(code); actual != expected {
			t.Fatalf("region %s = %q, want %q", code, actual, expected)
		}
	}
	for _, region := range (&Store{}).Regions() {
		name := regionNameZH(region.Code)
		if utf8.RuneCountInString(name) > maxRegionNameRunes {
			t.Fatalf("region %s prefix remains too long: %q", region.Code, name)
		}
		if name == region.Code && region.NameZH != regionFullNameZH(region.Code) {
			t.Fatalf("region %s lost its full picker name", region.Code)
		}
		for _, stored := range []string{
			regionFlag(region.Code) + " " + regionFullNameZH(region.Code) + "| Edge",
			region.Prefix + "| Edge",
			regionFlag(region.Code) + " " + region.Code + " · Edge",
		} {
			base := realityBaseName(stored, region.Code)
			if base != "| Edge" && base != "Edge" {
				t.Fatalf("region %s left part of its prefix in %q: %q", region.Code, stored, base)
			}
			_, _, canonical, err := composeRealityDisplayName(region.Code, base)
			if err != nil || realityBaseName(canonical, region.Code) != base {
				t.Fatalf("region %s does not round-trip: %q, %v", region.Code, canonical, err)
			}
		}
	}
}

func TestSuggestAgentRegionUsesConfirmedPublicGatewayAddress(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.91", Interface: "eth0", Kind: networking.KindLAN},
		{Address: "203.0.113.91", Interface: "eth0", Kind: networking.KindPublic},
	}, networking.Profile{ServiceAddress: "10.0.0.91", LANAddress: "10.0.0.91", PublicAddress: "203.0.113.91", EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true})
	var lookedUp string
	store.lookupPublicRegion = func(_ context.Context, address string) (string, error) {
		lookedUp = address
		return "US", nil
	}
	suggestion, err := store.SuggestAgentRegion(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lookedUp != "203.0.113.91" || suggestion.AgentID != node.ID || suggestion.RegionCode != "US" || suggestion.Prefix != "🇺🇸 美国" {
		t.Fatalf("region suggestion = %#v, lookup=%q", suggestion, lookedUp)
	}
}

func TestCountryISLookupRejectsMismatchedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"ip":"198.51.100.8","country":"US"}`)
	}))
	defer server.Close()
	lookup := regionLookupAt(server.Client(), server.URL)
	if _, err := lookup(context.Background(), "203.0.113.8"); err == nil {
		t.Fatal("country response for a different address was accepted")
	}
}
