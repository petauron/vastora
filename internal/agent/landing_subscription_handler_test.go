package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

type landingSubscriptionTestTransport func(*http.Request) (*http.Response, error)

func (f landingSubscriptionTestTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

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
	grant := landing.ClientGrant{ID: "grant-a", ParentID: parent, BaseIdentity: parent, BaseUser: "Phone", InboundTag: "business", FixedUser: landing.FixedUser("grant-a"), FixedIdentity: landing.Identity(childUUID), Peer: landing.PeerIdentity{ID: "landing-a", PublicKey: "key-a", Address: "100.64.0.9"}, Mode: landing.BothMode, Enabled: true}
	link := func(id string) string {
		return "vless://" + id + "@entry.example.test:443?type=tcp&security=reality&flow=xtls-rprx-vision&sni=example.com&pbk=public-key&sid=deadbeef#Original"
	}
	state := &landingControllerState{ControllerID: "controller", Grants: map[string]landingControllerGrant{
		grant.ID: {Task: landing.ControllerTask{Grant: grant, Revision: 1, Phase: "activate", ControllerID: "controller", InboundID: 9, FixedUUID: childUUID, EntryName: "Entry A", LandingName: "Landing A"}, Phase: "ready", ChildSubscription: "private-child-token", Material: landing.ControllerResult{BaseLink: link(parentUUID), FixedLink: link(childUUID)}},
	}, Accounts: map[string]landingControllerAccount{
		parent: {ID: parent, Email: "Phone", SubscriptionToken: "parent-sub-token", Mode: landing.BothMode, Enabled: true, Total: 1000, Expiry: store.now().Add(time.Hour).UnixMilli(), Members: []landing.QuotaMember{{ID: parent, Observed: 100, Active: true}, {ID: grant.FixedIdentity, Observed: 50, Active: true}}},
	}}
	return store, state, parent
}

func TestLandingSubscriptionHandlerScopesMaterialAndLifecycle(t *testing.T) {
	store, state, parent := landingSubscriptionTestState(t)
	native := state.Grants["grant-a"].Material.BaseLink + "\n"
	account := state.Accounts[parent]
	calls := 0
	client := &http.Client{Transport: landingSubscriptionTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "http" || r.URL.Host != "100.64.0.8:2096" || r.Method != http.MethodGet || r.Host != "subscription.example.test" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("subscription request escaped the fixed native boundary")
		}
		if r.URL.Path != "/sub/parent-sub-token" && r.URL.Path != "/sub/another-sub-token" {
			t.Fatalf("unexpected native path %q", r.URL.Path)
		}
		body := native
		if r.URL.Path == "/sub/another-sub-token" {
			body = "vless://another-user@another.example.test:443?security=reality\n"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/plain"}, "Subscription-Userinfo": {"upload=999; download=999; total=999"}, "Set-Cookie": {"must-not-forward=value"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	handler := store.landingSubscriptionHandler(client)
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
	if w.Code != http.StatusOK || strings.Count(w.Body.String(), "vless://") != 2 || !strings.HasPrefix(w.Body.String(), native) || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Subscription-Userinfo") != "upload=0; download=150; total=1000; expire="+strconv.FormatInt(account.Expiry/1000, 10) {
		t.Fatalf("incorrect synthesized parent response: status=%d", w.Code)
	}
	length := w.Body.Len()
	w = run(http.MethodHead, account.SubscriptionToken)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != strconv.Itoa(length) {
		t.Fatal("HEAD did not describe the same subscription without sending credentials")
	}
	w = run(http.MethodGet, "another-sub-token")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "aaaaaaaa-bbbb") || !strings.Contains(w.Body.String(), "another-user") {
		t.Fatal("another account received a combination identity")
	}
	before := calls
	w = run(http.MethodGet, "private-child-token")
	if w.Code != http.StatusNotFound || calls != before {
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
			before := calls
			w := run(http.MethodGet, account.SubscriptionToken)
			if w.Code != http.StatusNotFound || calls != before || strings.Contains(w.Body.String(), "vless://") {
				t.Fatal("unavailable parent reached native subscription or leaked material")
			}
		})
	}
	state.Accounts[parent] = account
	grant := state.Grants["grant-a"]
	grant.Phase = "revoked"
	state.Grants["grant-a"] = grant
	w = run(http.MethodGet, account.SubscriptionToken)
	if w.Code != http.StatusOK || w.Body.String() != native {
		t.Fatal("revoked grant remains published or base subscription was removed")
	}
}

func TestLandingSubscriptionHandlerFailsClosedOnUpstreamAndJournalErrors(t *testing.T) {
	store, state, parent := landingSubscriptionTestState(t)
	if err := store.saveLandingController(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"not a native subscription", strings.Repeat("x", landingSubscriptionMaxBytes+1)} {
		client := &http.Client{Transport: landingSubscriptionTestTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		w := httptest.NewRecorder()
		store.landingSubscriptionHandler(client).ServeHTTP(w, httptest.NewRequest("GET", "/sub/"+state.Accounts[parent].SubscriptionToken, nil))
		if w.Code < 400 || strings.Contains(w.Body.String(), "vless://") {
			t.Fatal("invalid native output produced a usable subscription")
		}
	}
	if _, err := store.db.Exec(`UPDATE landing_controller_state SET sealed_state=x'00' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: landingSubscriptionTestTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("corrupt journal must not fall back to native credentials")
		return nil, nil
	})}
	w := httptest.NewRecorder()
	store.landingSubscriptionHandler(client).ServeHTTP(w, httptest.NewRequest("GET", "/sub/parent-sub-token", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatal("corrupt journal did not fail closed")
	}
}
