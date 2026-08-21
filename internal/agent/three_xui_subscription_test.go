package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestApplySubscriptionCommandUpdatesOnlyPublicAddressSettings(t *testing.T) {
	var updated map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer local-api-token" {
			t.Fatalf("missing API token: %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/panel/api/setting/all":
			_, _ = response.Write([]byte(`{"success":true,"obj":{"subListen":"100.64.0.10","subPort":2096,"subPath":"/sub/"}}`))
		case "/panel/api/setting/update":
			if err := json.NewDecoder(request.Body).Decode(&updated); err != nil {
				t.Fatal(err)
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{
		InstanceID:     "three-x-ui",
		AppKey:         threeXUIKey,
		Version:        "3.6.0",
		Config:         json.RawMessage(`{"timezone":"UTC","panel_port":` + strconv.Itoa(port) + `}`),
		Secrets:        json.RawMessage(`{"api_token":"local-api-token"}`),
		ServiceAddress: host,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := applySubscriptionCommand(context.Background(), store, SubscriptionCommandTask{
		Domain:  "subscribe.example.com",
		BaseURI: "https://subscribe.example.com/sub/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Domain != "subscribe.example.com" || result.BaseURI != "https://subscribe.example.com/sub/" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if updated["subDomain"] != "subscribe.example.com" || updated["subURI"] != "https://subscribe.example.com/sub/" {
		t.Fatalf("public subscription address was not written: %#v", updated)
	}
	if updated["subEnable"] != true || updated["subPath"] != "/sub/" {
		t.Fatalf("public subscription endpoint was not enabled: %#v", updated)
	}
	if updated["subListen"] != "100.64.0.10" || updated["subPath"] != "/sub/" {
		t.Fatalf("existing private subscription settings were changed: %#v", updated)
	}
}
