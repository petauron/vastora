package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestNativeSubscriptionCredentialOwnershipCanonicalizesEquivalentURIs(t *testing.T) {
	first := "vless://11111111-2222-4333-8444-555555555555@ENTRY.example.test?security=reality&type=tcp&pbk=key#Old"
	second := "vless://11111111-2222-4333-8444-555555555555@entry.example.test:443?pbk=key&type=tcp&security=reality#New"
	if !sameNativeSubscriptionCredentials([]string{first}, []string{second}, false) {
		t.Fatal("equivalent VLESS URI formatting changed credential ownership")
	}
}

func TestBackgroundSubscriptionRefreshCannotInitializeAuthority(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	if err := store.refreshNativeSubscriptions(context.Background(), server.URL, "local-token"); err != nil {
		t.Fatal(err)
	}
	if requests != 0 {
		t.Fatal("background refresh inspected runtime before explicit authority initialization")
	}
	state, err := store.landingController(context.Background())
	if err != nil || state != nil {
		t.Fatalf("background refresh created authority state: state=%#v err=%v", state, err)
	}
}

func TestNativeSubscriptionUpdateCannotReplaceIdentity(t *testing.T) {
	mutation := &nativeSubscriptionMutation{action: "update", email: "Old", newEmail: "New"}
	prior := landingNativeSubscription{ID: "old-id", Email: "Old"}
	client := ThreeXUIClientView{ID: "new-id", Email: "New"}
	if mutation.accepts(client, landingNativeSubscription{}, false) {
		t.Fatal("client update accepted a controller-side identity replacement")
	}
	client.ID = prior.ID
	if !mutation.accepts(client, prior, true) {
		t.Fatal("client update rejected the journaled identity rename")
	}
}
