package center

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestErrorCodeUsesStableUserFacingCategories(t *testing.T) {
	tests := []struct {
		status  int
		message string
		want    string
	}{
		{http.StatusUnauthorized, "center: authentication required", "authentication_required"},
		{http.StatusConflict, "center: app already installed on node", "already_installed"},
		{http.StatusBadRequest, "center: DNS record center.example.com already exists with a different value", "dns_record_conflict"},
		{http.StatusBadGateway, "center: Cloudflare authorization failed", "cloudflare_error"},
		{http.StatusConflict, "center: gateway unavailable", "gateway_unavailable"},
		{http.StatusBadRequest, "center: invalid input", "invalid_request"},
		{http.StatusBadRequest, "center: disable node before deleting", "node_delete_requires_disabled"},
		{http.StatusBadRequest, "center: node still in use", "node_delete_in_use"},
		{http.StatusBadRequest, "center: node could not be deleted; check remaining dependencies", "node_delete_in_use"},
		{http.StatusInternalServerError, "center: database failed", "internal_error"},
		{http.StatusInternalServerError, "center: Cloudflare credential database failed", "internal_error"},
		{http.StatusForbidden, "center: Cloudflare client address is invalid", "forbidden"},
	}
	for _, test := range tests {
		if got := errorCode(test.status, test.message); got != test.want {
			t.Errorf("errorCode(%d, %q) = %q, want %q", test.status, test.message, got, test.want)
		}
	}
}

func TestHTTPErrorResponsesDoNotExposeInternalDetails(t *testing.T) {
	for _, test := range []struct {
		status int
		detail string
		code   string
	}{
		{http.StatusBadRequest, `center: decode JSON: unknown field "private_marker"`, "invalid_request"},
		{http.StatusInternalServerError, "center: SQLite /var/lib/private_marker/center.db failed", "internal_error"},
		{http.StatusBadGateway, "center: Cloudflare request https://private_marker.example/api?token=private_marker failed", "cloudflare_error"},
		{http.StatusUnauthorized, "center: session expired private_marker", "authentication_required"},
		{http.StatusForbidden, "center: missing Cloudflare private_marker identity", "forbidden"},
		{http.StatusConflict, "center: DNS record private_marker.example already exists", "dns_record_conflict"},
	} {
		response := httptest.NewRecorder()
		writeError(response, test.status, errors.New(test.detail))
		var body map[string]string
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != test.status || body["code"] != test.code || body["error"] != publicErrorMessage(test.code) || len(body) != 2 {
			t.Fatalf("unexpected error response: %d %v", response.Code, body)
		}
		for _, forbidden := range []string{"private_marker", "center:", "SQLite", "expired", "unknown field"} {
			if strings.Contains(response.Body.String(), forbidden) {
				t.Fatalf("error response disclosed %q: %s", forbidden, response.Body.String())
			}
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("error response must not be cached")
		}
	}
}

func TestPublicHealthResponsesDoNotExposeVersion(t *testing.T) {
	server := &Server{}
	for _, ready := range []bool{false, true} {
		server.startupReady.Store(ready)
		for _, path := range []string{"/healthz", "/readyz"} {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			if path == "/healthz" {
				server.handleHealth(response, request)
			} else {
				server.handleReady(response, request)
			}
			wantStatus := http.StatusOK
			if path == "/readyz" && !ready {
				wantStatus = http.StatusServiceUnavailable
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != wantStatus || len(body) != 1 || body["status"] == "" {
				t.Fatalf("unexpected health response: %d %v", response.Code, body)
			}
		}
	}
}
