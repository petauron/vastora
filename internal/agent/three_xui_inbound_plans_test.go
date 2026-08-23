package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestThreeXUIInboundPlanUpdatePreservesManualDisabledState(t *testing.T) {
	var update map[string]any
	total := int64(1000)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/9":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": map[string]any{"id": 9, "tag": "vastora-node", "enable": false, "protocol": "vless", "up": 1000, "down": 0, "total": total, "trafficReset": "never", "trafficResetDay": 1, "streamSettings": map[string]any{"security": "reality"}, "customField": "keep"}})
			_, _ = response.Write(encoded)
		case "POST /panel/api/inbounds/update/9":
			if json.NewDecoder(request.Body).Decode(&update) != nil {
				t.Fatal("inbound plan update was not decoded")
			}
			total = int64(update["total"].(float64))
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":9,"tag":"vastora-node","enable":false,"protocol":"vless","up":1000,"down":0,"total":2000,"streamSettings":{"security":"reality"}}]}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()
	command := ThreeXUIClientCommandTask{
		Action: "update_inbound", ServiceID: "service-9", InboundID: 9,
		InboundTotalBytes: 2000, InboundTag: "vastora-node",
		Inbounds: []ThreeXUIClientInbound{{ID: 9, ServiceID: "service-9", InboundTag: "vastora-node", PlanStatus: "active"}},
	}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, command); err != nil {
		t.Fatal(err)
	}
	if update["enable"] != false || update["total"] != float64(2000) || update["trafficReset"] != "never" || update["trafficResetDay"] != float64(1) || update["customField"] != "keep" {
		t.Fatalf("manual disabled state or inbound payload changed: %#v", update)
	}
}

func TestThreeXUIInboundPlanUpdateRestoresAppliedResetBeforeReadingEnabledState(t *testing.T) {
	enabled := false
	total := int64(1000)
	var update map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		inbound := func() map[string]any {
			return map[string]any{"id": 9, "tag": "vastora-node", "enable": enabled, "protocol": "vless", "up": 0, "down": 0, "total": total, "trafficReset": "never", "trafficResetDay": 1, "streamSettings": map[string]any{"security": "reality"}}
		}
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/9":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": inbound()})
			_, _ = response.Write(encoded)
		case "GET /panel/api/inbounds/list":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": []map[string]any{inbound()}})
			_, _ = response.Write(encoded)
		case "POST /panel/api/inbounds/setEnable/9":
			var body struct {
				Enable bool `json:"enable"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			enabled = body.Enable
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/inbounds/update/9":
			if json.NewDecoder(request.Body).Decode(&update) != nil {
				t.Fatal("inbound update was not decoded")
			}
			total = int64(update["total"].(float64))
			enabled = update["enable"].(bool)
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()
	boundary := "2026-09-01T00:00:00Z"
	operationKey := threeXUIResetOperationKey("service-9", boundary)
	if _, _, err := store.beginThreeXUIReset(context.Background(), operationKey, "service-9", boundary, 3, 9, "vastora-node", 1000, true); err != nil {
		t.Fatal(err)
	}
	if err := store.transitionThreeXUIReset(context.Background(), operationKey, "disable_started", "disabled", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.markThreeXUIResetApplied(context.Background(), operationKey, "disabled", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.markThreeXUIResetRecovery(context.Background(), operationKey, "restore_pending_applied", "simulated lost restore"); err != nil {
		t.Fatal(err)
	}
	command := ThreeXUIClientCommandTask{
		Action: "update_inbound", ServiceID: "service-9", InboundID: 9,
		InboundTotalBytes: 2000, InboundTag: "vastora-node",
		Inbounds: []ThreeXUIClientInbound{{ID: 9, ServiceID: "service-9", InboundTag: "vastora-node", PlanStatus: "failed"}},
	}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, command); err != nil {
		t.Fatal(err)
	}
	journal, err := scanThreeXUIResetJournal(store.db.QueryRow(`SELECT operation_key, service_id, expected_next_reset_at, plan_revision, target_inbound_id, target_inbound_tag, sync_used_bytes, desired_enabled, status, last_error FROM three_x_ui_reset_journal WHERE operation_key = ?`, operationKey))
	if err != nil {
		t.Fatal(err)
	}
	if !enabled || update["enable"] != true || journal.Status != "cancelled_applied" {
		t.Fatalf("enabled=%v update=%#v journal=%#v", enabled, update, journal)
	}
}

func TestThreeXUIInboundScheduledResetIsJournaledExactlyOnce(t *testing.T) {
	var resetCount atomic.Int32
	var enableCount atomic.Int32
	enabled := false
	used := int64(1000)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/9":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": map[string]any{"id": 9, "tag": "vastora-node", "enable": enabled, "protocol": "vless", "up": used, "down": 0, "total": 1000, "trafficReset": "never", "trafficResetDay": 1, "streamSettings": map[string]any{"security": "reality"}}})
			_, _ = response.Write(encoded)
		case "POST /panel/api/inbounds/9/resetTraffic":
			resetCount.Add(1)
			used = 0
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/inbounds/setEnable/9":
			enableCount.Add(1)
			var body struct {
				Enable bool `json:"enable"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			enabled = body.Enable
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/inbounds/list":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": []map[string]any{{"id": 9, "tag": "vastora-node", "enable": enabled, "protocol": "vless", "up": used, "down": 0, "total": 1000, "streamSettings": map[string]any{"security": "reality"}}}})
			_, _ = response.Write(encoded)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()
	boundary := "2026-09-01T00:00:00Z"
	command := ThreeXUIClientCommandTask{
		Action: "reset_inbound_plan", ServiceID: "service-9", InboundID: 9,
		InboundTotalBytes: 1000, ExpectedNextResetAt: boundary, PlanRevision: 3,
		OperationKey: threeXUIResetOperationKey("service-9", boundary), InboundTag: "vastora-node",
		Inbounds: []ThreeXUIClientInbound{{ID: 9, ServiceID: "service-9", InboundTag: "vastora-node", TotalBytes: 1000, ResetDays: 30, NextResetAt: boundary, PlanStatus: "resetting"}},
	}
	for range 2 {
		if _, err := applyThreeXUIClientCommand(context.Background(), store, command); err != nil {
			t.Fatal(err)
		}
	}
	if resetCount.Load() != 1 || enableCount.Load() != 1 || !enabled {
		t.Fatalf("reset=%d enabled-state writes=%d enabled=%v", resetCount.Load(), enableCount.Load(), enabled)
	}
}

func TestWorkerInboundPlanUpdateRepairsMissedRemotePushByTag(t *testing.T) {
	workerTotal := int64(1000)
	workerEnabled := false
	var workerUpdate map[string]any
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":9,"tag":"vastora-worker","enable":false,"protocol":"vless","total":1000,"trafficReset":"never","trafficResetDay":1,"streamSettings":{"security":"reality"}}]}`))
		case "GET /panel/api/inbounds/get/9":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": map[string]any{"id": 9, "tag": "vastora-worker", "enable": workerEnabled, "protocol": "vless", "up": 1000, "down": 0, "total": workerTotal, "trafficReset": "never", "trafficResetDay": 1, "streamSettings": map[string]any{"security": "reality"}, "customField": "keep"}})
			_, _ = response.Write(encoded)
		case "POST /panel/api/inbounds/update/9":
			if json.NewDecoder(request.Body).Decode(&workerUpdate) != nil {
				t.Fatal("worker inbound update was not decoded")
			}
			workerTotal = int64(workerUpdate["total"].(float64))
			workerEnabled = workerUpdate["enable"].(bool)
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected worker request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer worker.Close()
	workerURL, _ := url.Parse(worker.URL)
	workerPort, _ := strconv.Atoi(workerURL.Port())
	centralTotal := int64(1000)
	centralEnabled := true
	central := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/90":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": map[string]any{"id": 90, "nodeId": 7, "tag": "n7-vastora-worker", "enable": centralEnabled, "protocol": "vless", "up": 1000, "down": 0, "total": centralTotal, "trafficReset": "never", "trafficResetDay": 1, "streamSettings": map[string]any{"security": "reality"}}})
			_, _ = response.Write(encoded)
		case "POST /panel/api/inbounds/update/90":
			var update map[string]any
			if json.NewDecoder(request.Body).Decode(&update) != nil {
				t.Fatal("central inbound update was not decoded")
			}
			centralTotal = int64(update["total"].(float64))
			centralEnabled = update["enable"].(bool)
			// Deliberately do not propagate to the worker; the Agent must repair it.
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/inbounds/list":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": []map[string]any{{"id": 90, "nodeId": 7, "tag": "n7-vastora-worker", "enable": centralEnabled, "protocol": "vless", "up": 1000, "down": 0, "total": centralTotal, "streamSettings": map[string]any{"security": "reality"}}}})
			_, _ = response.Write(encoded)
		default:
			t.Fatalf("unexpected central request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer central.Close()
	store := threeXUIClientTestStore(t, central, "central-token")
	defer store.Close()
	command := ThreeXUIClientCommandTask{
		Action: "update_inbound", ServiceID: "service-worker", InboundID: 90,
		InboundTotalBytes: 2000, InboundTag: "n7-vastora-worker", TargetNodeID: 7,
		TargetAddress: workerURL.Hostname(), TargetPanelPort: workerPort, TargetAPIToken: "worker-token",
		Inbounds: []ThreeXUIClientInbound{{ID: 90, ServiceID: "service-worker", InboundTag: "n7-vastora-worker", PlanStatus: "active"}},
	}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, command); err != nil {
		t.Fatal(err)
	}
	if centralTotal != 2000 || workerTotal != 2000 || centralEnabled || workerEnabled || workerUpdate["customField"] != "keep" {
		t.Fatalf("central total/enabled=%d/%v worker=%d/%v payload=%#v", centralTotal, centralEnabled, workerTotal, workerEnabled, workerUpdate)
	}
}

func TestWorkerScheduledResetUsesLocalIDAndWaitsForCentralProjection(t *testing.T) {
	workerUsed := int64(1000)
	workerEnabled := false
	var workerResets atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":9,"tag":"vastora-worker","enable":false,"protocol":"vless","total":1000,"streamSettings":{"security":"reality"}}]}`))
		case "GET /panel/api/inbounds/get/9":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": map[string]any{"id": 9, "tag": "vastora-worker", "enable": workerEnabled, "protocol": "vless", "up": workerUsed, "down": 0, "total": 1000, "trafficReset": "never", "trafficResetDay": 1, "streamSettings": map[string]any{"security": "reality"}}})
			_, _ = response.Write(encoded)
		case "POST /panel/api/inbounds/9/resetTraffic":
			workerResets.Add(1)
			workerUsed = 0
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/inbounds/setEnable/9":
			var body struct {
				Enable bool `json:"enable"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			workerEnabled = body.Enable
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected worker request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer worker.Close()
	workerURL, _ := url.Parse(worker.URL)
	workerPort, _ := strconv.Atoi(workerURL.Port())
	var centralGets atomic.Int32
	centralEnabled := false
	central := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/90":
			used := int64(1000)
			if centralGets.Add(1) >= 3 {
				used = 0
			}
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": map[string]any{"id": 90, "nodeId": 7, "tag": "n7-vastora-worker", "enable": centralEnabled, "protocol": "vless", "up": used, "down": 0, "total": 1000, "trafficReset": "never", "trafficResetDay": 1, "streamSettings": map[string]any{"security": "reality"}}})
			_, _ = response.Write(encoded)
		case "POST /panel/api/inbounds/setEnable/90":
			var body struct {
				Enable bool `json:"enable"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			centralEnabled = body.Enable
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/inbounds/list":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": []map[string]any{{"id": 90, "nodeId": 7, "tag": "n7-vastora-worker", "enable": centralEnabled, "protocol": "vless", "up": 0, "down": 0, "total": 1000, "streamSettings": map[string]any{"security": "reality"}}}})
			_, _ = response.Write(encoded)
		default:
			if strings.Contains(request.URL.Path, "resetTraffic") {
				t.Fatal("central reset endpoint must not be called for a worker inbound")
			}
			t.Fatalf("unexpected central request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer central.Close()
	store := threeXUIClientTestStore(t, central, "central-token")
	defer store.Close()
	boundary := "2026-09-01T00:00:00Z"
	command := ThreeXUIClientCommandTask{
		Action: "reset_inbound_plan", ServiceID: "service-worker", InboundID: 90,
		InboundTotalBytes: 1000, ExpectedNextResetAt: boundary, PlanRevision: 3,
		OperationKey: threeXUIResetOperationKey("service-worker", boundary), InboundTag: "n7-vastora-worker", TargetNodeID: 7,
		TargetAddress: workerURL.Hostname(), TargetPanelPort: workerPort, TargetAPIToken: "worker-token",
		Inbounds: []ThreeXUIClientInbound{{ID: 90, ServiceID: "service-worker", InboundTag: "n7-vastora-worker", TotalBytes: 1000, ResetDays: 30, NextResetAt: boundary, PlanStatus: "resetting"}},
	}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, command); err != nil {
		t.Fatal(err)
	}
	if workerResets.Load() != 1 || centralGets.Load() < 3 || !workerEnabled || !centralEnabled {
		t.Fatalf("worker resets=%d central gets=%d enabled worker/central=%v/%v", workerResets.Load(), centralGets.Load(), workerEnabled, centralEnabled)
	}
}

func TestScheduledResetPreservesManualDisabledDecisionAcrossRetry(t *testing.T) {
	used := int64(10)
	enabled := false
	var resetCount atomic.Int32
	var enableAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/get/9":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": map[string]any{"id": 9, "tag": "vastora-node", "enable": enabled, "protocol": "vless", "up": used, "down": 0, "total": 1000, "trafficReset": "never", "trafficResetDay": 1, "streamSettings": map[string]any{"security": "reality"}}})
			_, _ = response.Write(encoded)
		case "POST /panel/api/inbounds/9/resetTraffic":
			resetCount.Add(1)
			used = 0
			enabled = true // simulate drift after the reset; the journal must restore false
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "POST /panel/api/inbounds/setEnable/9":
			var body struct {
				Enable bool `json:"enable"`
			}
			_ = json.NewDecoder(request.Body).Decode(&body)
			if body.Enable {
				t.Fatal("manual disabled inbound was enabled by its reset plan")
			}
			if resetCount.Load() == 0 {
				enabled = false
				_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
				return
			}
			if enableAttempts.Add(1) == 1 {
				enabled = true // simulate state drift before the retry
				response.WriteHeader(http.StatusServiceUnavailable)
				_, _ = response.Write([]byte(`{"success":false,"msg":"temporary"}`))
				return
			}
			enabled = body.Enable
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "GET /panel/api/inbounds/list":
			encoded, _ := json.Marshal(map[string]any{"success": true, "obj": []map[string]any{{"id": 9, "tag": "vastora-node", "enable": enabled, "protocol": "vless", "up": used, "down": 0, "total": 1000, "streamSettings": map[string]any{"security": "reality"}}}})
			_, _ = response.Write(encoded)
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "local-token")
	defer store.Close()
	boundary := "2026-09-01T00:00:00Z"
	command := ThreeXUIClientCommandTask{
		Action: "reset_inbound_plan", ServiceID: "service-9", InboundID: 9,
		InboundTotalBytes: 1000, ExpectedNextResetAt: boundary, PlanRevision: 3,
		OperationKey: threeXUIResetOperationKey("service-9", boundary), InboundTag: "vastora-node",
		Inbounds: []ThreeXUIClientInbound{{ID: 9, ServiceID: "service-9", InboundTag: "vastora-node", TotalBytes: 1000, ResetDays: 30, NextResetAt: boundary, PlanStatus: "resetting"}},
	}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, command); err == nil {
		t.Fatal("first enable attempt unexpectedly succeeded")
	}
	if _, err := applyThreeXUIClientCommand(context.Background(), store, command); err != nil {
		t.Fatal(err)
	}
	if resetCount.Load() != 1 || enableAttempts.Load() != 2 || enabled {
		t.Fatalf("reset=%d enable attempts=%d enabled=%v", resetCount.Load(), enableAttempts.Load(), enabled)
	}
}

func TestCentralWorkerResetProjectionRejectsChangedTag(t *testing.T) {
	central := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":{"id":90,"nodeId":7,"tag":"n7-other","enable":false,"protocol":"vless","up":0,"down":0,"total":1000,"streamSettings":{"security":"reality"}}}`))
	}))
	defer central.Close()
	err := waitForThreeXUICentralResetSync(context.Background(), central.URL, "central-token", ThreeXUIClientCommandTask{InboundID: 90, TargetNodeID: 7, InboundTag: "n7-vastora-worker"}, 0)
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("changed central tag error = %v", err)
	}
}

func TestWorkerInboundTagNormalizationUsesRemotePrefixOnly(t *testing.T) {
	if got := normalizedThreeXUIInboundTag("n7-vastora-node", 7); got != "vastora-node" {
		t.Fatalf("normalized tag = %q", got)
	}
	if got := normalizedThreeXUIInboundTag("n8-vastora-node", 7); got != "n8-vastora-node" {
		t.Fatalf("unrelated node prefix was stripped: %q", got)
	}
}
