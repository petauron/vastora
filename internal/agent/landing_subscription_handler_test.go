package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func landingSubscriptionTestState(t *testing.T) (*Store, *landingControllerState, string) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "controller-install", ApplicationID: "controller", AppKey: threeXUIKey, Version: "3.7.0", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`), ServiceAddress: "100.64.0.8"}); err != nil {
		t.Fatal(err)
	}
	parentUUID := "11111111-2222-4333-8444-555555555555"
	childUUID := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	parent := landing.Identity(parentUUID)
	grant := landing.ClientGrant{ID: "grant-a", ParentID: parent, BaseIdentity: parent, BaseUser: "Phone", InboundTag: "business", FixedUser: landing.FixedUser("grant-a"), FixedIdentity: landing.Identity(childUUID), Peer: landing.PeerIdentity{ID: "landing-a", PublicKey: "key-a", Address: "100.64.0.9"}, Mode: landing.FixedMode, Enabled: true}
	link := func(id string) string {
		return "vless://" + id + "@entry.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=example.com&pbk=public-key&sid=deadbeef#Original"
	}
	state := &landingControllerState{ControllerID: "controller", Grants: map[string]landingControllerGrant{
		grant.ID: {Task: landing.ControllerTask{Grant: grant, Revision: 1, Phase: "activate", ControllerID: "controller", InboundID: 9, FixedUUID: childUUID, EntryName: "Entry A", LandingRegionCode: "US"}, Phase: "ready", ChildSubscription: "private-child-token", Material: landing.ControllerResult{BaseLink: link(parentUUID), FixedLink: link(childUUID)}},
	}, Accounts: map[string]landingControllerAccount{
		parent: {ID: parent, Email: "Phone", SubscriptionToken: "parent-sub-token", Mode: landing.FixedMode, Enabled: true, Total: 1000, Expiry: store.now().Add(time.Hour).UnixMilli(), Members: []landing.QuotaMember{{ID: parent, Observed: 100, Active: true}, {ID: grant.FixedIdentity, Observed: 50, Active: true}}},
	}, Subscriptions: map[string]landingNativeSubscription{
		parent:                           {ID: parent, Email: "Phone", Token: "parent-sub-token", Enabled: true, Total: 1000, Used: 100, Expiry: store.now().Add(time.Hour).UnixMilli(), Links: []string{link(parentUUID)}},
		landing.Identity("another-user"): {ID: landing.Identity("another-user"), Email: "Another", Token: "another-sub-token", Enabled: true, Links: []string{"vless://another-user@another.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=example.com&pbk=public-key&sid=deadbeef#Another"}},
	}}
	return store, state, parent
}

func TestLandingSubscriptionHandlerScopesMaterialAndLifecycle(t *testing.T) {
	store, state, parent := landingSubscriptionTestState(t)
	native := state.Grants["grant-a"].Material.BaseLink + "\n"
	account := state.Accounts[parent]
	// Account state is authoritative after Vastora adopts a native
	// subscription. A stale 3x-ui lifecycle snapshot must not keep returning
	// 404 after the saved account plan has been confirmed.
	stale := state.Subscriptions[parent]
	stale.Enabled, stale.Total, stale.Used, stale.Expiry = false, 1, 1, 1
	state.Subscriptions[parent] = stale
	handler := store.landingSubscriptionHandler()
	run := func(method, token string) *httptest.ResponseRecorder {
		t.Helper()
		if err := store.saveLandingController(context.Background(), state); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(method, "http://subscription.example.test/sub/"+token, nil)
		r.Header.Set("Authorization", "Bearer must-not-forward")
		r.Header.Set("Cookie", "session=must-not-forward")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	w := run(http.MethodGet, account.SubscriptionToken)
	plain, decodeErr := base64.StdEncoding.DecodeString(w.Body.String())
	if decodeErr != nil || w.Code != http.StatusOK || strings.Count(string(plain), "vless://") != 2 || !strings.HasPrefix(string(plain), native) || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Subscription-Userinfo") != "upload=0; download=150; total=1000; expire="+strconv.FormatInt(account.Expiry/1000, 10) {
		t.Fatalf("incorrect synthesized parent response: status=%d", w.Code)
	}
	length := w.Body.Len()
	w = run(http.MethodHead, account.SubscriptionToken)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != strconv.Itoa(length) {
		t.Fatal("HEAD did not describe the same subscription without sending credentials")
	}
	w = run(http.MethodGet, "another-sub-token")
	plain, decodeErr = base64.StdEncoding.DecodeString(w.Body.String())
	if decodeErr != nil || w.Code != http.StatusOK || strings.Contains(string(plain), "aaaaaaaa-bbbb") || !strings.Contains(string(plain), "another-user") {
		t.Fatal("another account received a combination identity")
	}
	w = run(http.MethodGet, "private-child-token")
	if w.Code != http.StatusNotFound {
		t.Fatal("child subscription bypassed the parent account")
	}
	for _, test := range []struct {
		name   string
		change func(*landingControllerAccount)
	}{
		{"disabled", func(a *landingControllerAccount) { a.Enabled = false }},
		{"blocked", func(a *landingControllerAccount) { a.Blocked = true }},
		{"deleted", func(a *landingControllerAccount) { a.Deleted = true }},
		{"pending", func(a *landingControllerAccount) { a.PendingOperation = "mutation" }},
		{"expired", func(a *landingControllerAccount) { a.Expiry = 1 }},
		{"out-of-traffic", func(a *landingControllerAccount) { a.Total = 150 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := account
			test.change(&changed)
			state.Accounts[parent] = changed
			w := run(http.MethodGet, account.SubscriptionToken)
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "vless://") {
				t.Fatal("unavailable parent reached native subscription or leaked material")
			}
		})
	}
	state.Accounts[parent] = account
	grant := state.Grants["grant-a"]
	grant.Phase = "revoked"
	state.Grants["grant-a"] = grant
	w = run(http.MethodGet, account.SubscriptionToken)
	plain, decodeErr = base64.StdEncoding.DecodeString(w.Body.String())
	if decodeErr != nil || w.Code != http.StatusOK || string(plain) != native {
		t.Fatal("revoked grant remains published or base subscription was removed")
	}
}

func TestLandingSubscriptionHandlerFailsClosedOnUpstreamAndJournalErrors(t *testing.T) {
	store, state, parent := landingSubscriptionTestState(t)
	empty := state.Subscriptions[parent]
	empty.Links = nil
	state.Subscriptions[parent] = empty
	if err := store.saveLandingController(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	store.landingSubscriptionHandler().ServeHTTP(w, httptest.NewRequest("GET", "/sub/"+state.Accounts[parent].SubscriptionToken, nil))
	if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "vless://") {
		t.Fatal("account without an applied route published subscription material")
	}
	broken := state.Subscriptions[parent]
	broken.Links = []string{"not a native subscription"}
	state.Subscriptions[parent] = broken
	if err := store.saveLandingController(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	store.landingSubscriptionHandler().ServeHTTP(w, httptest.NewRequest("GET", "/sub/"+state.Accounts[parent].SubscriptionToken, nil))
	if w.Code < 400 || strings.Contains(w.Body.String(), "vless://") {
		t.Fatal("invalid native output produced a usable subscription")
	}
	if _, err := store.db.Exec(`UPDATE landing_controller_state SET sealed_state=x'00' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	store.landingSubscriptionHandler().ServeHTTP(w, httptest.NewRequest("GET", "/sub/parent-sub-token", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("corrupt journal did not fail closed")
	}
}
