package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
	for _, method := range []string{"GET", "POST"} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
			defer server.Close()
			store := &Store{}
			ctx, abort, release, err := store.beginExecution(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			client := Client{HTTPClient: server.Client(), executionAbort: abort}
			if method == "GET" {
				err = client.get(ctx, server.URL, "", "", "", nil)
			} else {
				err = client.post(ctx, server.URL, map[string]string{}, "", "", "", nil)
			}
			if err == nil || ctx.Err() == nil {
				t.Fatalf("HTTP failure did not stop execution: %v %v", err, ctx.Err())
			}
		})
	}
}

func TestExecutionHeartbeatFailureCancelsCurrentOperation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
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
	if err := (Client{HTTPClient: server.Client()}).Heartbeat(context.Background(), store); err == nil {
		t.Fatal("expected heartbeat failure")
	}
	if ctx.Err() == nil {
		t.Fatal("failed heartbeat left current operation authorized")
	}
}
