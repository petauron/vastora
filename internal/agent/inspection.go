package agent

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
)

// ConnectionSummary contains only non-secret enrollment metadata and opens the
// Agent database read-only. Installers use it before migration approval so an
// inspection can never initialize or migrate local state.
type ConnectionSummary struct {
	AgentID   string
	Name      string
	CenterURL string
}

func InspectConnection(dataDir string) (ConnectionSummary, error) {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return ConnectionSummary{}, fmt.Errorf("agent: resolve data directory: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(absolute, "agent.db"), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return ConnectionSummary{}, fmt.Errorf("agent: inspect state: %w", err)
	}
	defer db.Close()
	var summary ConnectionSummary
	err = db.QueryRow(`SELECT agent_id, name, center_url FROM control_plane_connection WHERE id = 1`).Scan(&summary.AgentID, &summary.Name, &summary.CenterURL)
	if errors.Is(err, sql.ErrNoRows) {
		return ConnectionSummary{}, errors.New("agent: not enrolled")
	}
	if err != nil {
		return ConnectionSummary{}, fmt.Errorf("agent: inspect Center connection: %w", err)
	}
	return summary, nil
}
