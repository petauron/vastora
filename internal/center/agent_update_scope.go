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

// The host-owned policy is loaded once, before serving requests. Keeping it in
// the persistent data directory also fences the first startup after an upgrade.
// Missing means the existing all-node policy; an explicit empty list means none.
func readAgentUpdateScope(dataDir string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "agent-update-scope.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read Agent update scope: %w", err)
	}
	var config struct {
		AgentIDs *[]string `json:"agentIds"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("center: invalid Agent update scope: %w", err)
	}
	if config.AgentIDs == nil {
		return nil, errors.New("center: Agent update scope requires agentIds array")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("center: trailing Agent update scope content")
	}
	scope := make(map[string]bool, len(*config.AgentIDs))
	for _, id := range *config.AgentIDs {
		if id == "" || strings.TrimSpace(id) != id || strings.ContainsAny(id, "\r\n\t") || scope[id] {
			return nil, errors.New("center: invalid or duplicate Agent update scope ID")
		}
		scope[id] = true
	}
	return scope, nil
}

func (s *Store) agentUpdateAllowed(agentID string) bool {
	return s.agentUpdateScope == nil || s.agentUpdateScope[agentID]
}
