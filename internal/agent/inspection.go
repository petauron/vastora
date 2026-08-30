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

type InstallOperationSummary struct {
	ReplaceExisting bool
	Phase           string
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

// InspectInstallOperation reads only non-secret recovery metadata and never
// initializes or migrates the Agent database. The bootstrap installer uses it
// to resume a partially completed installation without turning that recovery
// into an unapproved Center migration.
func InspectInstallOperation(dataDir string) (InstallOperationSummary, bool, error) {
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return InstallOperationSummary{}, false, fmt.Errorf("agent: resolve data directory: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(absolute, "agent.db"), RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return InstallOperationSummary{}, false, fmt.Errorf("agent: inspect state: %w", err)
	}
	defer db.Close()
	var tableExists int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'agent_install_operations'`).Scan(&tableExists); err != nil {
		return InstallOperationSummary{}, false, fmt.Errorf("agent: inspect installation schema: %w", err)
	}
	if tableExists == 0 {
		return InstallOperationSummary{}, false, nil
	}
	var summary InstallOperationSummary
	err = db.QueryRow(`SELECT replace_existing, phase FROM agent_install_operations WHERE id = 1`).Scan(&summary.ReplaceExisting, &summary.Phase)
	if errors.Is(err, sql.ErrNoRows) {
		return InstallOperationSummary{}, false, nil
	}
	if err != nil {
		return InstallOperationSummary{}, false, fmt.Errorf("agent: inspect installation operation: %w", err)
	}
	if !validInstallPhase(summary.Phase) {
		return InstallOperationSummary{}, false, errors.New("agent: stored installation phase is invalid")
	}
	return summary, true, nil
}
