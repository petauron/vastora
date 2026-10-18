//go:build linux || darwin

package main

import (
	"fmt"
	"syscall"
)

func requireAgentUpdateSpace(path string, bytes int64) error {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return fmt.Errorf("agent: inspect update filesystem space: %w", err)
	}
	return validateAgentUpdateSpace(uint64(stat.Bavail)*uint64(stat.Bsize), bytes)
}
