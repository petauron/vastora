package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func installRealityGuardTestSeams(t *testing.T) {
	t.Helper()
	previousVerifier := realityTargetVerifier
	previousCompanion := realityCompanionEnsurer
	previousRouting := realityRoutingEnsurer
	previousHardener := realityInboundHardener
	realityTargetVerifier = func(context.Context, string, string, string) (realityTargetVerification, error) {
		return realityTargetVerification{TargetHost: "www.example.com", TargetIP: "203.0.113.10", ServerName: "www.example.com", NodeASN: 64500, TargetASN: 64500, TLS13: true, X25519: true, HTTP2: true, CertificateValid: true}, nil
	}
	realityCompanionEnsurer = func(_ context.Context, _ string, _ string, _ int, realityPort int, inboundTag, _ string) (threeXUIRealityInbound, error) {
		port := 21000
		if realityPort >= threeXUIRealityPortFirst && realityPort <= threeXUIRealityPortLast {
			port += realityPort - threeXUIRealityPortFirst
		}
		return threeXUIRealityInbound{ID: 99, Tag: realityGuardTag(inboundTag), Port: port, Protocol: "tunnel", Listen: "127.0.0.1", Enable: true}, nil
	}
	realityRoutingEnsurer = func(context.Context, string, string, string, string) error { return nil }
	realityInboundHardener = func(_ context.Context, _ string, _ string, inbound threeXUIRealityInbound, _ int, inboundTag string, _ realityTargetVerification) (threeXUIRealityInbound, threeXUIRealityInbound, error) {
		inbound.Enable = true
		return inbound, threeXUIRealityInbound{ID: 99, Tag: realityGuardTag(inboundTag), Port: 21000, Protocol: "tunnel", Listen: "127.0.0.1", Enable: true}, nil
	}
	t.Cleanup(func() {
		realityTargetVerifier = previousVerifier
		realityCompanionEnsurer = previousCompanion
		realityRoutingEnsurer = previousRouting
		realityInboundHardener = previousHardener
	})
}

func TestThreeXUIRealityStreamSettingsUsesMinimumClientVersion(t *testing.T) {
	stream := threeXUIRealityStreamSettings("www.example.test:443", "www.example.test", "private-key", "public-key", "deadbeef")
	reality, ok := stream["realitySettings"].(map[string]any)
	settings, _ := reality["settings"].(map[string]any)
	if !ok || reality["minClientVer"] != threeXUIRealityMinClientVersion || reality["maxClientVer"] != "" || reality["maxTimediff"] != 0 || settings["spiderX"] != "/" {
		t.Fatalf("REALITY anti-fingerprinting settings = %#v", reality)
	}
}

func TestUncertainRealityRecoveryQuarantinesAtRetryLimit(t *testing.T) {
	cause := errors.New("cleanup outcome is unknown")
	if err := deferUncertainRealityTask(1, cause); !taskCompletionShouldBeDeferred(err, 1) || !errors.Is(err, cause) {
		t.Fatalf("first uncertain recovery error = %v", err)
	}
	if err := deferUncertainRealityTask(maxDeferredTaskAttempts, cause); taskCompletionShouldBeDeferred(err, maxDeferredTaskAttempts) || !taskCompletionRequiresReconciliation(err, maxDeferredTaskAttempts) || !errors.Is(err, cause) {
		t.Fatalf("persistent recovery error = %v", err)
	}
}

func TestFindRealityInboundNeverMatchesAnotherTagByClientName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":9,"tag":"vastora-other-command","protocol":"vless","settings":{"clients":[{"email":"vastora-client-same-command"}]}}]}`))
	}))
	defer server.Close()
	_, found, err := findRealityInbound(context.Background(), server.URL, "token", "vastora-expected-command", 0)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("an unrelated REALITY inbound was matched only because it shared a client name")
	}
}

func TestEnsureThreeXUIRealityMinimumClientVersionRepairsOnceAndPreservesPayload(t *testing.T) {
	minimumApplied := false
	updates := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/9":
			minClientVersion := ""
			if minimumApplied {
				minClientVersion = threeXUIRealityMinClientVersion
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
			if realitySettings["minClientVer"] != threeXUIRealityMinClientVersion || realitySettings["maxClientVer"] != "keep-max" || payload["remark"] != "keep-me" || payload["customField"] != "preserve-me" || payload["id"] != nil || payload["clientStats"] != nil {
				t.Fatalf("unexpected minimum-version inbound update: %#v", payload)
			}
			minimumApplied = true
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	for range 2 {
		inbound, err := ensureThreeXUIRealityMinimumClientVersion(context.Background(), server.URL, "token", 9)
		if err != nil {
			t.Fatal(err)
		}
		var stream struct {
			Reality struct {
				MinClientVersion string `json:"minClientVer"`
			} `json:"realitySettings"`
		}
		if json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Reality.MinClientVersion != threeXUIRealityMinClientVersion {
			t.Fatalf("returned minimum client version = %q", stream.Reality.MinClientVersion)
		}
	}
	if updates != 1 {
		t.Fatalf("compatible inbound updates = %d, want 1", updates)
	}
}

func TestRenameThreeXUIRealityInboundPreservesConfiguration(t *testing.T) {
	updates := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"id":9,"enable":true,"remark":"vastora-reality-old","protocol":"vless","listen":"100.64.0.2","port":39871,"nodeId":7,"settings":{"clients":[{"id":"client-id","email":"MacBook","flow":"xtls-rprx-vision"}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"target":"www.example.test:443","serverNames":["www.example.test"],"privateKey":"private-key","shortIds":["deadbeef"],"settings":{"publicKey":"public-key"}}},"sniffing":{"enabled":true},"clientStats":[{"email":"MacBook"}],"customField":"preserve-me"}}`))
		case "POST /panel/api/inbounds/update/9":
			updates++
			var payload map[string]any
			if json.NewDecoder(request.Body).Decode(&payload) != nil {
				t.Fatal("renamed inbound payload was not decoded")
			}
			settings, _ := payload["settings"].(map[string]any)
			stream, _ := payload["streamSettings"].(map[string]any)
			if payload["remark"] != "US Oracle" || payload["listen"] != "100.64.0.2" || payload["port"] != float64(39871) || payload["nodeId"] != float64(7) || payload["customField"] != "preserve-me" || settings == nil || stream["security"] != "reality" || payload["id"] != nil || payload["clientStats"] != nil {
				t.Fatalf("rename changed the inbound configuration: %#v", payload)
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	result, err := renameThreeXUIRealityInbound(context.Background(), server.URL, "token", RealityCommandTask{Action: "rename", DisplayName: "US Oracle", InboundID: 9, TargetNodeID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if updates != 1 || result.Action != "rename" || result.InboundID != 9 || result.DisplayName != "US Oracle" {
		t.Fatalf("unexpected rename result: %#v, updates=%d", result, updates)
	}
}

func TestApplyRealityRenameReconcilesLostUpdateResponseWithSameCommand(t *testing.T) {
	updated := false
	loseReadback := false
	updateCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/9":
			if loseReadback {
				loseReadback = false
				response.WriteHeader(http.StatusBadGateway)
				_, _ = response.Write([]byte(`{"success":false,"msg":"readback unavailable"}`))
				return
			}
			remark := "US old"
			trafficReset := ""
			trafficResetDay := 0
			if updated {
				remark = "US Oracle"
				trafficReset = "never"
				trafficResetDay = 1
			}
			_, _ = response.Write([]byte(fmt.Sprintf(`{"success":true,"obj":{"id":9,"enable":true,"remark":%q,"protocol":"vless","listen":"100.64.0.2","port":39871,"trafficReset":%q,"trafficResetDay":%d,"settings":{"clients":[]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"target":"www.example.test:443","serverNames":["www.example.test"],"privateKey":"private-key","shortIds":["deadbeef"],"settings":{"publicKey":"public-key"}}}}}`, remark, trafficReset, trafficResetDay)))
		case "POST /panel/api/inbounds/update/9":
			updateCalls++
			updated = true
			loseReadback = true
			response.WriteHeader(http.StatusBadGateway)
			_, _ = response.Write([]byte(`{"success":false,"msg":"response lost"}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()
	command := RealityCommandTask{Action: "rename", DisplayName: "US Oracle", InboundID: 9}

	if _, err := applyRealityCommand(context.Background(), store, "application-command-rename-lost", 1, command); !taskCompletionShouldBeDeferred(err, 1) {
		t.Fatalf("lost rename response was not deferred for same-ID reconciliation: %v", err)
	}
	result, err := applyRealityCommand(context.Background(), store, "application-command-rename-lost", 2, command)
	if err != nil || result.Action != "rename" || result.DisplayName != "US Oracle" || updateCalls != 1 {
		t.Fatalf("replayed rename result=%#v updates=%d err=%v", result, updateCalls, err)
	}
}

func TestApplyRealityRenameMissingTargetFailsWithoutReconciliation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.Method != http.MethodGet || request.URL.Path != "/panel/api/inbounds/get/9" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		response.WriteHeader(http.StatusNotFound)
		_, _ = response.Write([]byte(`{"success":false,"msg":"not found"}`))
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	_, err := applyRealityCommand(context.Background(), store, "application-command-rename-missing", maxDeferredTaskAttempts, RealityCommandTask{Action: "rename", DisplayName: "US Oracle", InboundID: 9})
	if err == nil || taskCompletionShouldBeDeferred(err, maxDeferredTaskAttempts) || taskCompletionRequiresReconciliation(err, maxDeferredTaskAttempts) {
		t.Fatalf("missing rename target should be terminal, got %v", err)
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
	if updated.GroupID != "vastora-public-9" || updated.Remark != "{{INBOUND}}" || len(updated.InboundIDs) != 1 || updated.InboundIDs[0] != 9 || len(updated.Hosts) != 1 || updated.Hosts[0] != "reality.example.test" || updated.Port != 443 || updated.SNI != "www.example.test" {
		t.Fatalf("unexpected managed Reality host: %#v", updated)
	}
}

func TestSyncThreeXUIRealityHostKeepsMatchingGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/panel/api/hosts/byInbound/9" {
			t.Fatalf("matching host should not be rewritten: %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":[{"groupId":"vastora-public-9","inboundIds":[9],"hosts":["reality.example.test"],"remark":"{{INBOUND}}","serverDescription":"Managed by Vastora","tags":["vastora"],"port":443,"security":"same","sni":"www.example.test","fingerprint":"chrome","mihomoIpVersion":"dual"}]}`))
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

func TestApplyRealityCommandCompensatesIncompleteCreation(t *testing.T) {
	installRealityGuardTestSeams(t)
	commandID := "application-command-compensate1234"
	clientEmail := threeXUIClientEmail("Phone", commandID)
	deletedInbound, deletedClient := false, false
	clientExists := true
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
		case "POST /panel/api/server/scanRealityTargets":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"target":"www.example.test:443","host":"www.example.test","feasible":true,"serverNames":["www.example.test"]}]}`))
		case "GET /panel/api/server/getNewX25519Cert":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"privateKey":"private-key","publicKey":"public-key"}}`))
		case "POST /panel/api/inbounds/add":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"id":9,"tag":"` + threeXUIRealityTag(commandID) + `"}}`))
		case "GET /panel/api/hosts/byInbound/9":
			response.WriteHeader(http.StatusBadGateway)
			_, _ = response.Write([]byte(`{"success":false,"msg":"host sync failed"}`))
		case "POST /panel/api/inbounds/del/9":
			deletedInbound = true
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/list/paged":
			if clientExists {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"` + clientEmail + `","subId":"sub","enable":true,"totalGB":0,"expiryTime":0,"reset":0,"limitIp":0,"inboundIds":[9],"traffic":{"up":0,"down":0}}],"total":1}}`))
			} else {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[],"total":0}}`))
			}
		case "POST /panel/api/clients/del/" + clientEmail:
			deletedClient = true
			clientExists = false
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	_, err = applyRealityCommand(context.Background(), store, commandID, 1, RealityCommandTask{
		Action: "create", DisplayName: "US node", ClientName: "Phone", ConnectHostname: "reality.example.test",
		TargetAddress: host, TargetPublicAddress: "198.51.100.10", TargetHost: "www.example.com", ServerName: "www.example.com", TargetPanelPort: port, TargetNodeID: 7, TargetAPIToken: "remote-token",
		CreateInitialClient: true, InboundTag: threeXUIRealityTag(commandID),
	})
	if err == nil || !strings.Contains(err.Error(), "subscription host") {
		t.Fatalf("creation error = %v", err)
	}
	if !deletedInbound || !deletedClient {
		t.Fatalf("compensation deleted inbound=%t client=%t", deletedInbound, deletedClient)
	}
}

func TestApplyRealityCommandRecoversLostAddResponse(t *testing.T) {
	installRealityGuardTestSeams(t)
	commandID := "application-command-lostresponse1234"
	tag := threeXUIRealityTag(commandID)
	created := false
	addCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			if !created {
				_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":9,"tag":"n7-` + tag + `","remark":"US node","protocol":"vless","listen":"100.64.0.2","port":31000,"nodeId":7,"total":0,"settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"target":"www.example.test:443","serverNames":["www.example.test"],"shortIds":["deadbeef"],"settings":{"publicKey":"public-key"}}}}]}`))
		case "POST /panel/api/server/scanRealityTargets":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"target":"www.example.test:443","host":"www.example.test","feasible":true,"serverNames":["www.example.test"]}]}`))
		case "GET /panel/api/server/getNewX25519Cert":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"privateKey":"private-key","publicKey":"public-key"}}`))
		case "POST /panel/api/inbounds/add":
			addCalls++
			created = true
			response.WriteHeader(http.StatusBadGateway)
			_, _ = response.Write([]byte(`{"success":false,"msg":"response lost"}`))
		case "GET /panel/api/hosts/byInbound/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
		case "POST /panel/api/hosts/add":
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/list/paged":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[],"total":0}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	result, err := applyRealityCommand(context.Background(), store, commandID, 1, RealityCommandTask{
		Action: "create", DisplayName: "US node", ConnectHostname: "reality.example.test",
		TargetAddress: host, TargetPublicAddress: "198.51.100.10", TargetHost: "www.example.com", ServerName: "www.example.com", TargetPanelPort: port, TargetNodeID: 7, TargetAPIToken: "remote-token", InboundTag: tag,
	})
	if err != nil {
		t.Fatal(err)
	}
	if addCalls != 1 || result.InboundID != 9 || result.InboundTag != "n7-"+tag || result.Port != 31000 {
		t.Fatalf("lost response recovery result=%#v add calls=%d", result, addCalls)
	}
}

func TestApplyRealityCommandRecreatesLostAddHalfStateWithInitialClient(t *testing.T) {
	installRealityGuardTestSeams(t)
	commandID := "application-command-lostclient1234"
	tag := threeXUIRealityTag(commandID)
	clientEmail := threeXUIClientEmail("Phone", commandID)
	created := false
	clientIncluded := false
	addCalls := 0
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		inbound := `{"id":9,"tag":"n7-` + tag + `","remark":"US node","protocol":"vless","listen":"100.64.0.2","port":31000,"nodeId":7,"total":0,"settings":{"clients":[],"decryption":"none"},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"target":"www.example.test:443","serverNames":["www.example.test"],"shortIds":["deadbeef"],"settings":{"publicKey":"public-key"}}}}`
		if clientIncluded {
			inbound = strings.Replace(inbound, `"clients":[]`, `"clients":[{"id":"client-id","email":"`+clientEmail+`"}]`, 1)
		}
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			if !created {
				_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":[` + inbound + `]}`))
		case "POST /panel/api/server/scanRealityTargets":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"target":"www.example.test:443","host":"www.example.test","feasible":true,"serverNames":["www.example.test"]}]}`))
		case "GET /panel/api/server/getNewX25519Cert":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"privateKey":"private-key","publicKey":"public-key"}}`))
		case "POST /panel/api/inbounds/add":
			addCalls++
			created = true
			clientIncluded = addCalls > 1
			if addCalls == 1 {
				response.WriteHeader(http.StatusBadGateway)
				_, _ = response.Write([]byte(`{"success":false,"msg":"response lost after inbound commit"}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{"id":9,"tag":"n7-` + tag + `"}}`))
		case "POST /panel/api/inbounds/del/9":
			deleteCalls++
			created = false
			clientIncluded = false
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/list/paged":
			if clientIncluded {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"` + clientEmail + `","subId":"sub","enable":true,"totalGB":0,"expiryTime":0,"reset":0,"limitIp":0,"inboundIds":[9],"traffic":{"up":0,"down":0}}],"total":1}}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[],"total":0}}`))
		case "GET /panel/api/hosts/byInbound/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
		case "POST /panel/api/hosts/add":
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/get/" + clientEmail:
			_, _ = response.Write([]byte(`{"success":true,"obj":{"client":{"email":"` + clientEmail + `"},"inboundIds":[9]}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	result, err := applyRealityCommand(context.Background(), store, commandID, 1, RealityCommandTask{
		Action: "create", DisplayName: "US node", ClientName: "Phone", ConnectHostname: "reality.example.test",
		TargetAddress: host, TargetPublicAddress: "198.51.100.10", TargetHost: "www.example.com", ServerName: "www.example.com", TargetPanelPort: port, TargetNodeID: 7, TargetAPIToken: "remote-token",
		CreateInitialClient: true, InboundTag: tag,
	})
	if err != nil {
		t.Fatal(err)
	}
	if addCalls != 2 || deleteCalls != 1 || !result.ClientCreated || result.ShareURI == "" {
		t.Fatalf("half-state recovery result=%#v add calls=%d delete calls=%d", result, addCalls, deleteCalls)
	}
}

func TestApplyRealityCommandRollsBackExpiredExistingInbound(t *testing.T) {
	installRealityGuardTestSeams(t)
	commandID := "application-command-expired-replay"
	tag := threeXUIRealityTag(commandID)
	clientEmail := threeXUIClientEmail("Phone", commandID)
	inbound := `{"id":9,"tag":"` + tag + `","remark":"US node","protocol":"vless","listen":"100.64.0.2","port":31000,"total":0,"trafficReset":"never","trafficResetDay":1,"settings":{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"` + clientEmail + `"}]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"target":"www.example.test:443","serverNames":["www.example.test"],"minClientVer":"1.8.2","maxClientVer":"","shortIds":["deadbeef"],"settings":{"publicKey":"public-key"}}}}`
	deleted := false
	clientExists := true
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[` + inbound + `]}`))
		case "GET /panel/api/inbounds/get/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":` + inbound + `}`))
		case "POST /panel/api/inbounds/del/9":
			deleted = true
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/list/paged":
			if clientExists {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"` + clientEmail + `","inboundIds":[9]}],"total":1}}`))
			} else {
				_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[],"total":0}}`))
			}
		case "POST /panel/api/clients/del/" + clientEmail:
			clientExists = false
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()
	store.now = func() time.Time { return time.Date(2026, time.August, 24, 0, 0, 0, 0, time.UTC) }

	_, err = applyRealityCommand(context.Background(), store, commandID, 2, RealityCommandTask{
		Action: "create", DisplayName: "US node", ClientName: "Phone", ConnectHostname: "reality.example.test",
		TargetAddress: host, TargetPublicAddress: "198.51.100.10", TargetHost: "www.example.com", ServerName: "www.example.com", TargetPanelPort: port, CreateInitialClient: true, InboundTag: tag,
		ClientResetDays: 30, ClientExpiryTime: store.now().Add(-time.Hour).UnixMilli(),
	})
	if err == nil || !strings.Contains(err.Error(), "parameters are invalid") {
		t.Fatalf("expired replay error = %v", err)
	}
	if !deleted || clientExists || taskCompletionShouldBeDeferred(err, 2) || taskCompletionRequiresReconciliation(err, 2) {
		t.Fatalf("expired replay deleted=%t clientExists=%t err=%v", deleted, clientExists, err)
	}
}

func TestApplyRealityCommandRollsBackKnownFailureAtRetryLimit(t *testing.T) {
	installRealityGuardTestSeams(t)
	commandID := "application-command-rollback-limit"
	tag := threeXUIRealityTag(commandID)
	deleted := false
	inbound := `{"id":9,"tag":"` + tag + `","remark":"US node","protocol":"vless","listen":"100.64.0.2","port":31000,"total":0,"trafficReset":"never","trafficResetDay":1,"settings":{"clients":[]},"streamSettings":{"network":"tcp","security":"reality","realitySettings":{"target":"www.example.test:443","serverNames":["www.example.test"],"minClientVer":"1.8.2","maxClientVer":"","shortIds":["deadbeef"],"settings":{"publicKey":"public-key"}}}}`
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[` + inbound + `]}`))
		case "GET /panel/api/inbounds/get/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":` + inbound + `}`))
		case "GET /panel/api/hosts/byInbound/9":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"groupId":"vastora-public-9","inboundIds":[9],"hosts":["reality.example.test"],"remark":"{{INBOUND}}","serverDescription":"Managed by Vastora","tags":["vastora"],"port":443,"security":"same","sni":"www.example.test","fingerprint":"chrome","mihomoIpVersion":"dual"}]}`))
		case "POST /panel/api/hosts/update/vastora-public-9":
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/clients/list/paged":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"items":[{"email":"Phone","inboundIds":[]}],"total":1}}`))
		case "POST /panel/api/clients/bulkAttach":
			response.WriteHeader(http.StatusBadGateway)
			_, _ = response.Write([]byte(`{"success":false,"msg":"attach failed"}`))
		case "POST /panel/api/inbounds/del/9":
			deleted = true
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()

	_, err = applyRealityCommand(context.Background(), store, commandID, maxDeferredTaskAttempts, RealityCommandTask{
		Action: "create", DisplayName: "US node", ConnectHostname: "reality.example.test",
		TargetAddress: host, TargetPublicAddress: "198.51.100.10", TargetHost: "www.example.com", ServerName: "www.example.com", TargetPanelPort: port, InboundTag: tag,
	})
	if err == nil || !strings.Contains(err.Error(), "attach existing clients") {
		t.Fatalf("retry-limit error = %v", err)
	}
	if !deleted || taskCompletionShouldBeDeferred(err, maxDeferredTaskAttempts) || taskCompletionRequiresReconciliation(err, maxDeferredTaskAttempts) {
		t.Fatalf("rollback deleted=%t deferred=%t reconciliation=%t err=%v", deleted, taskCompletionShouldBeDeferred(err, maxDeferredTaskAttempts), taskCompletionRequiresReconciliation(err, maxDeferredTaskAttempts), err)
	}
}

func TestInitialClientAttachesToExistingManagedWorkerReality(t *testing.T) {
	var attached []int
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":9,"tag":"vastora-master","protocol":"vless","streamSettings":{"security":"reality"}},{"id":90,"tag":"n7-vastora-worker","protocol":"vless","streamSettings":{"security":"reality"}},{"id":50,"tag":"manual","protocol":"vless","streamSettings":{"security":"reality"}}]}`))
		case "GET /panel/api/clients/get/Phone":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"client":{"email":"Phone"},"inboundIds":[9]}}`))
		case "POST /panel/api/clients/Phone/attach":
			var payload struct {
				InboundIDs []int `json:"inboundIds"`
			}
			if json.NewDecoder(request.Body).Decode(&payload) != nil {
				t.Fatal("initial client attachment payload was not decoded")
			}
			attached = payload.InboundIDs
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	if err := attachThreeXUIClientToAllManagedRealityInbounds(context.Background(), server.URL, "token", "Phone"); err != nil {
		t.Fatal(err)
	}
	if len(attached) != 1 || attached[0] != 90 {
		t.Fatalf("initial client attached to %#v", attached)
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
