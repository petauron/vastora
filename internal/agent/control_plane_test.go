package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/ipquality"
	"github.com/petauron/vastora/internal/networking"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// These existing control cases use typed gRPC fixtures and do not restore the
// removed HTTP endpoints.
type controlGRPCFixture struct {
	agentpb.UnimplementedAgentControlServer
	probe             func(context.Context, *agentpb.ProbeRequest) (*agentpb.ProbeResponse, error)
	heartbeat         func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error)
	session           func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error)
	transition        func(context.Context, *agentpb.ExecutionDocument) (*agentpb.Document, error)
	lease             func(context.Context, *agentpb.TaskDocument) (*agentpb.Document, error)
	complete          func(context.Context, *agentpb.TaskDocument) (*agentpb.Document, error)
	beginUpdate       func(context.Context, *agentpb.TaskDocument) (*agentpb.Document, error)
	beginDecommission func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error)
	callback          func(context.Context, *agentpb.DecommissionRequest) (*agentpb.Document, error)
	connect           func(agentpb.AgentControl_ConnectServer) error
}

func (f *controlGRPCFixture) Probe(ctx context.Context, in *agentpb.ProbeRequest) (*agentpb.ProbeResponse, error) {
	if f.probe == nil {
		return nil, status.Error(codes.Unimplemented, "probe")
	}
	return f.probe(ctx, in)
}
func (f *controlGRPCFixture) Heartbeat(ctx context.Context, in *agentpb.AgentDocument) (*agentpb.Document, error) {
	if f.heartbeat == nil {
		return nil, status.Error(codes.Unimplemented, "heartbeat")
	}
	return f.heartbeat(ctx, in)
}
func (f *controlGRPCFixture) RegisterExecutionSession(ctx context.Context, in *agentpb.AgentDocument) (*agentpb.Document, error) {
	if f.session == nil {
		return nil, status.Error(codes.Unimplemented, "session")
	}
	return f.session(ctx, in)
}
func (f *controlGRPCFixture) TransitionExecution(ctx context.Context, in *agentpb.ExecutionDocument) (*agentpb.Document, error) {
	if f.transition == nil {
		return nil, status.Error(codes.Unimplemented, "transition")
	}
	return f.transition(ctx, in)
}
func (f *controlGRPCFixture) RenewTaskLease(ctx context.Context, in *agentpb.TaskDocument) (*agentpb.Document, error) {
	if f.lease == nil {
		return nil, status.Error(codes.Unimplemented, "lease")
	}
	return f.lease(ctx, in)
}
func (f *controlGRPCFixture) CompleteTask(ctx context.Context, in *agentpb.TaskDocument) (*agentpb.Document, error) {
	if f.complete == nil {
		return nil, status.Error(codes.Unimplemented, "complete")
	}
	return f.complete(ctx, in)
}
func (f *controlGRPCFixture) BeginAgentUpdate(ctx context.Context, in *agentpb.TaskDocument) (*agentpb.Document, error) {
	if f.beginUpdate == nil {
		return nil, status.Error(codes.Unimplemented, "update")
	}
	return f.beginUpdate(ctx, in)
}
func (f *controlGRPCFixture) BeginDecommission(ctx context.Context, in *agentpb.AgentDocument) (*agentpb.Document, error) {
	if f.beginDecommission == nil {
		return nil, status.Error(codes.Unimplemented, "decommission")
	}
	return f.beginDecommission(ctx, in)
}
func (f *controlGRPCFixture) DecommissionCallback(ctx context.Context, in *agentpb.DecommissionRequest) (*agentpb.Document, error) {
	if f.callback == nil {
		return nil, status.Error(codes.Unimplemented, "callback")
	}
	return f.callback(ctx, in)
}
func (f *controlGRPCFixture) Connect(stream agentpb.AgentControl_ConnectServer) error {
	if f.connect == nil {
		return status.Error(codes.Unimplemented, "connect")
	}
	return f.connect(stream)
}
func controlFixtureServer(f *controlGRPCFixture) *httptest.Server {
	server := grpc.NewServer()
	agentpb.RegisterAgentControlServer(server, f)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The cleanup helper includes its task-bound path before the method.
		if strings.HasPrefix(r.URL.Path, "/api/v1/agent-decommission-results/") {
			if i := strings.Index(r.URL.Path, "/vastora.agent.v1.AgentControl/"); i >= 0 {
				r = r.Clone(r.Context())
				r.URL.Path = r.URL.Path[i:]
			}
		}
		server.ServeHTTP(w, r)
	})
	return httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
}
func fixtureDocument(t *testing.T, value any) *agentpb.Document {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &agentpb.Document{Json: data}
}

type executorFunc func(context.Context, DeploymentTask) (ApplicationTaskResult, error)

func (execute executorFunc) Deploy(ctx context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	return execute(ctx, task)
}

func testConnection(t *testing.T, agentID, name, centerURL, credential string) Connection {
	t.Helper()
	privateKey, _, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ""
	if strings.HasPrefix(centerURL, "https://") {
		fingerprint = strings.Repeat("a", 64)
	}
	return Connection{AgentID: agentID, Name: name, CenterURL: centerURL, Credential: credential, PrivateKey: privateKey, CAFingerprint: fingerprint}
}

type fakeHostDecommissioner struct {
	scheduled     bool
	request       HostDecommissionRequest
	scheduleCalls int
	scheduleErr   error
}

type fakeHostUpdater struct {
	requests    []HostUpdateRequest
	scheduleErr error
}

func (u *fakeHostUpdater) ScheduleUpdate(_ context.Context, request HostUpdateRequest) error {
	u.requests = append(u.requests, request)
	return u.scheduleErr
}

func (d *fakeHostDecommissioner) ScheduleFinalRemoval(_ context.Context, request HostDecommissionRequest) error {
	d.scheduled = true
	d.scheduleCalls++
	d.request = request
	return d.scheduleErr
}

func TestEnrollmentReportsNativePlatform(t *testing.T) {
	server := enrollmentFixtureServer(func(_ context.Context, input *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
		if input.Token != "one-time-token" || input.Version != Version || input.OperatingSystem != runtime.GOOS || input.Architecture != runtime.GOARCH || len(input.PublicKey) != controlplane.KeySize {
			t.Fatalf("enrollment platform payload = %#v", input)
		}
		return &agentpb.EnrollResponse{AgentId: "agent-1", Credential: "credential", Name: "arm-node", Roles: []string{"worker"}, CapabilitiesJson: []byte(`{"docker":true}`)}, nil
	})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := (Client{}).Enroll(context.Background(), store, server.URL, "one-time-token", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateEnrollmentReplacesOnlyCenterConnection(t *testing.T) {
	server := enrollmentFixtureServer(func(context.Context, *agentpb.EnrollRequest) (*agentpb.EnrollResponse, error) {
		return &agentpb.EnrollResponse{AgentId: "new-agent", Credential: "new-credential", Name: "moved-node", Roles: []string{"worker", "gateway"}, CapabilitiesJson: []byte(`{"docker":true,"gateway":true}`)}, nil
	})
	defer server.Close()
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "old-agent", "old-node", "https://old-center.example.com", "old-credential")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "existing-app", AppKey: "vastora-official/cpa", Version: "1.0.0", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`), ServiceAddress: "100.64.0.2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := (Client{}).MigrateEnrollment(context.Background(), store, server.URL, "one-time-token", "", ""); err != nil {
		t.Fatal(err)
	}
	connection, err := store.Connection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.AgentID != "new-agent" || connection.Name != "moved-node" || connection.CenterURL != server.URL || connection.Credential != "new-credential" {
		t.Fatalf("migrated connection = %#v", connection)
	}
	installations, err := store.ListApplied(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 1 || installations[0].InstanceID != "existing-app" {
		t.Fatalf("migration changed local application state: %#v", installations)
	}
}

func TestObserveProxyRuntimeSynchronizesLegacyEnabledInboundsWithoutChangingThem(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestCount++
		if request.Method != http.MethodGet || request.URL.Path != "/panel/api/inbounds/list" {
			t.Fatalf("unexpected 3x-ui API request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer local-token" {
			t.Fatalf("missing 3x-ui Bearer token: %q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"success":true,"obj":[{"id":7,"remark":"vless","protocol":"vless","port":443,"listen":"0.0.0.0","enable":true,"tag":"vastora-reality-7","total":1073741824,"streamSettings":{"network":"tcp","security":"reality"}},{"id":8,"remark":"disabled","protocol":"vmess","port":8443,"listen":"127.0.0.1","enable":false,"streamSettings":{"network":"ws"}},{"id":9,"remark":"Vastora REALITY fallback guard","protocol":"tunnel","port":21000,"listen":"127.0.0.1","enable":true,"tag":"vastora-reality-7-guard","streamSettings":{}}]}`))
	}))
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portText)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	config, _ := json.Marshal(map[string]any{"timezone": "UTC", "panel_port": port, "enable_fail2ban": true, "vmess_aead_forced": false})
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "3x-install", AppKey: threeXUIKey, Version: "3.7.0", Config: config, Secrets: json.RawMessage(`{"api_token":"local-token"}`), ServiceAddress: host}); err != nil {
		t.Fatal(err)
	}

	observed, err := observeProxyRuntime(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 || len(observed) != 2 {
		t.Fatalf("unexpected 3x-ui observation: requests=%d values=%#v", requestCount, observed)
	}
	if observed[0].Name != "inbound-7" || observed[0].AppProtocol != "vless/tcp/reality" || observed[0].Protocol != "tcp" || !observed[0].Enabled || observed[0].Port != 443 || observed[0].InboundTag != "vastora-reality-7" || observed[0].InboundTotalBytes != 1073741824 {
		t.Fatalf("enabled inbound was not synchronized: %#v", observed[0])
	}
	if observed[1].Name != "inbound-8" || observed[1].AppProtocol != "vmess/ws" || observed[1].Enabled {
		t.Fatalf("disabled inbound state was not preserved: %#v", observed[1])
	}
}

func TestHeartbeatAllowsMeridianSubscriptionHostWithoutLocalXrayRuntime(t *testing.T) {
	heartbeats := 0
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
		if request.AgentId != "agent-1" {
			t.Fatalf("unexpected request path: %s", request.AgentId)
		}
		var payload struct {
			ApplicationEndpoints         []ApplicationEndpointObservation `json:"applicationEndpoints"`
			ApplicationEndpointsObserved bool                             `json:"applicationEndpointsObserved"`
			MeridianRuntime              json.RawMessage                  `json:"meridianRuntime"`
		}
		if err := json.Unmarshal(request.Json, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ApplicationEndpointsObserved || len(payload.ApplicationEndpoints) != 0 || string(payload.MeridianRuntime) != "null" {
			t.Fatalf("controller-only Glivela package reported a proxy runtime: %#v", payload)
		}
		heartbeats++
		return &agentpb.Document{Json: []byte(`{}`)}, nil
	}})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{
		InstanceID: "meridian-controller", ApplicationID: "controller", AppKey: meridianKey,
		Version: "1.0.0", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`), ServiceAddress: "127.0.0.1",
	}); err != nil {
		t.Fatal(err)
	}
	observationErr, heartbeatErr := (Client{}).heartbeat(context.Background(), store)
	if heartbeatErr != nil || observationErr != nil || heartbeats != 1 {
		t.Fatalf("controller-only heartbeat failed: observation=%v heartbeat=%v count=%d", observationErr, heartbeatErr, heartbeats)
	}
}

func TestThreeXUIClientInboundDisplayNameSurvivesAgentRoundTrip(t *testing.T) {
	var task ThreeXUIClientCommandTask
	if err := json.Unmarshal([]byte(`{"action":"list","inbounds":[{"id":7,"name":"inbound-7","displayName":"美国Provider A","applicationId":"app-1","nodeId":"node-1","nodeName":"Provider A"}]}`), &task); err != nil {
		t.Fatal(err)
	}
	result := ThreeXUIClientCommandResult{Inbounds: task.Inbounds}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"displayName":"美国Provider A"`) {
		t.Fatalf("display name was lost from Agent result: %s", payload)
	}
}

func TestUncertainTaskOutcomeKeepsItsCause(t *testing.T) {
	cause := errors.New("external mutation outcome is unknown")
	err := uncertainTaskOutcome(cause)
	if !taskOutcomeIsUncertain(err) || !errors.Is(err, cause) || err.Error() != cause.Error() {
		t.Fatalf("uncertain task error = %#v", err)
	}
	if taskOutcomeIsUncertain(cause) || taskOutcomeIsUncertain(nil) || uncertainTaskOutcome(nil) != nil {
		t.Fatal("ordinary errors incorrectly marked uncertain")
	}
}

func TestHeartbeatsNeverRestoreGatewayState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	heartbeats := 0
	startupHeartbeats := 0
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
		if request.AgentId != "agent-1" {
			t.Fatalf("unexpected Agent: %s", request.AgentId)
		}
		var payload struct {
			Startup bool `json:"startup"`
		}
		if err := json.Unmarshal(request.Json, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Startup {
			startupHeartbeats++
		}
		heartbeats++
		if heartbeats == 2 {
			cancel()
		}
		return &agentpb.Document{Json: []byte(`{}`)}, nil
	}})
	defer server.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	state := gateway.DesiredState{Revision: 3, Listeners: []gateway.Listener{{Kind: "lan", Address: "192.0.2.10", HTTPPort: 80, HTTPSPort: 443}}}
	if _, err := store.RecordGatewayState(context.Background(), state, nil); err != nil {
		t.Fatal(err)
	}
	driver := &fakeGatewayDriver{}
	client := Client{GatewayDriver: driver}
	client.RunHeartbeats(ctx, store, time.Second, func(err error) {
		if ctx.Err() == nil {
			t.Errorf("heartbeat error: %v", err)
		}
	})
	if heartbeats != 2 {
		t.Fatalf("expected two heartbeat cycles, got %d", heartbeats)
	}
	if startupHeartbeats != 0 {
		t.Fatalf("steady heartbeat loop repeated startup %d times", startupHeartbeats)
	}
	if len(driver.applied) != 0 {
		t.Fatalf("heartbeat replayed persisted gateway state %d times", len(driver.applied))
	}
}

// The former bounded-long-poll case now checks a single idle task stream.
type idleTaskStreamFixture struct {
	agentpb.UnimplementedAgentControlServer
	ready chan struct{}
	calls atomic.Int32
}

func (f *idleTaskStreamFixture) Connect(stream agentpb.AgentControl_ConnectServer) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetHello() == nil {
		return errors.New("expected hello")
	}
	if err := stream.Send(&agentpb.CenterFrame{Body: &agentpb.CenterFrame_Welcome{Welcome: &agentpb.Welcome{ExecutionProtocol: int32(controlplane.ExecutionProtocol)}}}); err != nil {
		return err
	}
	frame, err := stream.Recv()
	if err != nil {
		return err
	}
	if frame.GetReady() == nil {
		return errors.New("expected ready")
	}
	f.calls.Add(1)
	close(f.ready)
	<-stream.Context().Done()
	return stream.Context().Err()
}
func TestTaskStreamWaitsWithoutPolling(t *testing.T) {
	service := &idleTaskStreamFixture{ready: make(chan struct{})}
	grpcServer := grpc.NewServer()
	agentpb.RegisterAgentControlServer(grpcServer, service)
	server := httptest.NewServer(h2c.NewHandler(grpcServer, &http2.Server{}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, _, closeStream, err := (Client{executionSession: "test-session"}).openTaskStream(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream()
	if err := stream.Send(&agentpb.AgentFrame{Body: &agentpb.AgentFrame_Ready{Ready: &agentpb.Ready{}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.ready:
	case <-time.After(time.Second):
		t.Fatal("stream did not become ready")
	}
	received := make(chan error, 1)
	go func() { _, err := stream.Recv(); received <- err }()
	select {
	case err := <-received:
		t.Fatalf("idle stream ended unexpectedly: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if service.calls.Load() != 1 {
		t.Fatalf("unexpected repeated task requests: %d", service.calls.Load())
	}
	cancel()
}

func TestTaskClaimDecryptsEnvelopeBoundToAgentAndAttempt(t *testing.T) {
	connection := testConnection(t, "agent-1", "test", "http://127.0.0.1", "credential")
	publicKey, err := controlplane.PublicKey(connection.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	task := DeploymentTask{Kind: "application.apply", ID: "task-1", Attempt: 3, Secrets: json.RawMessage(`{"token":"private"}`)}
	plaintext := []byte(`{"kind":"application.apply","id":"task-1","attempt":3,"secrets":{"token":"private"},"applicationCommand":{"action":"create","regionCode":"US"}}`)
	digest := sha256.Sum256(plaintext)
	authorization := controlplane.ExecutionAuthorization{ID: "execution-1", Protocol: controlplane.ExecutionProtocol, Digest: hex.EncodeToString(digest[:])}
	envelope, err := controlplane.Seal(publicKey, plaintext, controlplane.TaskAdditionalData(connection.AgentID, task.ID, task.Attempt))
	if err != nil {
		t.Fatal(err)
	}
	assignment := &agentpb.Assignment{TaskId: task.ID, Attempt: task.Attempt, ExecutionId: authorization.ID, ExecutionProtocol: int32(authorization.Protocol), Digest: authorization.Digest, EnvelopeVersion: int32(envelope.Version), EphemeralPublicKey: envelope.EphemeralPublicKey, Nonce: envelope.Nonce, Ciphertext: envelope.Ciphertext}
	claimed, err := decryptGRPCAssignment(connection, assignment)
	if err != nil || claimed == nil || claimed.ID != task.ID || string(claimed.Secrets) != string(task.Secrets) || claimed.ApplicationCommand == nil || claimed.ApplicationCommand.RegionCode != "US" {
		t.Fatalf("decrypted task=%#v err=%v", claimed, err)
	}
}

func TestTerminalTaskAuthorityConflictStartsFreshSessionWithoutReplayingTask(t *testing.T) {
	connection := testConnection(t, "agent-1", "test", "http://127.0.0.1", "credential")
	publicKey, err := controlplane.PublicKey(connection.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	task := DeploymentTask{Kind: ipquality.Kind, ID: "stale-task", Attempt: 1, IPQuality: &ipquality.Task{Address: "203.0.113.8", BindAddress: "10.0.0.8"}}
	plaintext, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(plaintext)
	authorization := controlplane.ExecutionAuthorization{ID: "stale-execution", Protocol: controlplane.ExecutionProtocol, Digest: hex.EncodeToString(digest[:])}
	envelope, err := controlplane.Seal(publicKey, plaintext, controlplane.TaskAdditionalData(connection.AgentID, task.ID, task.Attempt))
	if err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int64
	var sessions atomic.Int64
	restarted := make(chan struct{})
	resumed := make(chan struct{})
	server := controlFixtureServer(&controlGRPCFixture{
		session: func(_ context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
			if input.AgentId != "agent-1" {
				t.Fatalf("unexpected Agent %q", input.AgentId)
			}
			if sessions.Add(1) == 2 {
				close(restarted)
			}
			return &agentpb.Document{Json: []byte(`{"registered":true}`)}, nil
		},
		transition: func(_ context.Context, input *agentpb.ExecutionDocument) (*agentpb.Document, error) {
			if input.ExecutionId != "stale-execution" {
				t.Fatalf("unexpected execution %q", input.ExecutionId)
			}
			return nil, status.Error(codes.FailedPrecondition, "execution authority expired")
		},
		connect: func(stream agentpb.AgentControl_ConnectServer) error {
			frame, err := stream.Recv()
			if err != nil {
				return err
			}
			if frame.GetHello() == nil {
				return errors.New("hello missing")
			}
			if err := stream.Send(&agentpb.CenterFrame{Body: &agentpb.CenterFrame_Welcome{Welcome: &agentpb.Welcome{ExecutionProtocol: int32(controlplane.ExecutionProtocol)}}}); err != nil {
				return err
			}
			frame, err = stream.Recv()
			if err != nil {
				return err
			}
			if frame.GetReady() == nil {
				return errors.New("ready missing")
			}
			if count := claims.Add(1); count == 1 {
				assignment := &agentpb.Assignment{TaskId: task.ID, Attempt: task.Attempt, ExecutionId: authorization.ID, ExecutionProtocol: int32(authorization.Protocol), Digest: authorization.Digest, EnvelopeVersion: int32(envelope.Version), EphemeralPublicKey: envelope.EphemeralPublicKey, Nonce: envelope.Nonce, Ciphertext: envelope.Ciphertext}
				if err := stream.Send(&agentpb.CenterFrame{Body: &agentpb.CenterFrame_Assignment{Assignment: assignment}}); err != nil {
					return err
				}
			} else if count == 2 {
				close(resumed)
			}
			<-stream.Context().Done()
			return stream.Context().Err()
		},
	})
	defer server.Close()
	connection.CenterURL = server.URL
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		(Client{}).RunTasks(ctx, store, func(error) {})
		close(done)
	}()
	select {
	case <-restarted:
	case <-time.After(5 * time.Second):
		t.Fatal("task receiver did not register a new execution session")
	}
	select {
	case <-resumed:
	case <-time.After(5 * time.Second):
		t.Fatal("task receiver did not resume streaming after registration")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("task receiver did not stop after cancellation")
	}
	if sessions.Load() != 2 || claims.Load() != 2 {
		t.Fatalf("sessions=%d claims=%d; stale task may have been replayed", sessions.Load(), claims.Load())
	}
}

func TestHeartbeatKeepsCenterConnectedWhenThreeXUIObservationFails(t *testing.T) {
	heartbeats := 0
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
		if request.AgentId != "agent-1" {
			t.Fatalf("unexpected request path: %s", request.AgentId)
		}
		var payload struct {
			ApplicationEndpoints         []ApplicationEndpointObservation `json:"applicationEndpoints"`
			ApplicationEndpointsObserved bool                             `json:"applicationEndpointsObserved"`
		}
		if err := json.Unmarshal(request.Json, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ApplicationEndpointsObserved || len(payload.ApplicationEndpoints) != 0 {
			t.Fatalf("failed observation was reported as authoritative: %#v", payload)
		}
		heartbeats++
		return &agentpb.Document{Json: []byte(`{}`)}, nil
	}})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	config := json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)
	if _, err := store.RecordApplied(context.Background(), AppliedInstallation{InstanceID: "3x-install", AppKey: threeXUIKey, Version: "3.7.0", Config: config, Secrets: json.RawMessage(`{}`), ServiceAddress: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	observationErr, heartbeatErr := (Client{}).heartbeat(context.Background(), store)
	if heartbeatErr != nil {
		t.Fatalf("observation failure blocked heartbeat: %v", heartbeatErr)
	}
	if observationErr == nil || heartbeats != 1 {
		t.Fatalf("observation error was not isolated: err=%v heartbeats=%d", observationErr, heartbeats)
	}
}

func TestHeartbeatReportsAgentObservedPublicEgress(t *testing.T) {
	var reported *networking.PublicEgress
	observations := 0
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
		var payload struct {
			PublicEgress *networking.PublicEgress `json:"publicEgress"`
		}
		if err := json.Unmarshal(request.Json, &payload); err != nil {
			t.Fatal(err)
		}
		reported = payload.PublicEgress
		return &agentpb.Document{Json: []byte(`{"publicAddressLookupUrl":"https://helper.example.com/network/public-address"}`)}, nil
	}})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	client := Client{PublicEgress: func(_ context.Context, endpoint string, allowPrivate bool, _ []networking.Candidate, now time.Time) (*networking.PublicEgress, error) {
		observations++
		if endpoint != "https://helper.example.com/network/public-address" || allowPrivate {
			t.Fatalf("unexpected public helper: %q private=%t", endpoint, allowPrivate)
		}
		return &networking.PublicEgress{Address: "198.51.100.22", BindAddress: "10.0.0.22", Mode: networking.PublicModeNAT, ObservedAt: now}, nil
	}}
	if observationErr, heartbeatErr := client.heartbeatWithStartup(context.Background(), store, true); observationErr != nil || heartbeatErr != nil {
		t.Fatalf("heartbeat errors = %v, %v", observationErr, heartbeatErr)
	}
	if observations != 1 || reported == nil || reported.Address != "198.51.100.22" || reported.BindAddress != "10.0.0.22" || reported.Mode != networking.PublicModeNAT || reported.ObservedAt.IsZero() {
		t.Fatalf("reported public egress = %#v", reported)
	}
	if observationErr, heartbeatErr := client.heartbeat(context.Background(), store); observationErr != nil || heartbeatErr != nil {
		t.Fatalf("ordinary heartbeat errors = %v, %v", observationErr, heartbeatErr)
	}
	if observations != 2 || reported == nil {
		t.Fatalf("ordinary heartbeat observed=%d reported=%#v", observations, reported)
	}
}

func TestInitialHeartbeatRequestsRealityGuardRevalidation(t *testing.T) {
	startup := false
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
		var payload struct {
			Startup bool `json:"startup"`
		}
		if err := json.Unmarshal(request.Json, &payload); err != nil {
			t.Fatal(err)
		}
		startup = payload.Startup
		return &agentpb.Document{Json: []byte(`{}`)}, nil
	}})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	if err := (Client{}).StartupHeartbeat(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if !startup {
		t.Fatal("initial heartbeat did not request REALITY guard revalidation")
	}
}

func TestHeartbeatSwitchesToVerifiedCenterURL(t *testing.T) {
	healthChecks := 0
	newCenter := controlFixtureServer(&controlGRPCFixture{probe: func(context.Context, *agentpb.ProbeRequest) (*agentpb.ProbeResponse, error) {
		healthChecks++
		return &agentpb.ProbeResponse{Ready: true}, nil
	}})
	defer newCenter.Close()

	oldCenter := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
		if request.AgentId != "agent-1" {
			t.Fatalf("unexpected Agent %q", request.AgentId)
		}
		return fixtureDocument(t, map[string]any{"connected": true, "centerUrl": newCenter.URL}), nil
	}})
	defer oldCenter.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", oldCenter.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	observationErr, heartbeatErr := (Client{}).heartbeat(context.Background(), store)
	if observationErr != nil || heartbeatErr != nil {
		t.Fatalf("heartbeat failed: observation=%v heartbeat=%v", observationErr, heartbeatErr)
	}
	connection, err := store.Connection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.CenterURL != newCenter.URL || healthChecks != 1 {
		t.Fatalf("Center URL was not switched after verification: connection=%#v healthChecks=%d", connection, healthChecks)
	}
}

func TestHeartbeatKeepsExplicitHostOnlyCenterChannel(t *testing.T) {
	healthChecks := 0
	newCenter := controlFixtureServer(&controlGRPCFixture{probe: func(context.Context, *agentpb.ProbeRequest) (*agentpb.ProbeResponse, error) {
		healthChecks++
		return &agentpb.ProbeResponse{Ready: true}, nil
	}})
	defer newCenter.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	connection := testConnection(t, "agent-1", "center-host", "http://127.0.0.1:8080", "credential")
	if err := store.SaveConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	if err := store.SetLocalCenterChannel(connection.CenterURL); err != nil {
		t.Fatal(err)
	}
	if err := (Client{}).applyDesiredCenterURL(context.Background(), store, connection, newCenter.URL); err != nil {
		t.Fatal(err)
	}
	current, err := store.Connection(context.Background())
	if err != nil || current.CenterURL != connection.CenterURL || healthChecks != 0 {
		t.Fatalf("host-only channel changed: connection=%#v healthChecks=%d err=%v", current, healthChecks, err)
	}
}

func TestHeartbeatKeepsCurrentCenterWhenDesiredURLIsNotReady(t *testing.T) {
	newCenter := controlFixtureServer(&controlGRPCFixture{probe: func(context.Context, *agentpb.ProbeRequest) (*agentpb.ProbeResponse, error) {
		return &agentpb.ProbeResponse{Ready: false}, nil
	}})
	defer newCenter.Close()
	oldCenter := controlFixtureServer(&controlGRPCFixture{heartbeat: func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error) {
		return fixtureDocument(t, map[string]any{"connected": true, "centerUrl": newCenter.URL}), nil
	}})
	defer oldCenter.Close()

	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", oldCenter.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	_, heartbeatErr := (Client{}).heartbeat(context.Background(), store)
	if heartbeatErr == nil || !strings.Contains(heartbeatErr.Error(), "Center probe is not OK") {
		t.Fatalf("unready Center URL was not rejected: %v", heartbeatErr)
	}
	connection, err := store.Connection(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if connection.CenterURL != oldCenter.URL {
		t.Fatalf("unready Center URL replaced the working address: %#v", connection)
	}
}

func TestHeartbeatAppliesCenterTailscaleIsolationState(t *testing.T) {
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
		if request.AgentId != "agent-1" {
			t.Fatalf("unexpected request path: %s", request.AgentId)
		}
		var heartbeat struct {
			TailscaleEnrolled  bool   `json:"tailscaleEnrolled"`
			TailscaleOwnership string `json:"tailscaleOwnership"`
		}
		if err := json.Unmarshal(request.Json, &heartbeat); err != nil {
			t.Fatal(err)
		}
		if !heartbeat.TailscaleEnrolled || heartbeat.TailscaleOwnership != "managed" {
			t.Fatalf("Tailscale host state was not reported: %#v", heartbeat)
		}
		return &agentpb.Document{Json: []byte(`{"tailscaleIsolation":{"controlUrl":"https://headscale.example.com","controlAddresses":["203.0.113.10"]}}`)}, nil
	}})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "test", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	var applied TailscaleIsolationDesiredState
	client := Client{TailscaleEnrolled: true, TailscaleOwnership: "managed", TailscaleIsolation: func(_ context.Context, state TailscaleIsolationDesiredState) error {
		applied = state
		return nil
	}}
	if _, err := client.heartbeat(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if applied.ControlURL != "https://headscale.example.com" || len(applied.ControlAddresses) != 1 || applied.ControlAddresses[0] != "203.0.113.10" {
		t.Fatalf("Tailscale isolation state was not applied: %#v", applied)
	}
}

func TestTunnelDesiredStateRequiresFixedImageAndTokenWhileRunning(t *testing.T) {
	valid := TunnelDesiredState{Revision: 2, Status: "running", Image: "docker.io/cloudflare/cloudflared:2026.7.2", Token: "token"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Tunnel state was rejected: %v", err)
	}
	valid.Token = ""
	if err := valid.Validate(); err == nil {
		t.Fatal("running Tunnel without a token was accepted")
	}
	if err := (TunnelDesiredState{Revision: 3, Status: "stopped"}).Validate(); err != nil {
		t.Fatalf("stopped Tunnel unexpectedly required a token: %v", err)
	}
}

func executionAuthorizationForTest() controlplane.ExecutionAuthorization {
	return controlplane.ExecutionAuthorization{ID: "execution-1", Protocol: controlplane.ExecutionProtocol, Digest: strings.Repeat("a", 64)}
}

func acknowledgeExecutionForTest(t *testing.T, _ context.Context, request *agentpb.ExecutionDocument) (*agentpb.Document, error) {
	t.Helper()
	var input controlplane.ExecutionTransitionRequest
	if request.AgentId != "agent-1" || request.ExecutionId != "execution-1" || json.Unmarshal(request.Json, &input) != nil || input.SessionID != "test-execution-session" {
		t.Error("invalid execution transition")
		return nil, status.Error(codes.InvalidArgument, "invalid execution transition")
	}
	return &agentpb.Document{Json: []byte(`{"recorded":true}`)}, nil
}
func requireFixtureAuthorization(t *testing.T, ctx context.Context, expected string) {
	t.Helper()
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) != 1 || values[0] != "Bearer "+expected {
		t.Error("incorrect lifecycle credential")
	}
}

func TestExecutionHostHandoffDoesNotRearmOnRestart(t *testing.T) {
	for _, kind := range []string{"agent.update", "agent.decommission"} {
		for _, failed := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/scheduled", true: "/schedule-failed"}[failed], func(t *testing.T) {
				var starts, results atomic.Int64
				server := controlFixtureServer(&controlGRPCFixture{
					transition: func(_ context.Context, request *agentpb.ExecutionDocument) (*agentpb.Document, error) {
						var input controlplane.ExecutionTransitionRequest
						if request.ExecutionId != "execution-1" || request.AgentId != "agent-1" || json.Unmarshal(request.Json, &input) != nil || input.SessionID != "test-execution-session" {
							t.Error("invalid authorization")
							return nil, status.Error(codes.InvalidArgument, "invalid authorization")
						}
						if input.Action == "start" && starts.Add(1) > 1 {
							return nil, status.Error(codes.FailedPrecondition, "already started")
						}
						return &agentpb.Document{Json: []byte(`{"recorded":true}`)}, nil
					},
					complete: func(_ context.Context, request *agentpb.TaskDocument) (*agentpb.Document, error) {
						var result struct {
							Succeeded        bool `json:"succeeded"`
							HostUpdateHelper bool `json:"hostUpdateHelper"`
						}
						if request.TaskId != "host-task" || request.AgentId != "agent-1" || json.Unmarshal(request.Json, &result) != nil || result.Succeeded || result.HostUpdateHelper || !failed {
							t.Error("scheduling was reported as completed host work")
						}
						results.Add(1)
						return &agentpb.Document{Json: []byte(`{"completed":true}`)}, nil
					},
				})
				defer server.Close()
				directory := t.TempDir()
				store, err := Open(directory)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "node", server.URL, "credential")); err != nil {
					t.Fatal(err)
				}
				updater := &fakeHostUpdater{}
				remover := &fakeHostDecommissioner{}
				if failed {
					updater.scheduleErr = errors.New("systemd unavailable")
					remover.scheduleErr = errors.New("systemd unavailable")
				}
				client := Client{executionSession: "test-execution-session", Updater: updater, Decommissioner: remover}
				task := DeploymentTask{Kind: kind, ID: "host-task", Attempt: 1, TargetVersion: "0.1.0-alpha.129", DeleteData: true, Authorization: executionAuthorizationForTest(),
					DecommissionCallbackURL: server.URL + "/api/v1/agent-decommission-results/host-task", DecommissionCallbackToken: "callback-token"}
				reports := 0
				client.processTaskWithLease(context.Background(), store, task, func(error) { reports++ })
				if (reports > 0) != failed || results.Load() != map[bool]int64{false: 0, true: 1}[failed] {
					t.Fatalf("handoff reports=%d results=%d", reports, results.Load())
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = Open(directory)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				reports = 0
				client.processTaskWithLease(context.Background(), store, task, func(error) { reports++ })
				if reports == 0 || starts.Load() != 2 {
					t.Fatal("old authorization was accepted after restart")
				}
				if kind == "agent.update" {
					if len(updater.requests) != 1 {
						t.Fatalf("update scheduled %d times", len(updater.requests))
					}
					request := updater.requests[0]
					if request.ExecutionID != task.Authorization.ID || request.SessionID != client.executionSession || request.TargetVersion != task.TargetVersion || request.Connection.Credential != "credential" {
						t.Fatal("incomplete update authorization handoff")
					}
				} else {
					if remover.scheduleCalls != 1 {
						t.Fatalf("cleanup scheduled %d times", remover.scheduleCalls)
					}
					if remover.request.ExecutionID != task.Authorization.ID || remover.request.SessionID != client.executionSession || remover.request.CallbackToken != task.DecommissionCallbackToken || !remover.request.DeleteData {
						t.Fatal("incomplete cleanup authorization handoff")
					}
				}
			})
		}
	}
}

func TestAgentHeartbeatAdvertisesRemoteUpdateCapability(t *testing.T) {
	reported := false
	server := controlFixtureServer(&controlGRPCFixture{heartbeat: func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
		var heartbeat struct {
			RemoteUpdateSupported bool `json:"remoteUpdateSupported"`
		}
		if request.AgentId != "agent-1" || json.Unmarshal(request.Json, &heartbeat) != nil {
			t.Fatalf("unexpected heartbeat request: %s", request.AgentId)
		}
		reported = heartbeat.RemoteUpdateSupported
		return &agentpb.Document{Json: []byte(`{}`)}, nil
	}})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "node", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	client := Client{Updater: &fakeHostUpdater{}}
	if _, err := client.heartbeat(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	if !reported {
		t.Fatal("Agent did not advertise its persistent update capability")
	}
}

func TestHostDecommissionHelperUsesSeparateLifecycleCredentials(t *testing.T) {
	started := false
	completed := false
	server := controlFixtureServer(&controlGRPCFixture{
		beginDecommission: func(ctx context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
			requireFixtureAuthorization(t, ctx, "credential")
			if request.AgentId != "agent-1" {
				t.Error("wrong Agent")
			}
			started = true
			return &agentpb.Document{Json: []byte(`{"started":true}`)}, nil
		},
		callback: func(ctx context.Context, request *agentpb.DecommissionRequest) (*agentpb.Document, error) {
			requireFixtureAuthorization(t, ctx, "callback-token")
			if request.TaskId != "agent-decommission-agent-1" {
				t.Error("wrong callback task")
			}
			completed = true
			return &agentpb.Document{Json: []byte(`{"completed":true}`)}, nil
		},
	})
	defer server.Close()
	connection := testConnection(t, "agent-1", "node", server.URL, "credential")
	client := Client{}
	if err := client.BeginHostDecommission(context.Background(), connection, "agent-decommission-agent-1", 2, "test-execution", "test-session"); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteHostDecommission(context.Background(), server.URL+"/api/v1/agent-decommission-results/agent-decommission-agent-1", "callback-token", "agent-decommission-agent-1", 2); err != nil {
		t.Fatal(err)
	}
	if !started || !completed {
		t.Fatalf("host cleanup lifecycle callbacks: started=%v completed=%v", started, completed)
	}
}

func TestHostDecommissionRejectsSuccessWithoutAcknowledgement(t *testing.T) {
	for _, body := range []string{`{}`, `{"ok":true}`, `{"started":false,"completed":false}`, `<html>Sign in</html>`, ""} {
		t.Run(body, func(t *testing.T) {
			server := controlFixtureServer(&controlGRPCFixture{
				beginDecommission: func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error) {
					return &agentpb.Document{Json: []byte(body)}, nil
				},
				callback: func(context.Context, *agentpb.DecommissionRequest) (*agentpb.Document, error) {
					return &agentpb.Document{Json: []byte(body)}, nil
				},
			})
			defer server.Close()
			connection := testConnection(t, "agent-1", "node", server.URL, "credential")
			client := Client{}
			if err := client.BeginHostDecommission(context.Background(), connection, "agent-decommission-agent-1", 2, "test-execution", "test-session"); err == nil {
				t.Fatal("unacknowledged handoff was accepted")
			}
			if err := client.CompleteHostDecommission(context.Background(), server.URL+"/api/v1/agent-decommission-results/agent-decommission-agent-1", "callback-token", "agent-decommission-agent-1", 2); err == nil {
				t.Fatal("unacknowledged completion was accepted")
			}
		})
	}
}

func TestHostUpdateHelperUsesAuthenticatedLifecycleCallbacks(t *testing.T) {
	started := false
	completed := false
	server := controlFixtureServer(&controlGRPCFixture{
		beginUpdate: func(ctx context.Context, request *agentpb.TaskDocument) (*agentpb.Document, error) {
			requireFixtureAuthorization(t, ctx, "credential")
			if request.AgentId != "agent-1" || request.TaskId != "agent-update-task-1" {
				t.Error("unexpected update task")
			}
			started = true
			return &agentpb.Document{Json: []byte(`{"ok":true}`)}, nil
		},
		complete: func(ctx context.Context, request *agentpb.TaskDocument) (*agentpb.Document, error) {
			requireFixtureAuthorization(t, ctx, "credential")
			var payload struct {
				HostUpdateHelper bool `json:"hostUpdateHelper"`
			}
			if request.AgentId != "agent-1" || request.TaskId != "agent-update-task-1" || json.Unmarshal(request.Json, &payload) != nil || !payload.HostUpdateHelper {
				t.Errorf("update result did not identify its helper owner: %+v", payload)
			}
			completed = true
			return &agentpb.Document{Json: []byte(`{"ok":true}`)}, nil
		},
	})
	defer server.Close()
	connection := testConnection(t, "agent-1", "node", server.URL, "credential")
	client := Client{}
	if err := client.BeginHostUpdate(context.Background(), connection, "agent-update-task-1", 2, "helper-execution", "helper-session"); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteHostUpdate(context.Background(), connection, "agent-update-task-1", 2, nil, false, "helper-execution", "helper-session"); err != nil {
		t.Fatal(err)
	}
	if !started || !completed {
		t.Fatalf("host update lifecycle callbacks: started=%v completed=%v", started, completed)
	}
}

func TestHostUpdateHelperReportsRecoveryWithoutTerminalFailure(t *testing.T) {
	calls := 0
	server := controlFixtureServer(&controlGRPCFixture{complete: func(ctx context.Context, request *agentpb.TaskDocument) (*agentpb.Document, error) {
		calls++
		requireFixtureAuthorization(t, ctx, "credential")
		if request.AgentId != "agent-1" || request.TaskId != "agent-update-task-1" {
			t.Error("invalid recovery identity")
		}
		var payload struct {
			Attempt                int64  `json:"attempt"`
			Succeeded              bool   `json:"succeeded"`
			Error                  string `json:"error"`
			ReconciliationRequired bool   `json:"reconciliationRequired"`
			HostUpdateHelper       bool   `json:"hostUpdateHelper"`
		}
		if err := json.Unmarshal(request.Json, &payload); err != nil || payload.Attempt != 2 || payload.Succeeded || !payload.ReconciliationRequired || !payload.HostUpdateHelper || payload.Error == "" {
			t.Errorf("recovery was not marked nonterminal: %#v %v", payload, err)
		}
		return &agentpb.Document{Json: []byte(`{"completed":true}`)}, nil
	}})
	defer server.Close()
	connection := testConnection(t, "agent-1", "node", server.URL, "credential")
	client := Client{}
	if err := client.CompleteHostUpdate(context.Background(), connection, "agent-update-task-1", 2, errors.New("candidate requires recovery"), true, "helper-execution", "helper-session"); err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteHostUpdate(context.Background(), connection, "agent-update-task-1", 2, nil, true, "helper-execution", "helper-session"); err == nil || calls != 1 {
		t.Fatal("invalid successful-recovery payload was sent to Center")
	}
}

func TestTaskCompletionUsesFreshContextsAfterExecutionCancellation(t *testing.T) {
	resultReceived := false
	server := controlFixtureServer(&controlGRPCFixture{
		transition: func(ctx context.Context, request *agentpb.ExecutionDocument) (*agentpb.Document, error) {
			return acknowledgeExecutionForTest(t, ctx, request)
		},
		complete: func(_ context.Context, request *agentpb.TaskDocument) (*agentpb.Document, error) {
			if request.AgentId != "agent-1" || request.TaskId != "deadline-task" {
				t.Error("unexpected task identity")
			}
			var result struct {
				Succeeded bool `json:"succeeded"`
				Unknown   bool `json:"reconciliationRequired"`
			}
			if json.Unmarshal(request.Json, &result) != nil || result.Succeeded || !result.Unknown {
				t.Error("cancelled execution did not report uncertainty")
			}
			resultReceived = true
			return &agentpb.Document{Json: []byte(`{"completed":true}`)}, nil
		},
	})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "node", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	executionContext, cancelExecution := context.WithCancel(context.Background())
	defer cancelExecution()
	task := DeploymentTask{Kind: "application.apply", ID: "deadline-task", Attempt: 1, ApplicationID: "application-1", AppKey: cpaKey, Operation: "install", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`)}
	task.Authorization = executionAuthorizationForTest()
	task.Manifest.Version = "1.0.0"
	client := Client{executionSession: "test-execution-session", Capabilities: Capabilities{Docker: true}, Executor: executorFunc(func(ctx context.Context, _ DeploymentTask) (ApplicationTaskResult, error) {
		cancelExecution()
		return ApplicationTaskResult{}, nil
	})}
	var reported error
	client.processTaskWithLease(executionContext, store, task, func(err error) { reported = err })
	if !resultReceived {
		t.Fatal("task result was not delivered after execution cancellation")
	}
	if _, err := store.AppliedInstallation(context.Background(), cpaKey); !errors.Is(err, errApplicationNotInstalled) || reported == nil {
		t.Fatalf("cancelled execution persisted success or hid failure: %v, %v", err, reported)
	}
}

func TestLeaseRenewalFailureCancelsExecutionBeforeReporting(t *testing.T) {
	resultReceived := false
	server := controlFixtureServer(&controlGRPCFixture{
		transition: func(ctx context.Context, request *agentpb.ExecutionDocument) (*agentpb.Document, error) {
			return acknowledgeExecutionForTest(t, ctx, request)
		},
		lease: func(_ context.Context, request *agentpb.TaskDocument) (*agentpb.Document, error) {
			if request.AgentId != "agent-1" || request.TaskId != "lease-failure" {
				t.Error("unexpected lease identity")
			}
			return nil, status.Error(codes.FailedPrecondition, "lease unavailable")
		},
		complete: func(_ context.Context, request *agentpb.TaskDocument) (*agentpb.Document, error) {
			if request.AgentId != "agent-1" || request.TaskId != "lease-failure" {
				t.Error("unexpected result identity")
			}
			resultReceived = true
			return &agentpb.Document{Json: []byte(`{"completed":true}`)}, nil
		},
	})
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "node", server.URL, "credential")); err != nil {
		t.Fatal(err)
	}
	executionCanceled := false
	reportedRenewalFailure := false
	client := Client{executionSession: "test-execution-session", Capabilities: Capabilities{Docker: true}, Executor: executorFunc(func(ctx context.Context, _ DeploymentTask) (ApplicationTaskResult, error) {
		<-ctx.Done()
		executionCanceled = true
		return ApplicationTaskResult{}, ctx.Err()
	})}
	task := DeploymentTask{Kind: "application.apply", ID: "lease-failure", Attempt: 1, ApplicationID: "application-1", AppKey: cpaKey, Operation: "install", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`)}
	task.Authorization = executionAuthorizationForTest()
	client.processTaskWithLeaseInterval(context.Background(), store, task, func(err error) {
		if strings.Contains(err.Error(), "renew task lease") {
			reportedRenewalFailure = true
		}
	}, time.Millisecond)
	if !executionCanceled || !reportedRenewalFailure || !resultReceived {
		t.Fatalf("lease failure ordering: canceled=%t reported=%t result=%t", executionCanceled, reportedRenewalFailure, resultReceived)
	}
}

func TestNormalizeDeferredLoopbackCenterURLRejectsRemoteAddresses(t *testing.T) {
	normalized, err := NormalizeDeferredLoopbackCenterURL("http://127.0.0.1:8080/")
	if err != nil || normalized != "http://127.0.0.1:8080" {
		t.Fatalf("normalized loopback URL = %q, err=%v", normalized, err)
	}
	for _, candidate := range []string{"https://center.example.com", "http://192.0.2.10:8080", "http://127.0.0.1:8080/path?token=value"} {
		if _, err := NormalizeDeferredLoopbackCenterURL(candidate); err == nil {
			t.Fatalf("deferred verification accepted %q", candidate)
		}
	}
}
