package landing

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestProbeDiagnosticsKeepStageAndSanitizeCause(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"deadline", &url.Error{Op: "Get", URL: "https://secret.invalid/token", Err: context.DeadlineExceeded}, "timeout"},
		{"refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "connection_refused"},
		{"reset", syscall.ECONNRESET, "connection_reset"},
		{"closed", io.ErrUnexpectedEOF, "connection_closed"},
		{"canceled", context.Canceled, "canceled"},
		{"certificate", &tls.CertificateVerificationError{Err: errors.New("secret certificate detail")}, "certificate_verification"},
		{"remote message", errors.New("secret credentials and response body"), "check_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := probeFailure("tls_handshake", test.err)
			var diagnostic *probeError
			if !errors.As(err, &diagnostic) || diagnostic.stage != "tls_handshake" || diagnostic.reason != test.want {
				t.Fatalf("wrong diagnosis: %v", err)
			}
			if strings.Contains(err.Error(), "secret") || !errors.Is(err, test.err) {
				t.Fatal("error exposed its cause or lost unwrap support")
			}
		})
	}
	err := &probeError{stage: "http_response", reason: "status_403"}
	if got := probeFailure("http_response", err); got != err {
		t.Fatalf("safe HTTP status was lost: %v", got)
	}
}

func TestProbeLogsNeverIncludeArbitraryErrorText(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	logProbeFailure(context.Background(), 7, 2, time.Now(), probeFailure("socks_connect", fmt.Errorf("secret detail: %w", context.DeadlineExceeded)))
	if text := output.String(); strings.Contains(text, "secret") || !strings.Contains(text, `"stage":"socks_connect"`) || !strings.Contains(text, `"reason":"timeout"`) || !strings.Contains(text, `"attempt":2`) {
		t.Fatalf("unexpected diagnostic: %s", text)
	}
}
