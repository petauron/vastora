package center

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/nodeprotocol"
)

func TestGlivelaPilotLedgerTransactionAndLegacyFence(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	now := store.now().UTC()
	p := meridianruntime.PilotCounters{EntryID: "snapshot-shared-app", ConfigSHA256: strings.Repeat("a", 64), CertificateID: strings.Repeat("b", 64), Revision: 1, CutoverAt: now.Add(-10 * time.Second)}
	rows, err := store.db.Query(`SELECT id FROM meridian_credentials WHERE endpoint_id=? ORDER BY id`, sharedSnapshotEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		p.Totals = append(p.Totals, glivela.CounterSnapshot{CredentialID: id, UpBytes: 100, DownBytes: 200})
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(p.Totals) == 0 {
		t.Fatal("missing credentials")
	}
	for _, c := range p.Totals {
		if _, err := store.db.Exec(`INSERT INTO meridian_usage_watermarks(credential_id,baseline_bytes,observed_bytes,raw_up_bytes,raw_down_bytes,observed_at)
			VALUES(?,0,300,100,200,?) ON CONFLICT(credential_id) DO UPDATE SET observed_bytes=300,raw_up_bytes=100,raw_down_bytes=200`,
			c.CredentialID, now.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(p)
	if _, err := store.db.Exec(`INSERT INTO glivela_pilot_checkpoint VALUES(1,?,?,?)`, sharedSnapshotEndpointID, raw, now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	seedSecondTakeoverEntry(t, store)
	if _, err := store.db.Exec(`INSERT INTO glivela_pilot_checkpoint(endpoint_id,checkpoint_json,updated_at) VALUES('second-endpoint','{}','original');
		UPDATE meridian_endpoints SET runtime_healthy=1 WHERE id='second-endpoint'`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.queueMeridianRuntime(ctx, tx, sharedSnapshotEndpointID, false); err != errGlivelaPilotOwnsRuntime {
		t.Fatal("legacy queue not fenced", err)
	}
	if _, err := store.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, ""); err != errGlivelaPilotOwnsRuntime {
		t.Fatal("legacy claim not fenced", err)
	}
	if owned, err := glivelaPilotOwnsEndpoint(ctx, tx, "another-endpoint"); err != nil || owned {
		t.Fatal("pilot widened to another endpoint", err)
	}
	if err := requireLegacyMeridianApplicationOwner(ctx, tx, p.EntryID); err != errGlivelaPilotOwnsRuntime {
		t.Fatal("legacy application management not fenced", err)
	}
	if err := requireLegacyMeridianApplicationOwner(ctx, tx, "another-application"); err != nil {
		t.Fatal("another application's lifecycle blocked", err)
	}
	var cleanups []publicationCleanup
	if err := store.retireApplicationManagement(ctx, tx, p.EntryID, now, &cleanups); err != errGlivelaPilotOwnsRuntime || len(cleanups) != 0 {
		t.Fatal("legacy retirement changed owned publication", err)
	}
	tx.Rollback()
	var serviceID string
	if err := store.db.QueryRow(`SELECT service_id FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.configureMeridianNodeProtocols(ctx, serviceID, nodeprotocol.Selection{VLESS: true, HY2: true}); err != errGlivelaPilotOwnsRuntime {
		t.Fatal("legacy protocol change was not fenced before certificate acquisition", err)
	}
	r := meridianruntime.PilotReceipt{EntryID: p.EntryID, ConfigSHA256: p.ConfigSHA256, StartedAt: now.Add(-5 * time.Second), SampledAt: now.Add(-time.Second), Ready: true}
	r.PublicationSuspended = true // New traffic is still accounted exactly once while publication is paused.
	for _, c := range p.Totals {
		r.Counters = append(r.Counters, glivela.CounterSnapshot{CredentialID: c.CredentialID, UpBytes: 3, DownBytes: 7})
	}
	if err := store.recordGlivelaPilotReceipt(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := store.recordGlivelaPilotReceipt(ctx, r); err != nil {
		t.Fatal(err)
	}
	readUsage := func() int64 {
		t.Helper()
		var used int64
		if err := store.db.QueryRow(`SELECT observed_bytes FROM meridian_usage_watermarks WHERE credential_id=?`, p.Totals[0].CredentialID).Scan(&used); err != nil {
			t.Fatal(err)
		}
		return used
	}
	if readUsage() != 310 {
		t.Fatal("duplicate report changed accounting")
	}
	// The independent consumer needs no Agent heartbeat and must retain the
	// existing idempotent ledger transaction, not introduce a second writer.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.glivelaPilotStatePath = filepath.Join(dir, "nodes.json")
	counters := map[string]any{}
	for _, c := range r.Counters {
		counters[c.CredentialID] = map[string]string{"upload": "3", "download": "7"}
	}
	source, err := json.Marshal([]any{map[string]any{
		"id": p.EntryID, "certificateId": p.CertificateID, "reportedAt": now,
		"desired": map[string]any{"revision": p.Revision, "configSha256": p.ConfigSHA256},
		"receipt": map[string]any{"revision": p.Revision, "configSha256": p.ConfigSHA256, "runtimeReady": true,
			"traffic": map[string]any{"startedAt": r.StartedAt, "sampledAt": r.SampledAt, "counters": counters}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.glivelaPilotStatePath, source, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.refreshGlivelaPilotReceipt(ctx); err != nil {
			t.Fatal("independent receipt refresh failed", err)
		}
	}
	if readUsage() != 310 {
		t.Fatal("independent refresh double-counted traffic")
	}
	var secondHealthy bool
	if err := store.db.QueryRow(`SELECT runtime_healthy FROM meridian_endpoints WHERE id='second-endpoint'`).Scan(&secondHealthy); err != nil || secondHealthy {
		t.Fatal("missing second receipt was not independently blocked", err)
	}
	// A subscription must read the latest pinned receipt without waiting for
	// either polling loop, and retain the same sole, idempotent ledger.
	if _, err := store.db.Exec(`UPDATE meridian_endpoints SET runtime_healthy=0 WHERE id=?`, sharedSnapshotEndpointID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MeridianSubscription(ctx, sharedSnapshotTokenB); err != nil {
		t.Fatal("subscription did not refresh pilot receipt", err)
	}
	var ready bool
	if err := store.db.QueryRow(`SELECT runtime_healthy FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&ready); err != nil || !ready {
		t.Fatal("subscription retained stale pilot state", err)
	}
	if readUsage() != 310 {
		t.Fatal("subscription refresh double-counted traffic")
	}
	var saved string
	// Publication suspension must hide the entry without discarding or replaying
	// its authenticated traffic receipt. Resuming uses a fresh controller state.
	var suspended []map[string]any
	if err := json.Unmarshal(source, &suspended); err != nil {
		t.Fatal(err)
	}
	suspended[0]["publicationSuspended"] = true
	paused, err := json.Marshal(suspended)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.glivelaPilotStatePath, paused, 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := store.refreshGlivelaPilotReceipt(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.QueryRow(`SELECT runtime_healthy FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&ready); err != nil || ready {
		t.Fatal("suspended entry still published", err)
	}
	if readUsage() != 310 {
		t.Fatal("suspension changed accounting")
	}
	if err := os.WriteFile(store.glivelaPilotStatePath, source, 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.refreshGlivelaPilotReceipt(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT checkpoint_json FROM glivela_pilot_checkpoint WHERE endpoint_id=?`, sharedSnapshotEndpointID).Scan(&saved); err != nil {
		t.Fatal(err)
	}
	// Failure after updating the ledger must roll back both halves.
	if _, err := store.db.Exec(`CREATE TRIGGER pilot_checkpoint_fail BEFORE UPDATE ON glivela_pilot_checkpoint BEGIN SELECT RAISE(ABORT,'test atomic rollback'); END`); err != nil {
		t.Fatal(err)
	}
	r.SampledAt = now
	r.Counters[0].UpBytes++
	if err := store.recordGlivelaPilotReceipt(ctx, r); err == nil {
		t.Fatal("checkpoint write failure ignored")
	}
	var retained string
	if err := store.db.QueryRow(`SELECT checkpoint_json FROM glivela_pilot_checkpoint WHERE endpoint_id=?`, sharedSnapshotEndpointID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if readUsage() != 310 || retained != saved {
		t.Fatal("receipt and ledger did not roll back together")
	}
	if _, err := store.db.Exec(`DROP TRIGGER pilot_checkpoint_fail`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE meridian_usage_watermarks SET raw_up_bytes=raw_up_bytes+1 WHERE credential_id=?`, p.Totals[0].CredentialID); err != nil {
		t.Fatal(err)
	}
	if err := store.recordGlivelaPilotReceipt(ctx, r); err == nil {
		t.Fatal("foreign ledger write accepted")
	}
	if readUsage() != 310 {
		t.Fatal("conflicted receipt advanced ledger")
	}
}
