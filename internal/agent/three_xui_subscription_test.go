package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRestartThreeXUIPanelAcceptsInProcessReload(t *testing.T) {
	var restartCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/panel/api/setting/restartPanel":
			restartCount.Add(1)
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "/panel/api/setting/all":
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	if err := restartThreeXUIPanel(context.Background(), server.URL, "local-api-token", 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if restartCount.Load() != 1 {
		t.Fatalf("3x-ui restart count = %d, want 1", restartCount.Load())
	}
}

func TestRetiredSubscriptionCommandCannotChangeManagedInstallation(t *testing.T) {
	var completed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if acknowledgeExecutionForTest(t, w, r) {
			return
		}
		if r.URL.Path != "/api/v1/agents/agent-1/tasks/retired-subscription/result" {
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var result struct {
			Succeeded bool
			Error     string
			Result    ApplicationTaskResult
		}
		if json.NewDecoder(r.Body).Decode(&result) != nil || result.Succeeded || !strings.Contains(result.Error, "unsupported application command") || result.Result.SubscriptionCommand != nil {
			t.Error("retired subscription command did not fail closed")
		}
		completed.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"completed":true}`))
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SaveConnection(ctx, testConnection(t, "agent-1", "node", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	original := AppliedInstallation{InstanceID: "three-x-ui", ApplicationID: "controller", AppKey: threeXUIKey, Version: "3.7.0", Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053}`), Secrets: json.RawMessage(`{"api_token":"unused"}`), ServiceAddress: "100.64.0.10"}
	if _, err := store.RecordApplied(ctx, original); err != nil {
		t.Fatal(err)
	}
	client := Client{HTTPClient: server.Client(), Capabilities: Capabilities{Docker: true}, execution: executionAuthorizationForTest(), executionSession: "test-execution-session"}
	var reported error
	if err := client.processTask(ctx, store, DeploymentTask{Kind: "application.command", ID: "retired-subscription", Attempt: 1, SubscriptionCommand: &SubscriptionCommandTask{Domain: "subscribe.example.com", BaseURI: "https://subscribe.example.com/sub/"}}, func(err error) { reported = err }); err != nil {
		t.Fatal(err)
	}
	if reported == nil || !strings.Contains(reported.Error(), "unsupported application command") || completed.Load() != 1 {
		t.Fatal("retired command was not reported as a failure")
	}
	current, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil || current.ApplicationID != original.ApplicationID || current.Version != original.Version || string(current.Config) != string(original.Config) || string(current.Secrets) != string(original.Secrets) {
		t.Fatal("retired command changed the managed installation")
	}
}
