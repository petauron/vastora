package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestThreeXUIClientListReturnsOnlySafeMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/panel/api/clients/list/paged" || request.Header.Get("Authorization") != "Bearer local-token" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"MacBook","subId":"private-sub-id","enable":true,"totalGB":10737418240,"expiryTime":0,"limitIp":2,"inboundIds":[9],"traffic":{"up":1024,"down":2048}}],"total":1}}`))
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	result, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "list", Inbounds: []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9", ConnectHostname: "reality.example.test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Clients) != 1 || result.Clients[0].Email != "MacBook" || result.Clients[0].UsedBytes != 3072 || !result.Clients[0].HasSubscription || result.Secret != "" {
		t.Fatalf("unexpected safe client projection: %#v", result)
	}
	encoded, _ := json.Marshal(result)
	if string(encoded) == "" || containsAny(string(encoded), "private-sub-id", "uuid", "password") {
		t.Fatalf("client metadata leaked a credential: %s", encoded)
	}
}

func TestThreeXUIClientRevealsPublishedRealityAndSubscriptionLinks(t *testing.T) {
	updatedSubID := ""
	var updatedSettings map[string]any
	var restartPending atomic.Bool
	var restartCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/clients/get/MacBook":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"client":{"email":"MacBook","id":"11111111-2222-4333-8444-555555555555","subId":"","flow":"xtls-rprx-vision","enable":true},"inboundIds":[9]}}`))
		case "GET /panel/api/inbounds/get/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"id":9,"settings":{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"MacBook","flow":"xtls-rprx-vision"}]},"streamSettings":{"security":"reality","realitySettings":{"serverNames":["www.example.com"],"shortIds":["deadbeef"],"settings":{"publicKey":"public-key"}}}}}`))
		case "POST /panel/api/clients/update/MacBook":
			var payload map[string]json.RawMessage
			if json.NewDecoder(request.Body).Decode(&payload) != nil || json.Unmarshal(payload["subId"], &updatedSubID) != nil || updatedSubID == "" {
				t.Fatal("subscription id was not generated in the full client payload")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/setting/all":
			if restartPending.Swap(false) {
				response.WriteHeader(http.StatusServiceUnavailable)
				_, _ = response.Write([]byte(`{"success":false}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{"subEnable":true,"subPath":"/sub/","subClashEnable":false}}`))
		case "POST /panel/api/setting/update":
			if json.NewDecoder(request.Body).Decode(&updatedSettings) != nil {
				t.Fatal("Clash subscription settings were not decoded")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/setting/restartPanel":
			restartCount.Add(1)
			restartPending.Store(true)
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/list/paged":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[],"total":0}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()
	inbounds := []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9", ConnectHostname: "reality.example.test"}}

	link, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "reveal_link", Email: "MacBook", InboundID: 9, Inbounds: inbounds})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(link.Secret)
	if link.SecretKind != "client_link" || parsed.Host != "reality.example.test:443" || parsed.Query().Get("sni") != "www.example.com" {
		t.Fatalf("unexpected public REALITY link: %#v", link)
	}
	subscription, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "reveal_subscription", Email: "MacBook", Inbounds: inbounds, SubscriptionBaseURI: "https://subscription.example.test/sub/"})
	if err != nil {
		t.Fatal(err)
	}
	if subscription.SecretKind != "subscription" || subscription.Secret != "https://subscription.example.test/sub/"+updatedSubID {
		t.Fatalf("unexpected subscription link: %#v", subscription)
	}
	if updatedSettings["subClashEnable"] != true || updatedSettings["subClashPath"] != "/clash/" || updatedSettings["subClashAutoDetect"] != true || updatedSettings["subClashUserAgentRegex"] != `(?i)(clash|mihomo)` {
		t.Fatalf("Clash/Mihomo output was not enabled: %#v", updatedSettings)
	}
	if restartCount.Load() != 1 {
		t.Fatalf("3x-ui restart count = %d, want 1", restartCount.Load())
	}
}

func TestThreeXUIClientCreateAndUpdateUseFirstClassClientAPI(t *testing.T) {
	createdID, createdSubID := "", ""
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "POST /panel/api/clients/add":
			var payload struct {
				Client     map[string]json.RawMessage `json:"client"`
				InboundIDs []int                      `json:"inboundIds"`
			}
			if json.NewDecoder(request.Body).Decode(&payload) != nil || json.Unmarshal(payload.Client["id"], &createdID) != nil || json.Unmarshal(payload.Client["subId"], &createdSubID) != nil || createdID == "" || createdSubID == "" || len(payload.InboundIDs) != 1 || payload.InboundIDs[0] != 9 {
				t.Fatal("create did not use generated first-class client credentials")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/get/Phone":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"client":{"email":"Phone","id":"preserved-uuid","subId":"preserved-sub-id","comment":"keep-me","enable":true,"totalGB":0,"expiryTime":0,"limitIp":0},"inboundIds":[9]}}`))
		case "POST /panel/api/clients/update/Phone":
			var payload map[string]json.RawMessage
			var email, id, subID, comment string
			var total int64
			if json.NewDecoder(request.Body).Decode(&payload) != nil || json.Unmarshal(payload["email"], &email) != nil || json.Unmarshal(payload["id"], &id) != nil || json.Unmarshal(payload["subId"], &subID) != nil || json.Unmarshal(payload["comment"], &comment) != nil || json.Unmarshal(payload["totalGB"], &total) != nil || email != "Phone 2" || id != "preserved-uuid" || subID != "preserved-sub-id" || comment != "keep-me" || total != 20*gibibyteForTest {
				t.Fatal("update did not preserve the complete 3x-ui client payload")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/list/paged":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[],"total":0}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()
	inbounds := []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9"}}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "create", NewEmail: "Phone", InboundID: 9, Enabled: true, Inbounds: inbounds}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "update", Email: "Phone", NewEmail: "Phone 2", TotalBytes: 20 * gibibyteForTest, Inbounds: inbounds}); err != nil {
		t.Fatal(err)
	}
}

const gibibyteForTest int64 = 1024 * 1024 * 1024

func threeXUIClientTestStore(t *testing.T, server *httptest.Server, token string) *Store {
	t.Helper()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{"timezone": "UTC", "panel_port": port, "enable_fail2ban": true, "vmess_aead_forced": false})
	secrets, _ := json.Marshal(map[string]string{"api_token": token})
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "3x-install", AppKey: threeXUIKey, Version: "3.6.0", Config: config, Secrets: secrets, ServiceAddress: host}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
