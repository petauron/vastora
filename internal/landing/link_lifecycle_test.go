package landing

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLinkCheckerOversizedResponsesReleaseBeforeNextRequest(t *testing.T) {
	for _, mode := range []string{"content-length", "chunked", "rejected-status"} {
		t.Run(mode, func(t *testing.T) {
			var healthy atomic.Bool
			chunk := bytes.Repeat([]byte("x"), 64<<10)
			prefix := []byte(`{"BackendState":"Running","Self":{"ID":"self","PublicKey":"key","TailscaleIPs":["100.64.0.1"]},"Padding":"`)
			suffix := []byte(`"}`)
			checker, open, _ := unixChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if healthy.Load() {
					w.Write([]byte(`{"BackendState":"Running","Self":{"ID":"self","PublicKey":"key","TailscaleIPs":["100.64.0.1"]}}`))
					return
				}
				if mode == "content-length" {
					w.Header().Set("Content-Length", strconv.Itoa(len(prefix)+(8<<20)+len(suffix)))
				}
				if mode == "rejected-status" {
					w.WriteHeader(http.StatusServiceUnavailable)
				} else {
					w.WriteHeader(http.StatusOK)
				}
				w.(http.Flusher).Flush()
				if _, err := w.Write(prefix); err != nil {
					return
				}
				// Stream bounded test data instead of allocating an oversized body.
				for range 128 {
					if r.Context().Err() != nil {
						return
					}
					if _, err := w.Write(chunk); err != nil {
						return
					}
				}
				w.Write(suffix)
			}))
			for range 3 {
				if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err == nil {
					t.Fatal("oversized or rejected response accepted")
				}
				// No checker.Close/GC between requests: aborted bodies must close
				// their own connections, not wait for final owner shutdown.
				awaitNoConnections(t, open)
			}
			healthy.Store(true)
			if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err != nil {
				t.Fatalf("checker did not recover after invalid responses: %v", err)
			}
			checker.Close()
			awaitNoConnections(t, open)
		})
	}
}

func unixChecker(t *testing.T, handler http.Handler) (*LinkChecker, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	// Keep the socket path below the macOS sockaddr_un length limit.
	directory, err := os.MkdirTemp("", "lc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	path := filepath.Join(directory, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var open, accepted atomic.Int64
	server := httptest.NewUnstartedServer(handler)
	server.Listener.Close()
	server.Listener = listener
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			open.Add(1)
			accepted.Add(1)
		case http.StateClosed:
			open.Add(-1)
		}
	}
	server.Start()
	t.Cleanup(server.Close)
	checker := NewLinkChecker()
	transport := checker.HTTPClient.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: CheckTimeout}).DialContext(ctx, "unix", path)
	}
	t.Cleanup(checker.Close)
	return checker, &open, &accepted
}

func awaitNoConnections(t *testing.T, open *atomic.Int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for open.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if open.Load() != 0 {
		t.Fatalf("connections remained after Close: %d", open.Load())
	}
}

func TestLinkCheckerReusesUnixConnectionAndClosesIt(t *testing.T) {
	checker, open, accepted := unixChecker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"BackendState":"Running","Self":{"ID":"self","PublicKey":"key","TailscaleIPs":["100.64.0.1"]}}`))
	}))
	for i := 0; i < 200; i++ {
		if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	if accepted.Load() != 1 {
		t.Fatalf("periodic identity reads created %d connections", accepted.Load())
	}
	checker.Close()
	awaitNoConnections(t, open)
	if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err == nil {
		t.Fatal("closed checker accepted a request")
	}
	checker.Close()
}

func TestLinkCheckerErrorResponsesReleaseConnections(t *testing.T) {
	for _, mode := range []string{"status", "json", "identity"} {
		t.Run(mode, func(t *testing.T) {
			checker, open, _ := unixChecker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if mode == "status" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				if mode == "json" {
					w.Write([]byte(`{broken`))
					return
				}
				w.Write([]byte(`{"BackendState":"Stopped"}`))
			}))
			for i := 0; i < 20; i++ {
				if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err == nil {
					t.Fatal("invalid response accepted")
				}
			}
			checker.Close()
			awaitNoConnections(t, open)
		})
	}
}

func TestLinkCheckerCloseCancelsActiveRequest(t *testing.T) {
	started := make(chan struct{})
	checker, open, _ := unixChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	done := make(chan error, 1)
	go func() { _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	checker.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("request survived Close")
	}
	awaitNoConnections(t, open)
}

func TestLinkCheckerTransportIsBounded(t *testing.T) {
	checker := NewLinkChecker()
	defer checker.Close()
	transport := checker.HTTPClient.Transport.(*http.Transport)
	if transport.MaxConnsPerHost != 8 || transport.MaxIdleConns != 8 || transport.MaxIdleConnsPerHost != 8 || transport.IdleConnTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 {
		t.Fatal("checker transport lost connection or timeout bounds")
	}
}

func TestLinkCheckerConcurrentRequestsAndQueuedCancellationAreBounded(t *testing.T) {
	started := make(chan struct{}, 32)
	checker, open, accepted := unixChecker(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
	}))
	var requests sync.WaitGroup
	for i := 0; i < 32; i++ {
		requests.Add(1)
		go func() {
			defer requests.Done()
			if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err == nil {
				t.Error("cancelled request unexpectedly succeeded")
			}
		}()
	}
	for i := 0; i < 8; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("connection slots did not fill")
		}
	}
	if accepted.Load() > 8 {
		t.Fatalf("connection limit exceeded: %d", accepted.Load())
	}
	checker.Close()
	requests.Wait()
	awaitNoConnections(t, open)
	checker.mu.Lock()
	remaining := len(checker.requests)
	checker.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("request registry retained %d requests", remaining)
	}
}

func TestLinkCheckerReplacementDoesNotAccumulateConnectionsOrGoroutines(t *testing.T) {
	base, open, accepted := unixChecker(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"BackendState":"Running","Self":{"ID":"self","PublicKey":"key","TailscaleIPs":["100.64.0.1"]}}`))
	}))
	dial := base.HTTPClient.Transport.(*http.Transport).DialContext
	base.Close()
	baseline := runtime.NumGoroutine()
	for batch := 0; batch < 5; batch++ {
		for i := 0; i < 10; i++ {
			checker := NewLinkChecker()
			checker.HTTPClient.Transport.(*http.Transport).DialContext = dial
			for request := 0; request < 10; request++ {
				if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err != nil {
					checker.Close()
					t.Fatal(err)
				}
			}
			checker.Close()
			awaitNoConnections(t, open)
		}
		deadline := time.Now().Add(time.Second)
		for runtime.NumGoroutine() > baseline+4 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if count := runtime.NumGoroutine(); count > baseline+4 {
			t.Fatalf("batch %d retained goroutines: baseline=%d current=%d", batch, baseline, count)
		}
	}
	if accepted.Load() != 50 {
		t.Fatalf("replacement pools did not reuse their connection: %d", accepted.Load())
	}
}

func TestLinkCheckerTruncatedAndTimedOutResponsesCloseConnections(t *testing.T) {
	for _, slow := range []bool{false, true} {
		checker, open, _ := unixChecker(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if slow {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Length", "100")
			w.Write([]byte(`{}`))
		}))
		checker.HTTPClient.Timeout = 20 * time.Millisecond
		if _, err := checker.SelfIdentity(context.Background(), "100.64.0.1"); err == nil {
			checker.Close()
			t.Fatal("incomplete response was accepted")
		}
		checker.Close()
		awaitNoConnections(t, open)
	}
}
