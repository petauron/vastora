package main

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"github.com/sethvargo/go-retry"
)

// Retry only interrupted transport. Authorization, certificate, integrity,
// version, local disk and activation failures must not become retry loops.
func retryAgentDownloadError(err error) error {
	if errors.Is(err, context.Canceled) {
		return err
	}
	var network net.Error
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE) ||
		(errors.As(err, &network) && network.Timeout()) {
		return retry.RetryableError(err)
	}
	return err
}
