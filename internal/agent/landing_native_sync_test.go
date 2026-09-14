package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLandingNativeWriteWaitsForAllNodesWithoutRepeatingMutation(t *testing.T) {
	var writes, scopeReads, node7Reads, node8Reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer native-token" {
			t.Error("missing native API authentication")
		}
		var obj any
		switch r.Method + " " + r.URL.Path {
		case "GET /panel/api/inbounds/get/9", "GET /panel/api/inbounds/get/10":
			if writes.Load() != 0 {
				t.Error("write scope must be resolved before detachment")
			}
			scopeReads.Add(1)
			id, nodeID := 9, 7
			if strings.HasSuffix(r.URL.Path, "/10") {
				id, nodeID = 10, 8
			}
			obj = map[string]any{"id": id, "nodeId": nodeID}
		case "POST /panel/api/clients/child/detach":
			if scopeReads.Load() != 2 {
				t.Error("did not pin the original inbound scope")
			}
			writes.Add(1)
			obj = map[string]any{"nodePending": true}
		case "GET /panel/api/nodes/get/7":
			node7Reads.Add(1)
			obj = map[string]any{"id": 7, "enable": true, "status": "online", "configDirty": false}
		case "GET /panel/api/nodes/get/8":
			read := node8Reads.Add(1)
			// Online alone must not confirm the write; wait for dirty to clear.
			obj = map[string]any{"id": 8, "enable": true, "status": "online", "configDirty": read == 1}
		default:
			t.Error("unexpected request", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": obj})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := writeLandingNativeChange(ctx, server.URL, "native-token", server.URL+"/panel/api/clients/child/detach", map[string]any{"inboundIds": []int{9, 10}}, []int{10, 9, 9}); err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 || node7Reads.Load() != 2 || node8Reads.Load() != 2 {
		t.Fatal("write was replayed or a pending node was not confirmed", writes.Load(), node7Reads.Load(), node8Reads.Load())
	}
}

func TestLandingNativeSyncFailsClosedWithoutRetryingErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		obj  any
	}{
		{"read-error", http.StatusServiceUnavailable, nil},
		{"missing-confirmation", http.StatusOK, map[string]any{"id": 7, "enable": true, "status": "online"}},
		{"wrong-node", http.StatusOK, map[string]any{"id": 8, "enable": true, "status": "online", "configDirty": false}},
		{"disabled", http.StatusOK, map[string]any{"id": 7, "enable": false, "status": "online", "configDirty": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/panel/api/nodes/get/7" {
					t.Error("sync confirmation must only read the intended node")
				}
				reads.Add(1)
				w.WriteHeader(tc.code)
				_ = json.NewEncoder(w).Encode(map[string]any{"success": tc.code == http.StatusOK, "obj": tc.obj})
			}))
			defer server.Close()
			if err := waitLandingNativeSync(context.Background(), server.URL, "native-token", []int{7}); err == nil || reads.Load() != 1 {
				t.Fatal("error was accepted or retried", err, reads.Load())
			}
		})
	}
}

func TestLandingNativeSyncHonorsCancellationAndDeadline(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			var reads atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reads.Add(1)
				if !deadline {
					cancel()
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "obj": map[string]any{"id": 7, "enable": true, "status": "offline", "configDirty": true}})
			}))
			defer server.Close()
			if err := waitLandingNativeSync(ctx, server.URL, "native-token", []int{7}); !errors.Is(err, want) || reads.Load() > 1 {
				t.Fatal("wait ignored task lifetime or kept polling", err, reads.Load())
			}
		})
	}
	if err := waitLandingNativeSync(context.Background(), "", "", nil); err == nil {
		t.Fatal("an empty attachment list cannot confirm a pending detach")
	}
}

func TestLandingNativeWriteNeverRetriesRejectedMutation(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Error("rejected write must not start confirmation")
		}
		writes.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	if err := writeLandingNativeChange(context.Background(), server.URL, "native-token", server.URL+"/update", nil, nil); err == nil || writes.Load() != 1 {
		t.Fatal("rejected mutation was accepted or retried", err, writes.Load())
	}
}
