package center

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The host-owned pause list is loaded before any automatic update is queued.
// Missing or empty means all eligible Agents, including newly enrolled nodes.
// Only explicitly paused Agents are excluded; this is never an allowlist.
func readAgentUpdatePauses(dataDir string) (map[string]bool, error) {
	if _, err := os.Stat(filepath.Join(dataDir, "agent-update-scope.json")); err == nil {
		return nil, errors.New("center: retired Agent update allowlist exists; configure explicit pauses and remove agent-update-scope.json before startup")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("center: inspect retired Agent update allowlist: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "agent-update-pauses.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read Agent update pauses: %w", err)
	}
	var config struct {
		PausedAgentIDs *[]string `json:"pausedAgentIds"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("center: invalid Agent update pauses: %w", err)
	}
	if config.PausedAgentIDs == nil {
		return nil, errors.New("center: Agent update pauses requires pausedAgentIds array")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("center: trailing Agent update pauses content")
	}
	pauses := make(map[string]bool, len(*config.PausedAgentIDs))
	for _, id := range *config.PausedAgentIDs {
		if id == "" || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\t") || pauses[id] {
			return nil, errors.New("center: invalid or duplicate Agent update pauses ID")
		}
		pauses[id] = true
	}
	return pauses, nil
}

func (s *Store) agentUpdateAllowed(agentID string) bool {
	return !s.agentUpdatePauses[agentID]
}
