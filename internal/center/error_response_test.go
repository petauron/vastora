package center

import (
	"net/http"
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
		{http.StatusInternalServerError, "center: database failed", "internal_error"},
	}
	for _, test := range tests {
		if got := errorCode(test.status, test.message); got != test.want {
			t.Errorf("errorCode(%d, %q) = %q, want %q", test.status, test.message, got, test.want)
		}
	}
}
