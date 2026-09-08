package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingRouteAPILostWriteResponseReconcilesAndRestores(t *testing.T) {
	const before = `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"}],"routing":{"domainStrategy":"AsIs","rules":[]},"stats":{"counter":9007199254740993}}`
	change, err := landing.PrepareRouteChange(json.RawMessage(before), 1, []string{"business"}, landing.PeerIdentity{ID: "peer", PublicKey: "key", Address: "100.64.0.8"})
	if err != nil {
		t.Fatal(err)
	}
	current, writes := json.RawMessage(before), 0
	var stateMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stateMu.Lock()
		defer stateMu.Unlock()
		if r.Header.Get("Authorization") != "Bearer local-token" {
			t.Error("missing local authorization")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/panel/api/xray/":
			nested, marshalErr := json.Marshal(map[string]any{"xraySetting": current, "outboundTestUrl": "https://example.com/check"})
			if marshalErr != nil {
				t.Error(marshalErr)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": string(nested)})
		case "/panel/api/xray/update":
			if r.ParseForm() != nil || r.Form.Get("outboundTestUrl") != "https://example.com/check" {
				t.Error("changed unrelated outbound test URL")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			current = json.RawMessage(r.Form.Get("xraySetting"))
			writes++
			// Model a committed write with an unsuccessful/lost reply.
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"success":false,"msg":"sensitive remote error"}`))
		default:
			t.Error("unexpected route")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	routes := threeXUILandingRoutes{baseURL: server.URL, token: "local-token"}
	for _, enable := range []bool{true, true, false, false} {
		if err := routes.Apply(context.Background(), change, enable); err != nil {
			t.Fatal(err)
		}
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if writes != 2 || !strings.Contains(string(current), "9007199254740993") {
		t.Fatal("retry repeated writes or rounded an unrelated number")
	}
	wantHash, _ := landing.RouteSettingsHash(change.Before)
	gotHash, _ := landing.RouteSettingsHash(current)
	if wantHash != gotHash {
		t.Fatal("disable did not restore the exact original settings")
	}
}

func TestLandingRouteAPIRefusesRedirectAndRedactsRemoteErrors(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	for _, redirect := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if redirect {
				http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				return
			}
			_, _ = w.Write([]byte(`{"success":false,"msg":"password=do-not-log"}`))
		}))
		routes := threeXUILandingRoutes{baseURL: server.URL, token: "do-not-forward"}
		_, _, err := routes.Read(context.Background())
		server.Close()
		if err == nil || strings.Contains(err.Error(), "do-not") || strings.Contains(err.Error(), "password") {
			t.Fatal("redirect/error accepted or secret leaked")
		}
	}
	if forwarded.Load() != 0 {
		t.Fatal("forwarded node credentials to a redirect target")
	}
}
