package center

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestGlivelaPilotActivationFinalCountersAndFenceAreAtomic(t *testing.T) {
	store := openMeridianSharedEndpointSnapshotFixture(t)
	seedSecondTakeoverEntry(t, store)
	if _, err := store.db.Exec(`INSERT INTO glivela_pilot_checkpoint(endpoint_id,checkpoint_json,updated_at) VALUES('second-endpoint','{"retained":true}','original')`); err != nil {
		t.Fatal(err)
	}
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
	// A newer Glivela may render different bytes for the same retained plan.
	// Only its pinned private state can bind those bytes, never caller input.
	native.Config = append(native.Config, '\n')
	digest := sha256.Sum256(native.Config)
	native.ConfigSHA256 = hex.EncodeToString(digest[:])
	input.ConfigSHA256 = native.ConfigSHA256
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.glivelaPilotStatePath = filepath.Join(dir, "nodes.json")
	writeDesired := func(p glivela.XrayPlan, certificate string) {
		t.Helper()
		raw, err := json.Marshal([]any{map[string]any{
			"id": projection.task.ApplicationID, "certificateId": certificate,
			"desired": map[string]any{"revision": native.Revision, "config": native.Config,
				"configSha256": native.ConfigSHA256, "entry": p,
				"entryNetwork": map[string]any{"source": projection.task.Source, "peers": projection.task.Peers}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(store.glivelaPilotStatePath, raw, 0600); err != nil {
			t.Fatal(err)
		}
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
	writeDesired(plan, strings.Repeat("d", 64))
	if err := store.ActivateGlivelaPilot(ctx, sharedSnapshotEndpointID, "pilot-admin", input); err == nil {
		t.Fatal("foreign controller identity accepted")
	}
	changed := plan
	changed.Revision++
	writeDesired(changed, input.CertificateID)
	if err := store.ActivateGlivelaPilot(ctx, sharedSnapshotEndpointID, "pilot-admin", input); err == nil {
		t.Fatal("changed retained plan accepted")
	}
	writeDesired(plan, input.CertificateID)
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
	if err := store.db.QueryRow(`SELECT checkpoint_json FROM glivela_pilot_checkpoint WHERE endpoint_id=?`, sharedSnapshotEndpointID).Scan(&raw); err != nil {
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
	var retained, stamp string
	if err := store.db.QueryRow(`SELECT checkpoint_json,updated_at FROM glivela_pilot_checkpoint WHERE endpoint_id='second-endpoint'`).Scan(&retained, &stamp); err != nil || retained != `{"retained":true}` || stamp != "original" {
		t.Fatal("another entry checkpoint changed during activation or accounting", err)
	}
}
