package agent

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestLandingSubscriptionRequestCannotBecomeAnOpenProxy(t *testing.T) {
	for _, path := range []string{"/management", "/sub/short", "/sub/abcdefgh/more", "/sub/abcdefgh?url=https://other.example", "/sub/abcdefgh%2Fmore", "/clash/abcdefgh?target=anything", "/clash/abcdefgh?format=landing-fragment"} {
		request := httptest.NewRequest("GET", path, nil)
		if _, _, ok := landingSubscriptionRequest(request); ok {
			t.Fatalf("accepted unsafe path %s", path)
		}
	}
	for _, test := range []struct {
		path, ua string
		clash    bool
	}{{"/sub/abcdefgh", "", false}, {"/sub/abcdefgh", "Mihomo", true}, {"/clash/abcdefgh", "", true}} {
		r := httptest.NewRequest("GET", test.path, nil)
		r.Header.Set("User-Agent", test.ua)
		token, clash, ok := landingSubscriptionRequest(r)
		if !ok || token != "abcdefgh" || clash != test.clash {
			t.Fatal("incorrect native format selection")
		}
	}
	if sameSubscriptionToken("", "") || sameSubscriptionToken("abcdefgh", "abcdefgi") || !sameSubscriptionToken("abcdefgh", "abcdefgh") {
		t.Fatal("incorrect token comparison")
	}
}

func TestLandingNativeWriteDistinguishesPendingFromInvalidResponses(t *testing.T) {
	for _, raw := range []string{`{}`, `{"nodePending":null}`, `{"nodePending":"false"}`, `invalid`} {
		if _, err := landingNativeWritePending(json.RawMessage(raw)); err == nil {
			t.Fatal("invalid worker response accepted")
		}
	}
	for _, raw := range []string{`null`, `{"nodePending":false}`} {
		if pending, err := landingNativeWritePending(json.RawMessage(raw)); err != nil || pending {
			t.Fatal("synchronous write was not recognized", pending, err)
		}
	}
	if pending, err := landingNativeWritePending(json.RawMessage(`{"nodePending":true}`)); err != nil || !pending {
		t.Fatal("deferred success must wait, not fail", pending, err)
	}
}
