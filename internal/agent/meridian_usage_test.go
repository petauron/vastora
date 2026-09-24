package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/petauron/vastora/internal/meridianruntime"
)

func meridianUsageSample(t *testing.T, values map[string]int64) []byte {
	t.Helper()
	stats := make([]map[string]any, 0, len(values))
	for name, value := range values {
		stats = append(stats, map[string]any{"name": "user>>>" + name + ">>>traffic>>>uplink", "value": value})
	}
	encoded, err := json.Marshal(map[string]any{"stat": stats})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func meridianUsageTotals(t *testing.T, report *meridianruntime.UsageLedgerReport) map[string]int64 {
	t.Helper()
	var parsed struct {
		Stats []struct {
			Name  string `json:"name"`
			Value int64  `json:"value"`
		} `json:"stat"`
	}
	if report == nil || report.Validate() != nil || json.Unmarshal(report.Stats, &parsed) != nil {
		t.Fatal("invalid cumulative Meridian usage report")
	}
	totals := map[string]int64{}
	for _, stat := range parsed.Stats {
		totals[stat.Name] = stat.Value
	}
	return totals
}

func TestMeridianUsageLedgerSurvivesRestartAndScopesProcessGapToOldUsers(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	var ledgerID string
	var sequence uint64
	read := func(generation string, users []string, values map[string]int64) *meridianruntime.UsageLedgerReport {
		t.Helper()
		report, err := store.accumulateMeridianUsage(ctx, "entry-app", generation, users, meridianUsageSample(t, values))
		if err != nil {
			t.Fatal(err)
		}
		if ledgerID == "" {
			ledgerID = report.ID
		}
		if report.ID != ledgerID || report.Sequence != sequence+1 {
			t.Fatalf("ledger identity or sequence changed: id=%q sequence=%d previous=%d", report.ID, report.Sequence, sequence)
		}
		sequence = report.Sequence
		return report
	}
	upAlice := "user>>>alice>>>traffic>>>uplink"
	upBob := "user>>>bob>>>traffic>>>uplink"
	if got := meridianUsageTotals(t, read("container/start-1", []string{"alice"}, map[string]int64{"alice": 100}))[upAlice]; got != 100 {
		t.Fatalf("initial total=%d", got)
	}
	if got := meridianUsageTotals(t, read("container/start-1", []string{"alice"}, map[string]int64{"alice": 100}))[upAlice]; got != 100 {
		t.Fatalf("replayed raw counter double counted: %d", got)
	}
	if got := meridianUsageTotals(t, read("container/start-1", []string{"alice"}, map[string]int64{"alice": 130}))[upAlice]; got != 130 {
		t.Fatalf("in-process delta=%d", got)
	}
	// The first counter in the new process exceeds the old raw value. It is
	// still a new generation and must be added, with Alice's gap marked.
	gap := read("container/start-2", []string{"bob"}, map[string]int64{"alice": 200, "bob": 10})
	if got := meridianUsageTotals(t, gap); got[upAlice] != 330 || got[upBob] != 10 || !reflect.DeepEqual(gap.AffectedUsers, []string{"alice"}) {
		t.Fatalf("process gap totals=%v affected=%v", got, gap.AffectedUsers)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	continued := read("container/start-2", []string{"bob"}, map[string]int64{"bob": 20})
	if got := meridianUsageTotals(t, continued); got[upAlice] != 330 || got[upBob] != 20 || !reflect.DeepEqual(continued.AffectedUsers, []string{"alice"}) {
		t.Fatalf("persisted gap contaminated new user or lost old total: %v affected=%v", got, continued.AffectedUsers)
	}
	regressed := read("container/start-2", []string{"bob"}, map[string]int64{"bob": 5})
	if got := meridianUsageTotals(t, regressed); got[upBob] != 20 || !reflect.DeepEqual(regressed.AffectedUsers, []string{"alice", "bob"}) {
		t.Fatalf("lower same-process counter rewound high water: %v affected=%v", got, regressed.AffectedUsers)
	}
}

func TestMeridianUsageJournalCorruptionFailsClosed(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.accumulateMeridianUsage(ctx, "entry-app", "container/start-1", []string{"alice"}, meridianUsageSample(t, map[string]int64{"alice": 1})); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_usage_state SET sealed_state=X'00' WHERE application_id='entry-app'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.accumulateMeridianUsage(ctx, "entry-app", "container/start-1", []string{"alice"}, meridianUsageSample(t, map[string]int64{"alice": 2})); err == nil {
		t.Fatal("corrupt encrypted usage journal was reinitialized")
	}
}

func TestMeridianUsageUsersIncludeZeroTrafficAndRejectUnavailableGeneration(t *testing.T) {
	users, err := meridianActiveUsageUsers([]byte(`{"inbounds":[{"settings":{"clients":[{"email":"alice"}] }},{"settings":{"users":[{"email":"alice"},{"email":"bob"}]}}]}`))
	if err != nil || !reflect.DeepEqual(users, []string{"alice", "bob"}) {
		t.Fatalf("active users=%v err=%v", users, err)
	}
	if _, err := meridianRuntimeGeneration("container", ""); err == nil {
		t.Fatal("missing process start time was accepted")
	}
	if _, err := meridianRuntimeGeneration("container", "2026-09-24T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

func TestVersion22MigrationBacksUpAgentBeforeAddingUsageJournal(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE meridian_usage_state; PRAGMA user_version=21`); err != nil {
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
	var version, tables int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 22 {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='meridian_usage_state'`).Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("usage journal tables=%d err=%v", tables, err)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "schema-21-backup-*", "agent.db"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup missing: %v %v", backups, err)
	}
	backup, err := sql.Open("sqlite", "file:"+backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := backup.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 21 {
		t.Fatalf("backup schema version=%d err=%v", version, err)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Size() == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("empty migration backup: %v", err)
	}
}
