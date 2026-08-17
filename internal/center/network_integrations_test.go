package center

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCloudflareAPIErrorsDoNotExposeToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-cloudflare-token" {
			t.Fatalf("unexpected authorization header")
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusForbidden)
		_, _ = response.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"permission denied"}],"result":null}`))
	}))
	defer server.Close()
	client := cloudflareClient{accountID: "account", zoneID: "zone", token: "secret-cloudflare-token", baseURL: server.URL, http: server.Client()}
	_, err := client.createDNSRecord(context.Background(), "A", "app.example.com", "203.0.113.10", false)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Cloudflare API error was not preserved: %v", err)
	}
	if strings.Contains(err.Error(), "secret-cloudflare-token") {
		t.Fatalf("Cloudflare token leaked in error: %v", err)
	}
}

func TestHeadscaleJoinKeyIsOneHourAndSingleUse(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/preauthkey" || request.Method != http.MethodPost {
			t.Fatalf("unexpected Headscale request: %s %s", request.Method, request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"preAuthKey":{"key":"one-time-key"}}`))
	}))
	defer server.Close()
	client := headscaleClient{baseURL: server.URL, apiKey: "headscale-secret", http: server.Client()}
	expires := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	key, err := client.createPreAuthKey(context.Background(), "42", []string{"tag:vastora-agent"}, expires)
	if err != nil {
		t.Fatal(err)
	}
	if key != "one-time-key" || body["user"] != "42" || body["reusable"] != false || body["expiration"] != expires.Format(time.RFC3339) {
		t.Fatalf("unexpected Headscale pre-auth key request: key=%q body=%#v", key, body)
	}
}
