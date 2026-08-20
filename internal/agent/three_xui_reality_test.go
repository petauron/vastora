package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSelectRealityTargetUsesNodeLocalFeasibilityAndSkipsUsedSNI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/panel/api/server/scanRealityTargets" || request.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if err := request.ParseForm(); err != nil || request.PostForm.Get("targets") != "" {
			t.Fatalf("scan was not submitted as an empty node-local candidate request: %v %#v", err, request.PostForm)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"success":true,"obj":[{"target":"used.example:443","host":"used.example","feasible":true,"serverNames":["used.example"]},{"target":"ready.example:443","host":"ready.example","feasible":true,"serverNames":["ready.example"]}]}`))
	}))
	defer server.Close()
	target, sni, err := selectRealityTarget(context.Background(), server.URL, "token", RealityCommandTask{ExcludedSNI: []string{"used.example"}})
	if err != nil {
		t.Fatal(err)
	}
	if target != "ready.example:443" || sni != "ready.example" {
		t.Fatalf("selected target = %q, SNI = %q", target, sni)
	}
}

func TestSelectRealityTargetValidatesCustomSNIFromCertificateNames(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/panel/api/server/scanRealityTarget" {
			t.Fatalf("unexpected request: %s", request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":{"target":"edge.example:443","host":"edge.example","feasible":true,"serverNames":["*.example"]}}`))
	}))
	defer server.Close()

	target, sni, err := selectRealityTarget(context.Background(), server.URL, "token", RealityCommandTask{Target: "edge.example:443", SNIHostname: "www.example"})
	if err != nil || target != "edge.example:443" || sni != "www.example" {
		t.Fatalf("custom target = %q, SNI = %q, err = %v", target, sni, err)
	}
	if _, _, err := selectRealityTarget(context.Background(), server.URL, "token", RealityCommandTask{Target: "edge.example:443", SNIHostname: "unrelated.test"}); err == nil {
		t.Fatal("custom SNI outside the target certificate names was accepted")
	}
}

func TestRealityShareURISeparatesConnectionHostnameFromSNI(t *testing.T) {
	value := realityShareURI("11111111-2222-4333-8444-555555555555", "reality.edge.site.example.com", "MacBook", "www.example.com", "public-key", "deadbeef")
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "reality.edge.site.example.com:443" || parsed.Query().Get("sni") != "www.example.com" || parsed.Query().Get("pbk") != "public-key" || !strings.HasSuffix(value, "#MacBook") {
		t.Fatalf("unexpected REALITY link: %s", value)
	}
}

func TestThreeXUIClientEmailIsSafeAndUnique(t *testing.T) {
	first := threeXUIClientEmail("My device / Mac", "application-command-abcdefgh12345678")
	second := threeXUIClientEmail("My device / Mac", "application-command-abcdefgh87654321")
	if first != "my-device-mac-12345678" || second != "my-device-mac-87654321" || first == second {
		t.Fatalf("client identifiers = %q and %q", first, second)
	}
}
