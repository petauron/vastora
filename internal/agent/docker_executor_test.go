package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/catalog"
)

func TestDeclaredImagePullOptionsUseOnlyMatchingEphemeralCredential(t *testing.T) {
	image := "registry.example.test:5443/team/app@sha256:" + strings.Repeat("a", 64)
	task := DeploymentTask{RegistryCredential: &RegistryCredential{Host: "registry.example.test:5443", Username: "robot", Password: "private-token"}}
	options, err := declaredImagePullOptions(task, image)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := authconfig.Decode(options.RegistryAuth)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ServerAddress != task.RegistryCredential.Host || decoded.Username != "robot" || decoded.Password != "private-token" {
		t.Fatalf("unexpected Registry authentication metadata: %#v", decoded)
	}
	if _, err := declaredImagePullOptions(task, "other.example.test/team/app@sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("credential was accepted for a different Registry authority")
	}
}

func TestPullDeclaredImageReportsRegistryStreamErrorWithoutSecret(t *testing.T) {
	const token = "private-registry-token"
	image := "registry.example.test/team/app@sha256:" + strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/_ping") {
			response.Header().Set("API-Version", "1.55")
			response.WriteHeader(http.StatusOK)
			return
		}
		if !strings.HasSuffix(request.URL.Path, "/images/create") {
			t.Fatalf("unexpected Docker endpoint: %s", request.URL.Path)
		}
		decoded, err := authconfig.Decode(request.Header.Get("X-Registry-Auth"))
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Password != token || decoded.ServerAddress != "registry.example.test" {
			t.Fatalf("unexpected Registry authentication: %#v", decoded)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"errorDetail":{"message":"denied ` + token + `"},"error":"denied ` + token + `"}` + "\n"))
	}))
	defer server.Close()
	docker, err := client.New(client.WithHost(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	task := DeploymentTask{
		Manifest:           catalog.AppManifest{Images: []catalog.Image{{Name: "app", Reference: image}}},
		RegistryCredential: &RegistryCredential{Host: "registry.example.test", Username: "robot", Password: token},
	}
	_, err = pullDeclaredImage(context.Background(), docker, task, "app")
	if err == nil || !strings.Contains(err.Error(), "denied [redacted]") || strings.Contains(err.Error(), token) {
		t.Fatalf("unsafe or missing Registry error: %v", err)
	}
}

func TestApplicationExecutorRejectsManifestBeforeDocker(t *testing.T) {
	executor := ApplicationExecutor{DockerSocket: "not-a-docker-socket"}
	_, err := executor.Deploy(context.Background(), DeploymentTask{ID: "invalid-task", ApplicationID: "application-1", AppKey: cpaKey, Operation: "install"})
	if err == nil || !strings.Contains(err.Error(), "invalid signed application manifest") {
		t.Fatalf("invalid task reached Docker validation: %v", err)
	}
}

func TestApplicationExecutorRejectsTypedConfigurationBeforeDocker(t *testing.T) {
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := catalog.ParseCatalog(payload)
	if err != nil {
		t.Fatal(err)
	}
	var manifest catalog.AppManifest
	for _, app := range value.Apps {
		if app.ID == "cpa" {
			manifest = app
		}
	}
	executor := ApplicationExecutor{DockerSocket: "not-a-docker-socket"}
	_, err = executor.Deploy(context.Background(), DeploymentTask{
		ID: "invalid-config", ApplicationID: "application-1", AppKey: cpaKey, Operation: "install", Manifest: manifest,
		Config: json.RawMessage(`[]`), Secrets: json.RawMessage(`{"management_key":"management","api_key":"api"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "invalid CPA configuration") {
		t.Fatalf("invalid typed configuration reached Docker: %v", err)
	}
	_, err = executor.Deploy(context.Background(), DeploymentTask{ID: "invalid-operation", ApplicationID: "application-1", AppKey: cpaKey, Operation: "replace", Manifest: manifest})
	if err == nil || !strings.Contains(err.Error(), "unsupported application operation") {
		t.Fatalf("unknown operation reached Docker: %v", err)
	}
}

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

func TestWaitForServiceEndpointRejectsClosedTCPPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address, rawPort, _ := net.SplitHostPort(listener.Addr().String())
	port, _ := strconv.Atoi(rawPort)
	_ = listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := waitForServiceEndpoint(ctx, address, port, catalog.Service{Protocol: "tcp"}); err == nil {
		t.Fatal("closed TCP port passed restore health")
	}
}

func TestWaitForServiceEndpointRejectsHTTPServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	host, rawPort, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(rawPort)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := waitForServiceEndpoint(ctx, host, port, catalog.Service{Protocol: "http", HealthPath: "/healthz"}); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("HTTP 503 restore health error = %v", err)
	}
}

func TestReportedServicesSkipsUnavailableThreeXUISubscriptionWhenNotAuthoritative(t *testing.T) {
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
	baseTask := DeploymentTask{
		AppKey: threeXUIKey, Config: json.RawMessage(`{"panel_port":` + rawPort + `}`),
		Manifest: catalog.AppManifest{Services: []catalog.Service{
			{Name: "panel", Protocol: "http", HostPortField: "panel_port", ContainerPort: 2053, HealthPath: "/"},
			{Name: "subscription", Protocol: "http", DefaultHostPort: port + 1, ContainerPort: 2096, HealthPath: "/sub/"},
		}},
	}
	for name, task := range map[string]DeploymentTask{
		"worker deployment": func() DeploymentTask { value := baseTask; value.ApplicationRole = "worker"; return value }(),
		"offline restore": func() DeploymentTask {
			value := baseTask
			value.ApplicationRole = "master"
			value.OfflineRestore = true
			return value
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := reportedServices(context.Background(), task, address)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Services) != 1 || result.Services[0].Name != "panel" {
				t.Fatalf("reported services = %#v", result.Services)
			}
		})
	}
}
