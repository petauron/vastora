package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestThreeXUINodeReconcileUsesControllerNodeAPI(t *testing.T) {
	applicationID := "worker-application"
	desiredName := threeXUINodeAPIName(applicationID)
	worker := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer worker-token" || request.URL.Path != "/panel/api/server/status" {
			t.Fatalf("unexpected worker request: %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":{"xray":{"state":"running"}}}`))
	}))
	defer worker.Close()
	workerAddress, workerPortValue, err := net.SplitHostPort(strings.TrimPrefix(worker.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	workerPort, err := strconv.Atoi(workerPortValue)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer master-token" {
			t.Fatalf("controller token = %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/nodes/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
		case "POST /panel/api/nodes/add":
			var payload struct {
				Name                string `json:"name"`
				Scheme              string `json:"scheme"`
				Address             string `json:"address"`
				Port                int    `json:"port"`
				APIToken            string `json:"apiToken"`
				AllowPrivateAddress bool   `json:"allowPrivateAddress"`
				InboundSyncMode     string `json:"inboundSyncMode"`
			}
			if json.NewDecoder(request.Body).Decode(&payload) != nil || payload.Name != desiredName || payload.Scheme != "http" || payload.Address != workerAddress || payload.Port != workerPort || payload.APIToken != "worker-token" || !payload.AllowPrivateAddress || payload.InboundSyncMode != "all" {
				t.Fatalf("unexpected node payload: %#v", payload)
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{"id":7,"name":"` + desiredName + `","address":"` + workerAddress + `","port":` + workerPortValue + `,"status":"online"}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "master-token")
	defer store.Close()

	result, err := applyThreeXUINodeCommand(context.Background(), store, ThreeXUINodeCommandTask{Action: "reconcile", WorkerApplicationID: applicationID, Name: "edge-2", Address: workerAddress, Port: workerPort, APIToken: "worker-token"})
	if err != nil {
		t.Fatal(err)
	}
	if result.RemoteNodeID != 7 || result.Status != "ready" {
		t.Fatalf("node result = %#v", result)
	}
}

func TestThreeXUINodeRemovalDeletesRemoteInboundsFirst(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		response.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "GET /panel/api/inbounds/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":12,"nodeId":7},{"id":13,"nodeId":null}]}`))
		case "POST /panel/api/inbounds/del/12", "POST /panel/api/nodes/del/7":
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	store := threeXUIClientTestStore(t, server, "master-token")
	defer store.Close()

	result, err := applyThreeXUINodeCommand(context.Background(), store, ThreeXUINodeCommandTask{Action: "remove", WorkerApplicationID: "worker-application", RemoteNodeID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "stopped" || len(requests) != 3 || requests[1] != "POST /panel/api/inbounds/del/12" || requests[2] != "POST /panel/api/nodes/del/7" {
		t.Fatalf("removal result=%#v requests=%#v", result, requests)
	}
}
