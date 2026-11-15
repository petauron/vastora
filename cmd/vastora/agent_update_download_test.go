package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/agentpb"
	"github.com/sethvargo/go-retry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Existing host lifecycle cases invoke typed gRPC service methods.
type hostControlGRPCFixture struct {
	agentpb.UnimplementedAgentControlServer
	binary            func(*agentpb.BinaryRequest, agentpb.AgentControl_DownloadAgentBinaryServer) error
	transition        func(context.Context, *agentpb.ExecutionDocument) (*agentpb.Document, error)
	complete          func(context.Context, *agentpb.TaskDocument) (*agentpb.Document, error)
	beginUpdate       func(context.Context, *agentpb.TaskDocument) (*agentpb.Document, error)
	beginDecommission func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error)
	callback          func(context.Context, *agentpb.DecommissionRequest) (*agentpb.Document, error)
}

func (f *hostControlGRPCFixture) DownloadAgentBinary(in *agentpb.BinaryRequest, stream agentpb.AgentControl_DownloadAgentBinaryServer) error {
	if f.binary == nil {
		return status.Error(codes.Unimplemented, "binary")
	}
	return f.binary(in, stream)
}
func (f *hostControlGRPCFixture) TransitionExecution(ctx context.Context, in *agentpb.ExecutionDocument) (*agentpb.Document, error) {
	if f.transition == nil {
		return nil, status.Error(codes.Unimplemented, "transition")
	}
	return f.transition(ctx, in)
}
func (f *hostControlGRPCFixture) CompleteTask(ctx context.Context, in *agentpb.TaskDocument) (*agentpb.Document, error) {
	if f.complete == nil {
		return nil, status.Error(codes.Unimplemented, "complete")
	}
	return f.complete(ctx, in)
}
func (f *hostControlGRPCFixture) BeginAgentUpdate(ctx context.Context, in *agentpb.TaskDocument) (*agentpb.Document, error) {
	if f.beginUpdate == nil {
		return nil, status.Error(codes.Unimplemented, "update")
	}
	return f.beginUpdate(ctx, in)
}
func (f *hostControlGRPCFixture) BeginDecommission(ctx context.Context, in *agentpb.AgentDocument) (*agentpb.Document, error) {
	if f.beginDecommission == nil {
		return nil, status.Error(codes.Unimplemented, "decommission")
	}
	return f.beginDecommission(ctx, in)
}
func (f *hostControlGRPCFixture) DecommissionCallback(ctx context.Context, in *agentpb.DecommissionRequest) (*agentpb.Document, error) {
	if f.callback == nil {
		return nil, status.Error(codes.Unimplemented, "callback")
	}
	return f.callback(ctx, in)
}

// grpcTestHTTPServer serves existing fixtures over HTTP/1 and cleartext HTTP/2.
func grpcTestHTTPServer(handler http.Handler) *httptest.Server {
	server := httptest.NewUnstartedServer(handler)
	server.Config.Protocols = new(http.Protocols)
	server.Config.Protocols.SetHTTP1(true)
	server.Config.Protocols.SetUnencryptedHTTP2(true)
	server.Start()
	return server
}

func hostControlFixtureServer(f *hostControlGRPCFixture) *httptest.Server {
	server := grpc.NewServer()
	agentpb.RegisterAgentControlServer(server, f)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/agent-decommission-results/") {
			if i := strings.Index(r.URL.Path, "/vastora.agent.v1.AgentControl/"); i >= 0 {
				r = r.Clone(r.Context())
				r.URL.Path = r.URL.Path[i:]
			}
		}
		server.ServeHTTP(w, r)
	})
	return grpcTestHTTPServer(handler)
}
func requireHostFixtureCredential(t *testing.T, ctx context.Context, expected string) {
	t.Helper()
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get("authorization")
	if len(values) != 1 || values[0] != "Bearer "+expected {
		t.Error("unexpected control credential")
	}
}

type interruptedAgentBody struct {
	io.Reader
	closed *bool
}

func (b interruptedAgentBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

func (b interruptedAgentBody) Close() error { *b.closed = true; return nil }

func TestAgentUpdateDownloadRetriesInterruptedBodyAndRevalidatesCandidate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Agent update candidates require a Linux build target")
	}
	directory := t.TempDir()
	binary := "#!/bin/sh\nprintf '0.2.0\\n'\n"
	digest := sha256.Sum256([]byte(binary))
	calls, closed := 0, false
	open := func(_ context.Context, connection agent.Connection, _, _ string) (io.ReadCloser, string, string, int64, error) {
		calls++
		if connection.Credential != "credential" {
			t.Fatal("retry lost update authentication")
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("partial candidate survived before attempt %d: %v %v", calls, entries, err)
		}
		var body io.ReadCloser = io.NopCloser(strings.NewReader(binary))
		if calls == 1 {
			body = interruptedAgentBody{Reader: strings.NewReader(binary[:8]), closed: &closed}
		} else if !closed {
			t.Fatal("first response was not closed before retry")
		}
		return body, "0.2.0", fmt.Sprintf("%x", digest), int64(len(binary)), nil
	}
	path, version, err := downloadAgentUpdateCandidate(context.Background(), agent.Connection{CenterURL: "https://center.example.test", AgentID: "node", Credential: "credential"}, directory, open)
	if err != nil || calls != 2 || version != "0.2.0" {
		t.Fatalf("retry failed: calls=%d version=%q err=%v", calls, version, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != binary {
		t.Fatalf("candidate changed: %v", err)
	}
}

func TestAgentUpdateDownloadBoundsRetriesAndHonorsCancellation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Agent update candidates require a Linux build target")
	}
	for _, cancelDuringWait := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDuringWait), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cancelDuringWait {
					timer := time.NewTimer(time.Second)
					defer timer.Stop()
					done := make(chan struct{})
					go func() {
						defer close(done)
						select {
						case <-timer.C:
							cancel()
						case <-ctx.Done():
						}
					}()
					defer func() { cancel(); <-done }()
				}
				calls := 0
				open := func(ctx context.Context, _ agent.Connection, _, _ string) (io.ReadCloser, string, string, int64, error) {
					calls++
					if _, ok := ctx.Deadline(); !ok {
						t.Fatal("download has no total deadline")
					}
					return nil, "", "", 0, io.ErrUnexpectedEOF
				}
				started := time.Now()
				_, _, err := downloadAgentUpdateCandidate(ctx, agent.Connection{CenterURL: "https://center.example.test", AgentID: "node"}, t.TempDir(), open)
				if cancelDuringWait {
					if !errors.Is(err, context.Canceled) || calls != 1 || time.Since(started) != time.Second {
						t.Fatalf("cancellation ignored: calls=%d elapsed=%s err=%v", calls, time.Since(started), err)
					}
				} else if err == nil || calls != 4 || time.Since(started) != 14*time.Second {
					t.Fatalf("retry budget changed: calls=%d elapsed=%s err=%v", calls, time.Since(started), err)
				}
			})
		})
	}
}

func TestAgentUpdateDownloadDoesNotRetryPermanentFailures(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Agent update candidates require a Linux build target")
	}
	for _, scenario := range []string{"unauthorized", "metadata", "digest", "disk", "certificate"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			if scenario == "disk" {
				directory += "/missing"
			}
			calls := 0
			open := func(context.Context, agent.Connection, string, string) (io.ReadCloser, string, string, int64, error) {
				calls++
				if scenario == "certificate" {
					return nil, "", "", 0, &tls.CertificateVerificationError{Err: errors.New("invalid certificate")}
				}
				if scenario == "unauthorized" {
					return nil, "", "", 0, status.Error(codes.Unauthenticated, "unauthorized")
				}
				version := "0.2.0"
				if scenario == "metadata" {
					version = ""
				}
				return io.NopCloser(strings.NewReader("untrusted candidate")), version, strings.Repeat("0", 64), int64(len("untrusted candidate")), nil
			}
			path, _, err := downloadAgentUpdateCandidate(context.Background(), agent.Connection{CenterURL: "https://center.example.test", AgentID: "node"}, directory, open)
			if err == nil || path != "" || calls != 1 {
				t.Fatalf("permanent failure retried: calls=%d path=%q err=%v", calls, path, err)
			}
		})
	}
}

func TestAgentUpdateDownloadClassifiesTransportErrorsOnly(t *testing.T) {
	for _, test := range []struct {
		err   error
		retry bool
	}{
		{syscall.ECONNRESET, true}, {syscall.ECONNREFUSED, true}, {syscall.EPIPE, true},
		{io.EOF, true}, {io.ErrUnexpectedEOF, true},
		{&net.OpError{Op: "read", Err: context.DeadlineExceeded}, true},
		{context.Canceled, false}, {syscall.ENOSPC, false}, {syscall.EACCES, false},
		{errors.New("agent update integrity check failed"), false},
	} {
		calls := 0
		err := retry.Do(context.Background(), retry.WithMaxRetries(1, retry.BackoffFunc(func() (time.Duration, bool) { return 0, false })), func(context.Context) error {
			calls++
			return retryAgentDownloadError(test.err)
		})
		want := 1
		if test.retry {
			want = 2
		}
		if calls != want || !errors.Is(err, test.err) {
			t.Fatalf("classification: %v calls=%d want=%d result=%v", test.err, calls, want, err)
		}
	}
}
