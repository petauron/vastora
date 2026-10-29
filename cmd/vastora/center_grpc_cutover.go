package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Check the live database without opening the Center store, which would run
// migrations. Independent access to each old Agent must also be verified by
// the operator before invoking the coordinated upgrade.
func runCenterGRPCCutoverCheck(arguments []string) error {
	flags := flag.NewFlagSet("center check-grpc-cutover", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	dataDir := flags.String("data-dir", "", "existing Center state directory")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if !filepath.IsAbs(*dataDir) || flags.NArg() != 0 {
		return errors.New("center: an absolute existing state directory is required")
	}
	databasePath := filepath.Join(filepath.Clean(*dataDir), "center.db")
	info, err := os.Stat(databasePath)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("center: existing Center database is not accessible")
	}
	dsn := (&url.URL{Scheme: "file", Path: databasePath, RawQuery: "mode=ro"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("center: open existing database read-only: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='execution_claim_control'`).Scan(&raw); err != nil {
		return errors.New("center: pause task claims before the gRPC cutover")
	}
	var pause struct {
		Paused *bool  `json:"paused"`
		Actor  string `json:"actor"`
	}
	if json.Unmarshal([]byte(raw), &pause) != nil || pause.Paused == nil || !*pause.Paused || strings.TrimSpace(pause.Actor) == "" || strings.HasPrefix(strings.TrimSpace(pause.Actor), "migration:") {
		return errors.New("center: task claims must be paused by an administrator before the gRPC cutover")
	}
	checks := []struct {
		name  string
		query string
	}{
		{"unfinished task executions", `SELECT COUNT(*) FROM task_executions WHERE disposition='' AND state IN ('offered','running','helper_running','unknown')`},
		{"Agent updates", `SELECT COUNT(*) FROM agent_updates WHERE state IN ('running','installing')`},
		{"Agent removals", `SELECT COUNT(*) FROM agent_decommissions WHERE state IN ('running','cleaning')`},
	}
	for _, check := range checks {
		var count int
		if err := tx.QueryRowContext(ctx, check.query).Scan(&count); err != nil {
			return fmt.Errorf("center: inspect %s: %w", check.name, err)
		}
		if count != 0 {
			return fmt.Errorf("center: %d %s need to finish or be resolved before cutover", count, check.name)
		}
	}
	return tx.Commit()
}
