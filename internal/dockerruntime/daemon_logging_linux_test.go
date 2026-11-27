//go:build linux

package dockerruntime

import (
	"path/filepath"
	"syscall"
	"testing"
)

func TestEnsureDefaultDaemonLoggingRejectsFIFOWithoutBlocking(t *testing.T) {
	fifoPath := filepath.Join(t.TempDir(), "daemon.json")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDefaultDaemonLogging(DaemonLoggingOptions{ConfigPath: fifoPath}); err == nil {
		t.Fatal("FIFO at daemon.json was accepted")
	}
}
