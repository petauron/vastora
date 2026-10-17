package deployer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/deployapi"
)

func TestMeridianVerifierUnavailableFailsClosed(t *testing.T) {
	server := NewServer(nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/meridian/verify", strings.NewReader(`{}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable verifier status=%d", response.Code)
	}
}

func TestMeridianVerificationIsResourceBounded(t *testing.T) {
	service := &MeridianProbeService{}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.VerifyMeridianEntry(context.Background(), deployapi.MeridianProbeRequest{}); err == nil {
		t.Fatal("concurrent verification was not rejected")
	}
}
