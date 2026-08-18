package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/petauron/vastora/internal/catalog"
)

func TestWaitForServiceEndpointChecksHTTPHealthPath(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			t.Fatalf("unexpected health path: %s", request.URL.Path)
		}
		if requests.Add(1) == 1 {
			response.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusUnauthorized)
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
	service := catalog.Service{Protocol: "http", HealthPath: "/healthz"}
	if err := waitForServiceEndpoint(context.Background(), host, port, service); err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 2 {
		t.Fatalf("health endpoint was not retried after a server error: %d requests", requests.Load())
	}
}
