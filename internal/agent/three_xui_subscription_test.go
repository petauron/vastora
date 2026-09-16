package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestApplySubscriptionCommandRequiresNativeEndpointWithoutChangingThreeXUI(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{
		InstanceID: "three-x-ui", ApplicationID: "controller", AppKey: threeXUIKey, Version: "3.7.0",
		Config: json.RawMessage(`{"timezone":"UTC","panel_port":2053}`), Secrets: json.RawMessage(`{"api_token":"unused"}`), ServiceAddress: "100.64.0.10",
	}); err != nil {
		t.Fatal(err)
	}
	command := SubscriptionCommandTask{Domain: "subscribe.example.com", BaseURI: "https://subscribe.example.com/sub/"}
	unavailableContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := applySubscriptionCommand(unavailableContext, store, command); err == nil {
		t.Fatal("unavailable native subscription endpoint was published")
	}
	store.landingSubscriptionMu.Lock()
	store.landingSubscriptionAddress = "100.64.0.10"
	store.landingSubscriptionMu.Unlock()
	result, err := applySubscriptionCommand(context.Background(), store, command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Domain != command.Domain || result.BaseURI != command.BaseURI {
		t.Fatalf("unexpected native subscription publication: %#v", result)
	}
}
