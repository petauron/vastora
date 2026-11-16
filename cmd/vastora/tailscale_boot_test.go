package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTailscaleBootOrderIsIdempotentAndRemovable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tailscaled.service.d")
	path := filepath.Join(dir, "92-vastora-network-online.conf")
	var commands []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		commands = append(commands, name+" "+args[0])
		return nil, nil
	}
	if err := manageTailscaleBootOrder(context.Background(), path, true, run); err != nil {
		t.Fatal(err)
	}
	if err := manageTailscaleBootOrder(context.Background(), path, true, run); err != nil || len(commands) != 1 {
		t.Fatal("unchanged ordering caused extra work", err)
	}
	if err := manageTailscaleBootOrder(context.Background(), path, false, run); err != nil || len(commands) != 2 {
		t.Fatal("owned ordering was not removed", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("owned ordering remains after removal")
	}
}
