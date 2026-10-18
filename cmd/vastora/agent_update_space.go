package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func validateAgentUpdateSpace(available uint64, required int64) error {
	if required <= 0 || available < uint64(required) {
		return fmt.Errorf("agent: insufficient update space: need %d bytes, available %d; current service was not replaced", required, available)
	}
	return nil
}

// Budget actual binary and SQLite sizes on each potentially separate filesystem.
// This is a conservative staging budget, not a minimum VPS disk-size promise.
func checkHostUpdateSpace(candidate string, operation hostUpdateOperation) error {
	binary, err := os.Stat(candidate)
	if err != nil {
		return err
	}
	var databaseBytes int64
	for _, name := range []string{"agent.db", "agent.db-wal", "agent.key"} {
		info, err := os.Stat(filepath.Join(operation.DataDir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		databaseBytes += info.Size()
	}
	required := 4*binary.Size() + 2*databaseBytes + 16<<20
	for _, path := range []string{filepath.Dir(candidate), filepath.Dir(operation.Executable), operation.DataDir} {
		if err := requireAgentUpdateSpace(path, required); err != nil {
			return err
		}
	}
	return nil
}
