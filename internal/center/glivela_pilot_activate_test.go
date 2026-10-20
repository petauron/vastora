package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestGlivelaPilotActivationFinalCountersAndFenceAreAtomic(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`INSERT INTO admins(id,username,password_hash,created_at) VALUES('pilot-admin','pilot-admin','test','');
		UPDATE meridian_accounts SET total_bytes=0;
		UPDATE meridian_endpoints SET target_ip='1.1.1.1';
		UPDATE applications SET image='example.test/xray:current' WHERE id='snapshot-shared-app'`); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.buildMeridianRuntimeTask(ctx, tx, sharedSnapshotEndpointID, "")
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	plan, _, _, err := store.ExportGlivelaPilotPlan(ctx, sharedSnapshotEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	native, err := glivela.BuildDesiredArtifact(plan)
	if err != nil {
		t.Fatal(err)
	}
	input := GlivelaPilotActivation{
		LegacyConfigSHA256: projection.task.Desired.ConfigSHA256, ConfigSHA256: native.ConfigSHA256,
		CertificateID: strings.Repeat("b", 64), BackupSHA256: strings.Repeat("c", 64), Revision: 1,
		SampledAt: store.now().UTC(), DrainedAndStopped: true,
	}
	for _, c := range projection.materials {
		input.Counters = append(input.Counters, glivela.CounterSnapshot{CredentialID: c.Credential.ID, UpBytes: 3, DownBytes: 7})
	}
	used := func() int64 {
		t.Helper()
		var value int64
		if err := store.db.QueryRow(`SELECT sum(observed_bytes) FROM meridian_usage_watermarks`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := used()
	if _, err := store.db.Exec(`CREATE TRIGGER reject_pilot_activation BEFORE INSERT ON glivela_pilot_checkpoint BEGIN SELECT RAISE(ABORT,'injected activation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGlivelaPilot(ctx, sharedSnapshotEndpointID, "pilot-admin", input); err == nil {
		t.Fatal("activation checkpoint failure ignored")
	}
	if used() != before {
		t.Fatal("final counters committed without fence")
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_pilot_activation`); err != nil {
		t.Fatal(err)
	}
	if err := store.ActivateGlivelaPilot(ctx, sharedSnapshotEndpointID, "pilot-admin", input); err != nil {
		t.Fatal(err)
	}
	if used() != before+int64(10*len(input.Counters)) {
		t.Fatal("final counter delta differs")
	}
	if err := store.ActivateGlivelaPilot(ctx, sharedSnapshotEndpointID, "pilot-admin", input); err == nil {
		t.Fatal("duplicate activation accepted")
	}
	var raw []byte
	if err := store.db.QueryRow(`SELECT checkpoint_json FROM glivela_pilot_checkpoint`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var checkpoint meridianruntime.PilotCounters
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	now := checkpoint.CutoverAt.Add(2 * time.Second)
	store.now = func() time.Time { return now }
	receipt := meridianruntime.PilotReceipt{EntryID: checkpoint.EntryID, ConfigSHA256: checkpoint.ConfigSHA256,
		Ready: true, StartedAt: checkpoint.CutoverAt.Add(time.Second), SampledAt: now}
	for _, c := range input.Counters {
		receipt.Counters = append(receipt.Counters, glivela.CounterSnapshot{CredentialID: c.CredentialID, UpBytes: 4, DownBytes: 6})
	}
	if err := store.recordGlivelaPilotReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.recordGlivelaPilotReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if used() != before+int64(20*len(input.Counters)) {
		t.Fatal("native receipt lost or duplicated final legacy usage")
	}
}
