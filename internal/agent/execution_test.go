package agent

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Existing failure-ordering cases exercise the gRPC control transport.
type unavailableExecutionFixture struct {
	agentpb.UnimplementedAgentControlServer
}

func (unavailableExecutionFixture) Heartbeat(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error) {
	return nil, status.Error(codes.Unavailable, "Center unavailable")
}
func (unavailableExecutionFixture) TransitionExecution(context.Context, *agentpb.ExecutionDocument) (*agentpb.Document, error) {
	return nil, status.Error(codes.Unavailable, "Center unavailable")
}
func unavailableExecutionServer() *httptest.Server {
	server := grpc.NewServer()
	agentpb.RegisterAgentControlServer(server, unavailableExecutionFixture{})
	return httptest.NewServer(h2c.NewHandler(server, &http2.Server{}))
}

func TestExecutionControlFailureCancelsOnlyItsOwner(t *testing.T) {
	store := &Store{}
	first, abort, release, err := store.beginExecution(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.beginExecution(context.Background()); err == nil {
		t.Fatal("concurrent management executions accepted")
	}
	failure := errors.New("control connection failed")
	store.stopActiveExecution(failure)
	if !errors.Is(context.Cause(first), failure) {
		t.Fatal("control failure did not cancel current execution")
	}
	release()
	second, _, releaseSecond, err := store.beginExecution(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	abort(errors.New("late error from old request"))
	release()
	if second.Err() != nil {
		t.Fatal("old request cancelled a newer execution")
	}
	store.stopActiveExecution(failure)
	if !errors.Is(context.Cause(second), failure) {
		t.Fatal("old release cleared newer owner")
	}
}

func TestExecutionHTTPRequestErrorsStopActiveExecution(t *testing.T) {
	for _, method := range []string{"heartbeat", "transition"} {
		t.Run(method, func(t *testing.T) {
			server := unavailableExecutionServer()
			defer server.Close()
			store := &Store{}
			ctx, abort, release, err := store.beginExecution(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			client := Client{executionAbort: abort}
			operation := rpcHeartbeat
			if method == "transition" {
				operation = rpcExecutionTransition
			}
			err = client.callAgentRPC(ctx, Connection{CenterURL: server.URL, AgentID: "node", Credential: "credential"}, operation, "execution", map[string]string{}, nil, controlplane.MaxJSONPayload, false)
			if err == nil || ctx.Err() == nil {
				t.Fatalf("control failure did not stop execution: %v %v", err, ctx.Err())
			}
		})
	}
}

func TestExecutionHeartbeatFailureCancelsCurrentOperation(t *testing.T) {
	server := unavailableExecutionServer()
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(context.Background(), testConnection(t, "heartbeat-agent", "test", server.URL, "test-credential")); err != nil {
		t.Fatal(err)
	}
	ctx, _, release, err := store.beginExecution(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := (Client{}).Heartbeat(context.Background(), store); err == nil {
		t.Fatal("expected heartbeat failure")
	}
	if ctx.Err() == nil {
		t.Fatal("failed heartbeat left current operation authorized")
	}
}
