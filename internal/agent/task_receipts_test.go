package agent

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/platform"
)

// Historical fixtures constructed from Open must remove indexes introduced by
// schema 19, as well as any later tables/columns removed by the individual test.
func dropTaskReceiptIndexesForFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP INDEX task_receipts_pending_completion;
		DROP INDEX task_receipts_unresolved_application;
		DROP INDEX task_receipts_expiry;`); err != nil {
		t.Fatal(err)
	}
}

func seedAcknowledgedReceiptHistory(t *testing.T, store *Store, count int, updatedAt time.Time) {
	t.Helper()
	// Opaque historical payloads need not be decoded by any lookup or cleanup.
	if _, err := store.db.Exec(`WITH RECURSIVE history(n) AS (
		SELECT 1 UNION ALL SELECT n + 1 FROM history WHERE n < ?
	) INSERT INTO task_receipts(task_id, task_kind, runtime_generation, attempt, task_hash, state, sealed_completion, created_at, updated_at)
		SELECT printf('history-%06d', n), 'node.listener.apply', 0, 1, X'01', 'acknowledged', zeroblob(4096), ?, ? FROM history`,
		count, updatedAt.UTC().Format(time.RFC3339Nano), updatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func assertTaskReceiptQueryPlans(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, test := range []struct {
		name, query, index string
		args               []any
	}{
		{"outbox", pendingTaskCompletionSQL, "task_receipts_pending_completion", nil},
		{"startup fence", unresolvedTaskReceiptSQL, "task_receipts_unresolved_application", nil},
		{"expiry", pruneTaskReceiptsSQL, "task_receipts_expiry", []any{"2026-09-01T00:00:00Z", taskReceiptPruneBatchSize}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := db.Query("EXPLAIN QUERY PLAN "+test.query, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			usedIndex := false
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				// An ordered partial-index SCAN with LIMIT 1 is fine; a scan of
				// the historical table or a separate sort is the regression.
				if strings.Contains(detail, "TEMP B-TREE") || (strings.HasPrefix(detail, "SCAN task_receipts") && !strings.Contains(detail, "USING")) {
					t.Fatalf("unbounded receipt query plan: %s", detail)
				}
				usedIndex = usedIndex || strings.Contains(detail, test.index)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !usedIndex {
				t.Fatalf("query did not use %s", test.index)
			}
		})
	}
}

func TestTaskReceiptQueriesUsePartialIndexesWithHistory(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	seedAcknowledgedReceiptHistory(t, store, 512, now.Add(-time.Hour))
	assertTaskReceiptQueryPlans(t, store.db)
	if pending, err := store.PendingTaskCompletion(ctx); err != nil || pending != nil {
		t.Fatalf("empty outbox with history: %#v, %v", pending, err)
	}
	if id, _, err := store.UnresolvedApplicationTaskReceipt(ctx); err != nil || id != "" {
		t.Fatalf("history fenced startup: %q, %v", id, err)
	}
	// Same timestamp: retain the task_id tie-breaker across pending states.
	for _, id := range []string{"z-pending", "a-reconciliation"} {
		task := DeploymentTask{ID: id, Kind: "application.apply", Attempt: 1}
		if _, err := store.PrepareTaskReceipt(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := store.RecordTaskCompletion(ctx, TaskCompletion{
			TaskID: id, Attempt: 1, ReconciliationRequired: id == "a-reconciliation",
			ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration,
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertTaskReceiptQueryPlans(t, store.db)
	if pending, err := store.PendingTaskCompletion(ctx); err != nil || pending == nil || pending.TaskID != "a-reconciliation" {
		t.Fatalf("outbox order: %#v, %v", pending, err)
	}
	if err := store.AcknowledgeTaskCompletion(ctx, "a-reconciliation"); err != nil {
		t.Fatal(err)
	}
	if pending, err := store.PendingTaskCompletion(ctx); err != nil || pending == nil || pending.TaskID != "z-pending" {
		t.Fatalf("acknowledged reconciliation stayed in outbox: %#v, %v", pending, err)
	}
	if id, _, err := store.UnresolvedApplicationTaskReceipt(ctx); err != nil || id != "a-reconciliation" {
		t.Fatalf("acknowledgement cleared unresolved effect: %q, %v", id, err)
	}
}

func TestTaskReceiptPruningIsBoundedThrottledAndPreservesRecovery(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	cutoff := now.Add(-taskReceiptRetention)
	seedAcknowledgedReceiptHistory(t, store, taskReceiptPruneBatchSize*2+1, cutoff.Add(-time.Hour))
	for _, receipt := range []struct{ id, kind, state string }{
		{"pending", "application.apply", "completed"},
		{"uncertain", "application.apply", "reconciliation_required"},
		{"uncertain-acked", "application.apply", "reconciliation_acknowledged"},
		{"processing-app", "application.apply", "processing"},
		{"processing-legacy", "legacy", "processing"},
		{"processing-host", "agent.decommission", "processing"},
		{"processing-command", "application.command", "processing"},
		{"expired-update", "agent.update", "processing"},
	} {
		at := cutoff.Add(-2 * time.Hour).Format(time.RFC3339Nano)
		if _, err := store.db.Exec(`INSERT INTO task_receipts(task_id, task_kind, runtime_generation, attempt, task_hash, state, created_at, updated_at)
			VALUES(?, ?, 0, 1, X'01', ?, ?, ?)`, receipt.id, receipt.kind, receipt.state, at, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"boundary-acked", "boundary-update"} {
		kind, state := "application.apply", "acknowledged"
		if id == "boundary-update" {
			kind, state = "agent.update", "processing"
		}
		if _, err := store.db.Exec(`INSERT INTO task_receipts(task_id, task_kind, runtime_generation, attempt, task_hash, state, created_at, updated_at)
			VALUES(?, ?, 0, 1, X'01', ?, ?, ?)`, id, kind, state, cutoff.Format(time.RFC3339Nano), cutoff.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		t.Helper()
		var n int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_receipts`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	// Receipt acquisition must not do maintenance, even with a large backlog.
	if _, err := store.PrepareTaskReceipt(ctx, DeploymentTask{ID: "new-task", Kind: "node.listener.apply", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != before+1 {
		t.Fatal("task acquisition pruned history")
	}
	before++
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.maintainTaskReceipts(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled cleanup: %v", err)
	}
	// Concurrent callers may delete only one batch per interval.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.maintainTaskReceipts(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := count(); got != before-taskReceiptPruneBatchSize {
		t.Fatalf("pruned %d rows, want %d", before-got, taskReceiptPruneBatchSize)
	}
	for _, id := range []string{"pending", "uncertain", "uncertain-acked", "processing-app", "processing-legacy", "processing-host", "processing-command", "boundary-acked", "boundary-update", "new-task"} {
		var n int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_receipts WHERE task_id = ?`, id).Scan(&n); err != nil || n != 1 {
			t.Fatalf("protected receipt %q removed: count=%d err=%v", id, n, err)
		}
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_receipts WHERE task_id = 'expired-update'`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("expired update receipt was not pruned: %d %v", remaining, err)
	}
	now = now.Add(taskReceiptPruneInterval - time.Nanosecond)
	if err := store.maintainTaskReceipts(ctx); err != nil || count() != before-taskReceiptPruneBatchSize {
		t.Fatalf("cleanup was not throttled: %v", err)
	}
	now = now.Add(time.Nanosecond)
	if err := store.maintainTaskReceipts(ctx); err != nil || count() != before-2*taskReceiptPruneBatchSize {
		t.Fatalf("next batch did not resume: %v", err)
	}
}

func TestTaskReceiptPruneFailureDoesNotBlockTaskRecordingOrBusyRetry(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now()
	store.now = func() time.Time { return now }
	seedAcknowledgedReceiptHistory(t, store, 1, now.Add(-taskReceiptRetention-time.Hour))
	if _, err := store.db.Exec(`CREATE TRIGGER fail_receipt_prune BEFORE DELETE ON task_receipts
		BEGIN SELECT RAISE(ABORT, 'simulated cleanup failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.maintainTaskReceipts(ctx); err == nil || !strings.Contains(err.Error(), "simulated cleanup failure") {
		t.Fatalf("cleanup error was hidden: %v", err)
	}
	if err := store.maintainTaskReceipts(ctx); err != nil {
		t.Fatalf("failed cleanup retried immediately: %v", err)
	}
	if _, err := store.PrepareTaskReceipt(ctx, DeploymentTask{ID: "record-on-prune-error", Kind: "application.apply", Attempt: 1}); err != nil {
		t.Fatalf("cleanup blocked durable intent: %v", err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_receipt_prune`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(taskReceiptPruneInterval)
	if err := store.maintainTaskReceipts(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_receipts WHERE state = 'acknowledged'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cleanup did not recover: %d %v", n, err)
	}
}

func TestTaskReceiptSchemaV19BacksUpWALAndPreservesReplay(t *testing.T) {
	directory := t.TempDir()
	old, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	dropTaskReceiptIndexesForFixture(t, old.db)
	if _, err := old.db.Exec(`PRAGMA user_version = 18; PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task := DeploymentTask{ID: "pending-before-upgrade", Kind: "application.apply", Attempt: 2}
	if _, err := old.PrepareTaskReceipt(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := old.RecordTaskCompletion(ctx, TaskCompletion{TaskID: task.ID, Attempt: 2, Error: "saved result", ApplicationRuntimeGeneration: platform.ApplicationRuntimeGeneration}); err != nil {
		t.Fatal(err)
	}
	var sealedBefore []byte
	if err := old.db.QueryRow(`SELECT sealed_completion FROM task_receipts WHERE task_id = ?`, task.ID).Scan(&sealedBefore); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(directory, "agent.db-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("fixture has no committed WAL data: %v", err)
	}
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertTaskReceiptQueryPlans(t, store.db)
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != agentSchemaVersion {
		t.Fatalf("migrated version=%d: %v", version, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "schema-18-backup-*", "agent.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup: %v %v", backups, err)
	}
	if info, err := os.Stat(filepath.Dir(backups[0])); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("backup directory must be private: %v", err)
	}
	backup, err := sql.Open("sqlite", backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
		t.Fatalf("backup version=%d: %v", version, err)
	}
	for name, db := range map[string]*sql.DB{"backup": backup, "migrated": store.db} {
		var sealed []byte
		if err := db.QueryRow(`SELECT sealed_completion FROM task_receipts WHERE task_id = ?`, task.ID).Scan(&sealed); err != nil || !bytes.Equal(sealedBefore, sealed) {
			t.Fatalf("%s lost or rewrote committed completion: %v", name, err)
		}
	}
	if pending, err := store.PendingTaskCompletion(ctx); err != nil || pending == nil || pending.TaskID != task.ID || pending.Error != "saved result" || pending.Attempt != 2 {
		t.Fatalf("migrated outbox cannot replay: %#v %v", pending, err)
	}
	if err := store.AcknowledgeTaskCompletion(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.maintainTaskReceipts(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if pending, err := store.PendingTaskCompletion(ctx); err != nil || pending != nil {
		t.Fatalf("acknowledged result reappeared: %#v %v", pending, err)
	}
	if completion, err := store.PrepareTaskReceipt(ctx, task); err != nil || completion == nil || completion.Error != "saved result" {
		t.Fatalf("restart/maintenance lost deduplication: %#v %v", completion, err)
	}
	if backups, err := filepath.Glob(filepath.Join(directory, "schema-18-backup-*", "agent.db")); err != nil || len(backups) != 1 {
		t.Fatalf("reopen repeated migration: %v %v", backups, err)
	}
}

func TestTaskReceiptSchemaV19FailsClosed(t *testing.T) {
	for _, failure := range []string{"backup", "index"} {
		t.Run(failure, func(t *testing.T) {
			directory := t.TempDir()
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			dropTaskReceiptIndexesForFixture(t, store.db)
			if _, err := store.db.Exec(`PRAGMA user_version = 18`); err != nil {
				t.Fatal(err)
			}
			if failure == "backup" {
				// A file is not a valid parent for a backup directory.
				err = migrateTaskReceiptIndexesV19(store.db, filepath.Join(directory, "agent.db"))
			} else {
				// Fail on the second CREATE INDEX, after the first would succeed.
				if _, err := store.db.Exec(`CREATE INDEX task_receipts_unresolved_application ON task_receipts(state)`); err != nil {
					t.Fatal(err)
				}
				var opened *Store
				opened, err = Open(directory)
				if opened != nil {
					opened.Close()
					t.Fatal("Open accepted an incomplete migration")
				}
			}
			if err == nil {
				t.Fatal("migration ignored failure")
			}
			var version, count int
			if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 18 {
				t.Fatalf("failed migration changed version: %d %v", version, err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name IN ('task_receipts_pending_completion', 'task_receipts_expiry')`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed migration left partial indexes: %d %v", count, err)
			}
			if failure == "index" {
				backups, err := filepath.Glob(filepath.Join(directory, "schema-18-backup-*", "agent.db"))
				if err != nil || len(backups) != 1 {
					t.Fatalf("failed migration lost backup: %v %v", backups, err)
				}
			}
		})
	}
}
