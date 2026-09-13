package center

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestExecutionMigrationBacksUpAndFailsClosed(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		directory := t.TempDir()
		old := legacyMigrationStore(t, directory, 74)
		if conflict {
			if _, err := old.db.Exec(`CREATE TABLE task_executions(unexpected TEXT)`); err != nil {
				t.Fatal(err)
			}
		}
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
		store, err := Open(directory)
		if conflict && err == nil {
			store.Close()
			t.Fatal("accepted conflicting execution schema")
		}
		if !conflict {
			if err != nil {
				t.Fatal(err)
			}
			control, err := store.ExecutionClaimControl(context.Background())
			if err != nil || !control.Paused || control.Actor != "migration:75" || control.UpdatedAt == "" {
				t.Fatalf("migration did not pause protocol cutover: %+v %v", control, err)
			}
			var events int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_claim_control_events WHERE paused=1 AND actor='migration:75'`).Scan(&events); err != nil || events != 1 {
				t.Fatalf("migration pause audit: %d %v", events, err)
			}
			store.Close()
			store, err = Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if paused, err := executionClaimsPaused(context.Background(), store.db); err != nil || !paused {
				t.Fatalf("restart removed migration pause: %t %v", paused, err)
			}
			cookie, _, err := store.CreateFirstAdmin(context.Background(), "operator", "test-only-long-password")
			if err != nil {
				t.Fatal(err)
			}
			admin, err := store.SessionAdminID(context.Background(), cookie)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SetExecutionClaimControl(context.Background(), admin, false); err != nil {
				t.Fatal(err)
			}
			store.Close()
			store, err = Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if paused, err := executionClaimsPaused(context.Background(), store.db); err != nil || paused {
				t.Fatalf("restart reapplied migration pause after explicit resume: %t %v", paused, err)
			}
			store.Close()
		}
		backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", "center-v74-before-v75-*.db"))
		if err != nil || len(backups) != 1 {
			t.Fatalf("pre-migration backup missing: %v %v", backups, err)
		}
		db, err := sql.Open("sqlite", filepath.Join(directory, "center.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			t.Fatal(err)
		}
		want := 75
		if conflict {
			want = 74
			var partial int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='agent_execution_sessions'`).Scan(&partial); err != nil || partial != 0 {
				t.Fatalf("failed migration left a partial schema: %d %v", partial, err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='execution_claim_control'`).Scan(&partial); err != nil || partial != 0 {
				t.Fatalf("failed migration left partial claim control: %d %v", partial, err)
			}
		}
		if version != want {
			t.Fatalf("schema version=%d want=%d", version, want)
		}
		db.Close()
	}
}

func TestExecutionFreshDatabaseDoesNotRequireCutover(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	if paused, err := executionClaimsPaused(context.Background(), store.db); err != nil || paused {
		t.Fatalf("fresh installation unexpectedly paused: %t %v", paused, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_claim_control_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fresh installation has migration history: %d %v", count, err)
	}
}
