package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func TestReportedServicesSkipsDisabledWorkerSubscription(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	address, rawPort, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	task := DeploymentTask{
		AppKey: threeXUIKey, ApplicationRole: "worker", Config: json.RawMessage(`{"panel_port":` + rawPort + `}`),
		Manifest: catalog.AppManifest{Services: []catalog.Service{
			{Name: "panel", Protocol: "http", HostPortField: "panel_port", ContainerPort: 2053, HealthPath: "/"},
			{Name: "subscription", Protocol: "http", DefaultHostPort: port + 1, ContainerPort: 2096, HealthPath: "/sub/"},
		}},
	}
	result, err := reportedServices(context.Background(), task, address)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Services) != 1 || result.Services[0].Name != "panel" {
		t.Fatalf("worker services = %#v", result.Services)
	}
}
