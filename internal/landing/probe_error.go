package landing

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"syscall"
	"time"
)

// Keep operational evidence without logging remote response bodies, URLs,
// credentials or an arbitrary error string supplied by a remote endpoint.
type probeError struct {
	stage, reason string
	cause         error
}

func (e *probeError) Error() string { return "landing: " + e.stage + " failed (" + e.reason + ")" }
func (e *probeError) Unwrap() error { return e.cause }

func probeFailure(stage string, err error) error {
	var existing *probeError
	if errors.As(err, &existing) {
		return existing
	}
	reason := "check_failed"
	var network net.Error
	var certificate *tls.CertificateVerificationError
	switch {
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "timeout"
	case errors.As(err, &network) && network.Timeout():
		reason = "timeout"
	case errors.Is(err, syscall.ECONNREFUSED):
		reason = "connection_refused"
	case errors.Is(err, syscall.ECONNRESET):
		reason = "connection_reset"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		reason = "connection_closed"
	case errors.As(err, &certificate):
		reason = "certificate_verification"
	}
	return &probeError{stage: stage, reason: reason, cause: err}
}

func logProbeFailure(ctx context.Context, revision uint64, attempt int, started time.Time, err error) {
	stage, reason := "readiness", "check_failed"
	var failure *probeError
	if errors.As(err, &failure) {
		stage, reason = failure.stage, failure.reason
	} else if errors.Is(err, context.DeadlineExceeded) {
		reason = "timeout"
	} else if errors.Is(err, context.Canceled) {
		reason = "canceled"
	}
	slog.WarnContext(ctx, "Landing check failed", "event", "landing.check", "revision", revision,
		"attempt", attempt, "stage", stage, "reason", reason, "elapsed_ms", time.Since(started).Milliseconds())
}
