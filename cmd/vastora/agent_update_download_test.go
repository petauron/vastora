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
	"os"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/sethvargo/go-retry"
)

type agentUpdateRoundTrip func(*http.Request) (*http.Response, error)

func (f agentUpdateRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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
	directory := t.TempDir()
	binary := "#!/bin/sh\nprintf '0.2.0\\n'\n"
	digest := sha256.Sum256([]byte(binary))
	calls, closed := 0, false
	client := &http.Client{Transport: agentUpdateRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer credential" {
			t.Fatal("retry lost update authentication")
		}
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("partial candidate survived before attempt %d: %v %v", calls, entries, err)
		}
		headers := http.Header{"X-Vastora-Version": {"0.2.0"}, "X-Vastora-Sha256": {fmt.Sprintf("%x", digest)}}
		var body io.ReadCloser = io.NopCloser(strings.NewReader(binary))
		if calls == 1 {
			body = interruptedAgentBody{Reader: strings.NewReader(binary[:8]), closed: &closed}
		} else if !closed {
			t.Fatal("first response was not closed before retry")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: body}, nil
	})}
	path, version, err := downloadAgentUpdateCandidate(context.Background(), client, agent.Connection{CenterURL: "https://center.example.test", AgentID: "node", Credential: "credential"}, directory)
	if err != nil || calls != 2 || version != "0.2.0" {
		t.Fatalf("retry failed: calls=%d version=%q err=%v", calls, version, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != binary {
		t.Fatalf("candidate changed: %v", err)
	}
}

func TestAgentUpdateDownloadBoundsRetriesAndHonorsCancellation(t *testing.T) {
	for _, cancelDuringWait := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelDuringWait), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if cancelDuringWait {
					go func() { time.Sleep(time.Second); cancel() }()
				}
				calls := 0
				client := &http.Client{Transport: agentUpdateRoundTrip(func(r *http.Request) (*http.Response, error) {
					calls++
					if _, ok := r.Context().Deadline(); !ok {
						t.Fatal("download has no total deadline")
					}
					return &http.Response{StatusCode: http.StatusServiceUnavailable, Status: "503 Service Unavailable", Body: io.NopCloser(strings.NewReader("restarting"))}, nil
				})}
				started := time.Now()
				_, _, err := downloadAgentUpdateCandidate(ctx, client, agent.Connection{CenterURL: "https://center.example.test", AgentID: "node"}, t.TempDir())
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
	for _, scenario := range []string{"unauthorized", "metadata", "digest", "disk", "certificate"} {
		t.Run(scenario, func(t *testing.T) {
			directory := t.TempDir()
			if scenario == "disk" {
				directory += "/missing"
			}
			calls := 0
			client := &http.Client{Transport: agentUpdateRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				if scenario == "certificate" {
					return nil, &tls.CertificateVerificationError{Err: errors.New("invalid certificate")}
				}
				response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"X-Vastora-Version": {"0.2.0"}, "X-Vastora-Sha256": {strings.Repeat("0", 64)}}, Body: io.NopCloser(strings.NewReader("untrusted candidate"))}
				if scenario == "unauthorized" {
					response.StatusCode = http.StatusUnauthorized
					response.Status = "401 Unauthorized"
				}
				if scenario == "metadata" {
					response.Header.Del("X-Vastora-Version")
				}
				return response, nil
			})}
			path, _, err := downloadAgentUpdateCandidate(context.Background(), client, agent.Connection{CenterURL: "https://center.example.test", AgentID: "node"}, directory)
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
