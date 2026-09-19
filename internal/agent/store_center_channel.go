package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const localCenterChannelName = "local-center-channel"

func (s *Store) LocalCenterChannel(centerURL string) (bool, error) {
	content, err := os.ReadFile(filepath.Join(s.dataDir, localCenterChannelName))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("agent: read local Center channel marker: %w", err)
	}
	return strings.TrimSpace(string(content)) == strings.TrimSpace(centerURL), nil
}

func (s *Store) SetLocalCenterChannel(centerURL string) error {
	path := filepath.Join(s.dataDir, localCenterChannelName)
	centerURL = strings.TrimSpace(centerURL)
	if centerURL == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("agent: remove local Center channel marker: %w", err)
		}
		return nil
	}
	temporary, err := os.CreateTemp(s.dataDir, ".local-center-channel-*")
	if err != nil {
		return fmt.Errorf("agent: create local Center channel marker: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(centerURL + "\n"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("agent: publish local Center channel marker: %w", err)
	}
	return nil
}
