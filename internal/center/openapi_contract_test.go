package center

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var centerAPIRoutePattern = regexp.MustCompile(`mux\.HandleFunc\("(GET|POST|PUT|PATCH|DELETE) (/api/v1/[^\"]+)",\s*(?:s\.requireAuth\((true|false),\s*)?s\.(handle[A-Za-z0-9]+)\)?\)`)

type registeredAPIRoute struct {
	method   string
	path     string
	admin    bool
	mutation bool
}

func TestOpenAPIExecutionRequestsRequireAuthorizationAndDisposition(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path, payload string
		valid         bool
	}{
		{"/api/v1/executions/{executionID}/reexecute", `{"action":"reexecute","executionStopped":true,"note":"checked"}`, true},
		{"/api/v1/executions/{executionID}/reexecute", `{"action":"reexecute","executionStopped":false,"note":"checked"}`, false},
		{"/api/v1/executions/{executionID}/resolve-helper", `{"action":"abandon","executionStopped":true,"note":"checked"}`, true},
		{"/api/v1/executions/{executionID}/resolve-helper", `{"action":"reexecute","executionStopped":true,"note":"checked"}`, false},
		{"/api/v1/executions/{executionID}/confirm-completed", `{"action":"confirm-completed","executionStopped":true,"note":"checked"}`, true},
		{"/api/v1/executions/{executionID}/confirm-completed", `{"action":"abandon","executionStopped":true,"note":"checked"}`, false},
	}
	for _, tc := range cases {
		operation := document.Paths.Value(tc.path).Post
		if operation.RequestBody == nil {
			t.Fatalf("missing strict request: %s", tc.path)
		}
		var value any
		if err := json.Unmarshal([]byte(tc.payload), &value); err != nil {
			t.Fatal(err)
		}
		err := operation.RequestBody.Value.Content["application/json"].Schema.Value.VisitJSON(value)
		if (err == nil) != tc.valid {
			t.Errorf("%s payload %s validity=%v err=%v", tc.path, tc.payload, tc.valid, err)
		}
	}
	// Agent execution requests moved to gRPC; keep the original authorization
	// and disposition assertions against the real service instead of OpenAPI.
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "grpc-contract", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.123", true)
	if _, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.124"); err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil {
		t.Fatalf("claim: %v", err)
	}
	service := newAgentGRPCService(NewServer(store, "", false))
	rpcCtx := agentGRPCTestContext(ctx, node.Credential)
	const session = "grpc-contract-session-0001"
	for _, payload := range []string{`{"sessionId":"grpc-contract-session-0001","protocol":1}`, `{"protocol":2}`} {
		if _, err := service.RegisterExecutionSession(rpcCtx, &agentpb.AgentDocument{AgentId: node.ID, Json: []byte(payload)}); err == nil {
			t.Fatalf("invalid session accepted: %s", payload)
		}
	}
	if _, err := service.RegisterExecutionSession(rpcCtx, &agentpb.AgentDocument{AgentId: node.ID, Json: []byte(`{"sessionId":"grpc-contract-session-0001","protocol":2}`)}); err != nil {
		t.Fatal(err)
	}
	auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, *task)
	if err != nil {
		t.Fatal(err)
	}
	transition := func(input controlplane.ExecutionTransitionRequest) error {
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.TransitionExecution(rpcCtx, &agentpb.ExecutionDocument{AgentId: node.ID, ExecutionId: auth.ID, Json: body})
		return err
	}
	if err := transition(controlplane.ExecutionTransitionRequest{SessionID: session, Action: "start"}); err == nil {
		t.Fatal("start without digest was accepted")
	}
	if err := transition(controlplane.ExecutionTransitionRequest{SessionID: session, Action: "start", Digest: auth.Digest}); err != nil {
		t.Fatal(err)
	}
	if err := transition(controlplane.ExecutionTransitionRequest{SessionID: session, Action: "helper-observe"}); status.Code(err) == codes.InvalidArgument {
		t.Fatalf("valid helper observation rejected as malformed: %v", err)
	}
	if err := transition(controlplane.ExecutionTransitionRequest{SessionID: session, Action: "helper-step"}); err == nil {
		t.Fatal("helper step without phase was accepted")
	}
	if err := transition(controlplane.ExecutionTransitionRequest{SessionID: session, Action: "replay"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid action accepted: %v", err)
	}
}

func TestOpenAPIContractIsValidAndMatchesRegisteredRoutes(t *testing.T) {
	contractPath := filepath.Join("..", "..", "docs", "openapi.json")
	document, err := openapi3.NewLoader().LoadFromFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI 3.1 contract: %v", err)
	}

	serverSource, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	registered := make(map[string]registeredAPIRoute)
	for _, match := range centerAPIRoutePattern.FindAllStringSubmatch(string(serverSource), -1) {
		route := registeredAPIRoute{method: strings.ToUpper(match[1]), path: match[2], admin: match[3] != "", mutation: match[3] == "true"}
		key := route.method + " " + route.path
		if _, exists := registered[key]; exists {
			t.Fatalf("Go route is registered more than once: %s", key)
		}
		registered[key] = route
	}
	if len(registered) == 0 {
		t.Fatal("no registered /api/v1 routes found")
	}

	documented := make(map[string]*openapi3.Operation)
	operationIDs := make(map[string]string)
	for routePath, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			key := strings.ToUpper(method) + " " + routePath
			if _, exists := documented[key]; exists {
				t.Fatalf("OpenAPI operation appears more than once: %s", key)
			}
			documented[key] = operation
			if previous := operationIDs[operation.OperationID]; previous != "" {
				t.Fatalf("OpenAPI operationId %q is shared by %s and %s", operation.OperationID, previous, key)
			}
			operationIDs[operation.OperationID] = key
		}
	}

	for key, route := range registered {
		operation := documented[key]
		if operation == nil {
			t.Errorf("registered route is missing from OpenAPI: %s", key)
			continue
		}
		assertRouteSecurity(t, route, operation.Security)
	}
	for key := range documented {
		if _, exists := registered[key]; !exists {
			t.Errorf("OpenAPI contains an unregistered route: %s", key)
		}
	}
	if t.Failed() {
		registeredKeys, documentedKeys := sortedKeys(registered), sortedKeys(documented)
		t.Logf("registered routes: %s", strings.Join(registeredKeys, ", "))
		t.Logf("documented routes: %s", strings.Join(documentedKeys, ", "))
	}
}

func assertRouteSecurity(t *testing.T, route registeredAPIRoute, actual *openapi3.SecurityRequirements) {
	t.Helper()
	want := securityRequirementNames(route)
	got := make([]string, 0)
	if actual != nil {
		for _, requirement := range *actual {
			names := make([]string, 0, len(requirement))
			for name := range requirement {
				names = append(names, name)
			}
			sort.Strings(names)
			got = append(got, strings.Join(names, "+"))
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s %s security = %v, want %v", route.method, route.path, got, want)
	}
}

func securityRequirementNames(route registeredAPIRoute) []string {
	if route.admin {
		if route.mutation {
			return []string{"AdminCSRF+AdminSession"}
		}
		return []string{"AdminSession"}
	}
	switch {
	case route.path == "/api/v1/setup/status":
		return []string{"", "AdminSession"}
	case route.path == "/api/v1/agent-binaries/{os}/{arch}":
		return []string{"EnrollmentBearer"}
	default:
		return []string{}
	}
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func TestOpenAPIRepresentativeHandlerCompatibility(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	catalogPayload, err := os.ReadFile(filepath.Join("testdata", "reviewed-catalog-v4.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SeedOfficialCatalog(context.Background(), catalogPayload); err != nil {
		t.Fatal(err)
	}
	center := NewServer(store, "", false)
	handler := center.Handler()
	if _, err := newAgentGRPCService(center).Heartbeat(context.Background(), &agentpb.AgentDocument{AgentId: "synthetic-agent", Json: []byte(`{}`)}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated gRPC Agent heartbeat accepted: %v", err)
	}
	retired := httptest.NewRecorder()
	handler.ServeHTTP(retired, httptest.NewRequest(http.MethodPost, "/api/v1/agents/synthetic-agent/heartbeat", strings.NewReader(`{}`)))
	if retired.Code != http.StatusNotFound {
		t.Fatalf("retired Agent HTTP endpoint remains active: %d", retired.Code)
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{name: "administrator", method: http.MethodGet, path: "/api/v1/status", status: http.StatusUnauthorized},
		{name: "catalog administrator", method: http.MethodGet, path: "/api/v1/catalog/sources", status: http.StatusUnauthorized},
		{name: "publication", method: http.MethodPost, path: "/api/v1/publications", body: `{}`, status: http.StatusUnauthorized},
		{name: "update", method: http.MethodPost, path: "/api/v1/system/update", status: http.StatusUnauthorized},
		{name: "integration", method: http.MethodPost, path: "/api/v1/integrations/cloudflare/oauth/start", body: `{}`, status: http.StatusUnauthorized},
		{name: "public catalog", method: http.MethodGet, path: "/api/v1/catalog/official", status: http.StatusOK},
		{name: "bootstrap", method: http.MethodGet, path: "/api/v1/setup/status", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.body != "" {
				request.Header.Set("Content-Type", "application/json")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, body = %q, want %d", response.Code, response.Body.String(), test.status)
			}
			if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Fatalf("Content-Type = %q", contentType)
			}
			if test.status >= 400 {
				var envelope struct {
					Code  string `json:"code"`
					Error string `json:"error"`
				}
				if json.Unmarshal(response.Body.Bytes(), &envelope) != nil || envelope.Code == "" || envelope.Error == "" {
					t.Fatalf("invalid error envelope: %q", response.Body.String())
				}
			}
		})
	}

	for _, test := range []struct {
		name        string
		contentType string
		body        string
		want        string
	}{
		{name: "unsupported content type", contentType: "text/plain", body: `{}`, want: "Content-Type must be application/json"},
		{name: "unknown field", contentType: "application/json", body: `{"known":"value","unknown":true}`, want: "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(test.body))
			request.Header.Set("Content-Type", test.contentType)
			var input struct {
				Known string `json:"known"`
			}
			if err := decodeJSON(request, &input); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decode error = %v, want %q", err, test.want)
			}
		})
	}
}
