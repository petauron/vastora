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
	"time"
)

func TestThreeXUIClientListReturnsOnlySafeMetadata(t *testing.T) {
	var clientListCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer local-token" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/panel/api/clients/list/paged":
			clientListCalls.Add(1)
			_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"MacBook","subId":"private-sub-id","enable":true,"totalGB":10737418240,"expiryTime":0,"reset":30,"limitIp":2,"inboundIds":[9],"traffic":{"up":1024,"down":2048}}],"total":1}}`))
		case "/panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":9,"tag":"vastora-node","enable":true,"protocol":"vless","up":2048,"down":4096,"total":21474836480,"streamSettings":{"security":"reality"}}]}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	result, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "list", Inbounds: []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9", ConnectHostname: "reality.example.test", PlanStatus: "active"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Clients) != 1 || result.Clients[0].Email != "MacBook" || result.Clients[0].UsedBytes != 3072 || result.Clients[0].ResetDays != 30 || !result.Clients[0].HasSubscription || result.Secret != "" || !result.InboundsObserved || len(result.Inbounds) != 1 || result.Inbounds[0].TotalBytes != 20*gibibyteForTest || result.Inbounds[0].UsedBytes != 6144 || result.Inbounds[0].InboundTag != "vastora-node" {
		t.Fatalf("unexpected safe client projection: %#v", result)
	}
	encoded, _ := json.Marshal(result)
	if string(encoded) == "" || containsAny(string(encoded), "private-sub-id", "uuid", "password") {
		t.Fatalf("client metadata leaked a credential: %s", encoded)
	}
	inboundOnly, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "list_inbounds", Inbounds: []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9", ConnectHostname: "reality.example.test", PlanStatus: "active"}}})
	if err != nil || !inboundOnly.InboundsObserved || inboundOnly.ClientsObserved || clientListCalls.Load() != 1 {
		t.Fatalf("inbound-only projection=%#v client list calls=%d err=%v", inboundOnly, clientListCalls.Load(), err)
	}
}

func TestThreeXUIClientRevealsPublishedRealityAndSubscriptionLinks(t *testing.T) {
	updatedSubID := ""
	var updatedSettings map[string]any
	var syncedHost threeXUIHostGroup
	var clientVersionUpdated atomic.Bool
	var clientVersionUpdateCount atomic.Int32
	var restartPending atomic.Bool
	var restartCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/clients/get/MacBook":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"client":{"email":"MacBook","id":"11111111-2222-4333-8444-555555555555","subId":"","flow":"xtls-rprx-vision","enable":true},"inboundIds":[9]}}`))
		case "GET /panel/api/inbounds/get/9":
			minClientVersion := ""
			proxySettings := ""
			if clientVersionUpdated.Load() {
				minClientVersion = threeXUIRealityMinClientVersion
				proxySettings = `,"tcpSettings":{"acceptProxyProtocol":true},"sockopt":{"acceptProxyProtocol":true}`
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{"id":9,"enable":true,"remark":"inbound-9","protocol":"vless","listen":"100.64.0.1","port":39871,"total":0,"expiryTime":0,"settings":{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"MacBook","flow":"xtls-rprx-vision"}]},"streamSettings":{"network":"tcp","security":"reality"` + proxySettings + `,"realitySettings":{"serverNames":["www.example.com"],"shortIds":["deadbeef"],"minClientVer":"` + minClientVersion + `","settings":{"publicKey":"public-key"}}},"sniffing":{"enabled":true}}}`))
		case "POST /panel/api/inbounds/update/9":
			var payload map[string]any
			if json.NewDecoder(request.Body).Decode(&payload) != nil {
				t.Fatal("compatible Reality inbound update was not decoded")
			}
			streamSettings, _ := payload["streamSettings"].(map[string]any)
			realitySettings, _ := streamSettings["realitySettings"].(map[string]any)
			if realitySettings["minClientVer"] != threeXUIRealityMinClientVersion || !realityStreamAcceptsProxyProtocol(streamSettings) {
				t.Fatalf("minimum Reality client version = %#v", realitySettings["minClientVer"])
			}
			clientVersionUpdateCount.Add(1)
			clientVersionUpdated.Store(true)
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/clients/update/MacBook":
			var payload map[string]json.RawMessage
			if json.NewDecoder(request.Body).Decode(&payload) != nil || json.Unmarshal(payload["subId"], &updatedSubID) != nil || updatedSubID == "" {
				t.Fatal("subscription id was not generated in the full client payload")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/hosts/byInbound/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
		case "POST /panel/api/hosts/add":
			if json.NewDecoder(request.Body).Decode(&syncedHost) != nil {
				t.Fatal("Reality subscription host was not decoded")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
		case "POST /panel/api/setting/all":
			if restartPending.Swap(false) {
				response.WriteHeader(http.StatusServiceUnavailable)
				_, _ = response.Write([]byte(`{"success":false}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{"subEnable":true,"subPath":"/sub/","subClashEnable":false,"remarkTemplate":"{{INBOUND}}-{{EMAIL}}"}}`))
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
	inbounds := []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9", ConnectHostname: "reality.example.test", SNIHostname: "www.example.com"}}

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
	if syncedHost.GroupID != "vastora-public-9" || len(syncedHost.InboundIDs) != 1 || syncedHost.InboundIDs[0] != 9 || len(syncedHost.Hosts) != 1 || syncedHost.Hosts[0] != "reality.example.test" || syncedHost.Port != 443 || syncedHost.SNI != "www.example.com" || syncedHost.Security != "same" {
		t.Fatalf("public Reality endpoint was not synchronized into subscriptions: %#v", syncedHost)
	}
	if updatedSettings["subClashEnable"] != true || updatedSettings["subClashPath"] != "/clash/" || updatedSettings["subClashAutoDetect"] != true || updatedSettings["subClashUserAgentRegex"] != `(?i)(clash|mihomo)` {
		t.Fatalf("Clash/Mihomo output was not enabled: %#v", updatedSettings)
	}
	if updatedSettings["remarkTemplate"] != "{{INBOUND}}" {
		t.Fatalf("subscription node names still include client identity: %#v", updatedSettings["remarkTemplate"])
	}
	if restartCount.Load() != 1 {
		t.Fatalf("3x-ui restart count = %d, want 1", restartCount.Load())
	}
	if clientVersionUpdateCount.Load() != 1 {
		t.Fatalf("Reality compatibility update count = %d, want 1", clientVersionUpdateCount.Load())
	}
}

func TestThreeXUIClientCreateAndUpdateUseFirstClassClientAPI(t *testing.T) {
	createdID, createdSubID := "", ""
	attached, detached := []int{}, []int{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "POST /panel/api/clients/add":
			var payload struct {
				Client     map[string]json.RawMessage `json:"client"`
				InboundIDs []int                      `json:"inboundIds"`
			}
			var resetDays int
			if json.NewDecoder(request.Body).Decode(&payload) != nil || json.Unmarshal(payload.Client["id"], &createdID) != nil || json.Unmarshal(payload.Client["subId"], &createdSubID) != nil || json.Unmarshal(payload.Client["reset"], &resetDays) != nil || resetDays != 30 || createdID == "" || createdSubID == "" || len(payload.InboundIDs) != 2 || payload.InboundIDs[0] != 9 || payload.InboundIDs[1] != 10 {
				t.Fatal("create did not use generated first-class client credentials")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/get/Phone":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"client":{"email":"Phone","id":"preserved-uuid","subId":"preserved-sub-id","comment":"keep-me","enable":true,"totalGB":0,"expiryTime":0,"limitIp":0},"inboundIds":[9,10]}}`))
		case "POST /panel/api/clients/update/Phone":
			var payload map[string]json.RawMessage
			var email, id, subID, comment string
			var total int64
			var resetDays int
			if json.NewDecoder(request.Body).Decode(&payload) != nil || json.Unmarshal(payload["email"], &email) != nil || json.Unmarshal(payload["id"], &id) != nil || json.Unmarshal(payload["subId"], &subID) != nil || json.Unmarshal(payload["comment"], &comment) != nil || json.Unmarshal(payload["totalGB"], &total) != nil || json.Unmarshal(payload["reset"], &resetDays) != nil || email != "Phone 2" || id != "preserved-uuid" || subID != "preserved-sub-id" || comment != "keep-me" || total != 20*gibibyteForTest || resetDays != 30 {
				t.Fatal("update did not preserve the complete 3x-ui client payload")
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/clients/Phone 2/attach":
			var payload struct {
				InboundIDs []int `json:"inboundIds"`
			}
			if json.NewDecoder(request.Body).Decode(&payload) != nil {
				t.Fatal("attach payload was not decoded")
			}
			attached = payload.InboundIDs
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/clients/Phone 2/detach":
			var payload struct {
				InboundIDs []int `json:"inboundIds"`
			}
			if json.NewDecoder(request.Body).Decode(&payload) != nil {
				t.Fatal("detach payload was not decoded")
			}
			detached = payload.InboundIDs
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
	inbounds := []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9"}, {ID: 10, Name: "inbound-10"}, {ID: 11, Name: "inbound-11"}}
	expiry := time.Now().Add(90 * 24 * time.Hour).UnixMilli()
	if _, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "create", NewEmail: "Phone", InboundIDs: []int{9, 10}, Enabled: true, ResetDays: 30, ExpiryTime: expiry, Inbounds: inbounds}); err != nil {
		t.Fatal(err)
	}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "update", Email: "Phone", NewEmail: "Phone 2", InboundIDs: []int{10, 11}, TotalBytes: 20 * gibibyteForTest, ResetDays: 30, ExpiryTime: expiry, Inbounds: inbounds}); err != nil {
		t.Fatal(err)
	}
	if len(attached) != 1 || attached[0] != 11 || len(detached) != 1 || detached[0] != 9 {
		t.Fatalf("attachment changes = attach %#v, detach %#v", attached, detached)
	}
}

func TestThreeXUIClientCreateRecoversLostResponseWithoutDuplicate(t *testing.T) {
	created := false
	addCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/clients/list/paged":
			if created {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"Phone","subId":"stable-sub","enable":true,"totalGB":0,"expiryTime":0,"reset":0,"limitIp":0,"inboundIds":[9],"traffic":{"up":0,"down":0}}],"total":1}}`))
			} else {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[],"total":0}}`))
			}
		case "POST /panel/api/clients/add":
			addCalls++
			created = true
			response.WriteHeader(http.StatusBadGateway)
			_, _ = response.Write([]byte(`{"success":false,"msg":"response lost"}`))
		case "GET /panel/api/clients/get/Phone":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"client":{"email":"Phone","id":"11111111-2222-4333-8444-555555555555","subId":"stable-sub","flow":"xtls-rprx-vision","enable":true,"totalGB":0,"expiryTime":0,"reset":0,"limitIp":0},"inboundIds":[9]}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	result, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{
		Action: "create", NewEmail: "Phone", InboundIDs: []int{9}, Enabled: true,
		Inbounds: []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if addCalls != 1 || !result.ClientsObserved || len(result.Clients) != 1 || result.Clients[0].Email != "Phone" {
		t.Fatalf("recovered client result=%#v add calls=%d", result, addCalls)
	}
}

func TestThreeXUIClientRenameRetryContinuesFromNewName(t *testing.T) {
	attached, detached := []int{}, []int{}
	updatePaths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/clients/get/Phone":
			response.WriteHeader(http.StatusNotFound)
			_, _ = response.Write([]byte(`{"success":false,"msg":"not found"}`))
		case "GET /panel/api/clients/get/Phone 2":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"client":{"email":"Phone 2","id":"preserved-uuid","subId":"preserved-sub-id","enable":true,"totalGB":21474836480,"expiryTime":0,"reset":0,"limitIp":0},"inboundIds":[9]}}`))
		case "POST /panel/api/clients/update/Phone 2":
			updatePaths = append(updatePaths, request.URL.Path)
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/clients/Phone 2/attach":
			var payload struct {
				InboundIDs []int `json:"inboundIds"`
			}
			_ = json.NewDecoder(request.Body).Decode(&payload)
			attached = payload.InboundIDs
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/clients/Phone 2/detach":
			var payload struct {
				InboundIDs []int `json:"inboundIds"`
			}
			_ = json.NewDecoder(request.Body).Decode(&payload)
			detached = payload.InboundIDs
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/list/paged":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"Phone 2","subId":"preserved-sub-id","enable":true,"totalGB":21474836480,"expiryTime":0,"reset":0,"limitIp":0,"inboundIds":[10],"traffic":{"up":0,"down":0}}],"total":1}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	_, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{
		Action: "update", Email: "Phone", NewEmail: "Phone 2", InboundIDs: []int{10}, Enabled: true, TotalBytes: 20 * gibibyteForTest,
		Inbounds: []ThreeXUIClientInbound{{ID: 9, Name: "inbound-9"}, {ID: 10, Name: "inbound-10"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updatePaths) != 1 || updatePaths[0] != "/panel/api/clients/update/Phone 2" || len(attached) != 1 || attached[0] != 10 || len(detached) != 1 || detached[0] != 9 {
		t.Fatalf("rename retry update=%#v attach=%#v detach=%#v", updatePaths, attached, detached)
	}
}

func TestThreeXUIClientDeleteRecoversLostResponse(t *testing.T) {
	exists := true
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/clients/list/paged":
			if exists {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"Phone","subId":"sub","enable":true,"totalGB":0,"expiryTime":0,"reset":0,"limitIp":0,"inboundIds":[9],"traffic":{"up":0,"down":0}}],"total":1}}`))
			} else {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[],"total":0}}`))
			}
		case "POST /panel/api/clients/del/Phone":
			deleteCalls++
			exists = false
			response.WriteHeader(http.StatusBadGateway)
			_, _ = response.Write([]byte(`{"success":false,"msg":"response lost"}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	result, err := applyThreeXUIClientCommand(context.Background(), store, ThreeXUIClientCommandTask{Action: "delete", Email: "Phone"})
	if err != nil {
		t.Fatal(err)
	}
	if deleteCalls != 1 || !result.ClientsObserved || len(result.Clients) != 0 {
		t.Fatalf("delete recovery result=%#v calls=%d", result, deleteCalls)
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
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "3x-install", AppKey: threeXUIKey, Version: "3.7.0", Config: config, Secrets: secrets, ServiceAddress: host}); err != nil {
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
