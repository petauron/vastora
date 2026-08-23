package center

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestComposeRealityDisplayNameUsesStableRegionPrefix(t *testing.T) {
	code, name, displayName, err := composeRealityDisplayName("us", " Oracle 9929 ")
	if err != nil {
		t.Fatal(err)
	}
	if code != "US" || name != "Oracle 9929" || displayName != "🇺🇸 US · Oracle 9929" {
		t.Fatalf("composed name = code %q, name %q, display %q", code, name, displayName)
	}
	if _, _, _, err := composeRealityDisplayName("ZZ", "Oracle"); err == nil {
		t.Fatal("unsupported ISO region was accepted")
	}
}

func TestSuggestAgentRegionUsesConfirmedPublicGatewayAddress(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "gateway", NodeCapabilities{Gateway: true}, []networking.Candidate{
		{Address: "10.0.0.91", Interface: "eth0", Family: "ipv4", Kind: networking.KindLAN},
		{Address: "203.0.113.91", Interface: "eth0", Family: "ipv4", Kind: networking.KindPublic},
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
	if lookedUp != "203.0.113.91" || suggestion.AgentID != node.ID || suggestion.RegionCode != "US" || suggestion.Prefix != "🇺🇸 US" {
		t.Fatalf("region suggestion = %#v, lookup=%q", suggestion, lookedUp)
	}
}

func TestCountryISLookupRejectsMismatchedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprint(writer, `{"ip":"198.51.100.8","country":"US"}`)
	}))
	defer server.Close()
	lookup := countryISLookupAt(server.Client(), server.URL)
	if _, err := lookup(context.Background(), "203.0.113.8"); err == nil {
		t.Fatal("country response for a different address was accepted")
	}
}
