package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestThreeXUIRealityStreamSettingsUsesMihomoCompatibleVersion(t *testing.T) {
	stream := threeXUIRealityStreamSettings("www.example.test:443", "www.example.test", "private-key", "public-key", "deadbeef")
	reality, ok := stream["realitySettings"].(map[string]any)
	if !ok || reality["minClientVer"] != threeXUIMihomoMinClientVersion {
		t.Fatalf("REALITY minimum client version = %#v", reality["minClientVer"])
	}
}

func TestEnsureThreeXUIRealityClientVersionRepairsOnceAndPreservesPayload(t *testing.T) {
	compatible := false
	updates := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/9":
			minClientVersion := ""
			if compatible {
				minClientVersion = threeXUIMihomoMinClientVersion
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{"id":9,"enable":true,"remark":"keep-me","protocol":"vless","listen":"100.64.0.1","port":39871,"settings":{"clients":[{"id":"client-id","email":"MacBook"}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"target":"www.example.test:443","serverNames":["www.example.test"],"privateKey":"private-key","minClientVer":"` + minClientVersion + `","maxClientVer":"keep-max","shortIds":["deadbeef"],"settings":{"publicKey":"public-key"}}},"sniffing":{"enabled":true},"clientStats":[{"email":"MacBook"}],"customField":"preserve-me"}}`))
		case "POST /panel/api/inbounds/update/9":
			updates++
			var payload map[string]any
			if json.NewDecoder(request.Body).Decode(&payload) != nil {
				t.Fatal("updated inbound payload was not decoded")
			}
			streamSettings, _ := payload["streamSettings"].(map[string]any)
			realitySettings, _ := streamSettings["realitySettings"].(map[string]any)
			if realitySettings["minClientVer"] != threeXUIMihomoMinClientVersion || realitySettings["maxClientVer"] != "keep-max" || payload["remark"] != "keep-me" || payload["customField"] != "preserve-me" || payload["id"] != nil || payload["clientStats"] != nil {
				t.Fatalf("unexpected compatible inbound update: %#v", payload)
			}
			compatible = true
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	for range 2 {
		inbound, err := ensureThreeXUIRealityClientVersion(context.Background(), server.URL, "token", 9)
		if err != nil {
			t.Fatal(err)
		}
		var stream struct {
			Reality struct {
				MinClientVersion string `json:"minClientVer"`
			} `json:"realitySettings"`
		}
		if json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Reality.MinClientVersion != threeXUIMihomoMinClientVersion {
			t.Fatalf("returned minimum client version = %q", stream.Reality.MinClientVersion)
		}
	}
	if updates != 1 {
		t.Fatalf("compatible inbound updates = %d, want 1", updates)
	}
}

func TestSyncThreeXUIRealityHostUpdatesOnlyManagedGroup(t *testing.T) {
	var updated threeXUIHostGroup
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/hosts/byInbound/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"groupId":"user-host","inboundIds":[9],"hosts":["user.example.test"],"remark":"User host","port":8443,"security":"same","sni":"www.example.test"},{"groupId":"vastora-public-9","inboundIds":[9],"hosts":["old.example.test"],"remark":"{{INBOUND}}-{{EMAIL}}","serverDescription":"Managed by Vastora","tags":["vastora"],"port":443,"security":"same","sni":"www.example.test","fingerprint":"chrome","mihomoIpVersion":"dual"}]}`))
		case "POST /panel/api/hosts/update/vastora-public-9":
			if json.NewDecoder(request.Body).Decode(&updated) != nil {
				t.Fatal("managed Reality host update was not decoded")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	if err := syncThreeXUIRealityHost(context.Background(), server.URL, "token", 9, "reality.example.test", "www.example.test"); err != nil {
		t.Fatal(err)
	}
	if updated.GroupID != "vastora-public-9" || len(updated.InboundIDs) != 1 || updated.InboundIDs[0] != 9 || len(updated.Hosts) != 1 || updated.Hosts[0] != "reality.example.test" || updated.Port != 443 || updated.SNI != "www.example.test" {
		t.Fatalf("unexpected managed Reality host: %#v", updated)
	}
}

func TestSyncThreeXUIRealityHostKeepsMatchingGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/panel/api/hosts/byInbound/9" {
			t.Fatalf("matching host should not be rewritten: %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":[{"groupId":"vastora-public-9","inboundIds":[9],"hosts":["reality.example.test"],"remark":"{{INBOUND}}-{{EMAIL}}","serverDescription":"Managed by Vastora","tags":["vastora"],"port":443,"security":"same","sni":"www.example.test","fingerprint":"chrome","mihomoIpVersion":"dual"}]}`))
	}))
	defer server.Close()

	if err := syncThreeXUIRealityHost(context.Background(), server.URL, "token", 9, "reality.example.test", "www.example.test"); err != nil {
		t.Fatal(err)
	}
}

func TestAttachAllThreeXUIClientsToInboundKeepsClientIdentity(t *testing.T) {
	var emails []string
	var inboundIDs []int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/clients/list/paged":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"MacBook","inboundIds":[7]},{"email":"Router","inboundIds":[7,9]}],"total":2}}`))
		case "POST /panel/api/clients/bulkAttach":
			var payload struct {
				Emails     []string `json:"emails"`
				InboundIDs []int    `json:"inboundIds"`
			}
			if json.NewDecoder(request.Body).Decode(&payload) != nil {
				t.Fatal("bulk attachment payload was not decoded")
			}
			emails, inboundIDs = payload.Emails, payload.InboundIDs
			_, _ = response.Write([]byte(`{"success":true,"obj":{"attached":["MacBook"],"skipped":[],"errors":[]}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	if err := attachAllThreeXUIClientsToInbound(context.Background(), server.URL, "token", 9); err != nil {
		t.Fatal(err)
	}
	if len(emails) != 1 || emails[0] != "MacBook" || len(inboundIDs) != 1 || inboundIDs[0] != 9 {
		t.Fatalf("automatic attachment = emails %#v, inbounds %#v", emails, inboundIDs)
	}
}

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
