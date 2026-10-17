package deployer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

func TestMeridianVerificationQueuesContentionWithoutReportingFailure(t *testing.T) {
	service := &MeridianProbeService{}
	if err := service.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- service.acquire(ctx) }()
	select {
	case err := <-result:
		t.Fatalf("concurrent check did not queue: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	<-service.gate
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
		<-service.gate
	case <-ctx.Done():
		t.Fatal("queued check did not acquire released slot")
	}
}

func TestMeridianVerificationQueueHonorsCancellation(t *testing.T) {
	service := &MeridianProbeService{}
	if err := service.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- service.VerifyMeridianEntry(ctx, deployapi.MeridianProbeRequest{}) }()
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled check succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled check remained blocked")
	}
	if len(service.gate) != 1 {
		t.Fatal("canceled waiter released active probe's slot")
	}
	<-service.gate
	if err := service.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-service.gate
	// An already-canceled caller must not take an idle slot either.
	if err := service.acquire(ctx); err == nil || len(service.gate) != 0 {
		t.Fatal("canceled caller retained slot")
	}
}
