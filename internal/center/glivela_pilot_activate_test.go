package center

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestGlivelaPilotActivationFinalCountersAndFenceAreAtomic(t *testing.T) {
	checkGlivelaPilotActivation(t, false)
}

func TestGlivelaPilotQuotaActivationAndResetPreserveLifetimeLedger(t *testing.T) {
	checkGlivelaPilotActivation(t, true)
}

func checkGlivelaPilotActivation(t *testing.T, finite bool) {
	t.Helper()
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
	var seed meridianruntime.PilotQuota
	if finite {
		var initial int64
		if err := store.db.QueryRow(`SELECT used_bytes FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&initial); err != nil {
			t.Fatal(err)
		}
		seed = meridianruntime.PilotQuota{TotalBytes: initial + int64(10*len(projection.materials)) + 5, UsedBytes: initial + int64(10*len(projection.materials)), ResetDay: 1, CutoverAt: input.SampledAt}
		if err := store.db.QueryRow(`SELECT site.timezone FROM meridian_endpoints e JOIN services service ON service.id=e.service_id JOIN sites site ON site.id=service.site_id WHERE e.id=?`, sharedSnapshotEndpointID).Scan(&seed.Timezone); err != nil {
			t.Fatal(err)
		}
		location, err := time.LoadLocation(seed.Timezone)
		if err != nil {
			t.Fatal(err)
		}
		local := input.SampledAt.In(location)
		seed.NextResetAt = time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, location)
		if _, err := store.db.Exec(`UPDATE meridian_endpoints SET total_bytes=?,reset_day=1,next_reset_at=? WHERE id=?`, seed.TotalBytes, seed.NextResetAt.Format(time.RFC3339Nano), sharedSnapshotEndpointID); err != nil {
			t.Fatal(err)
		}
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store.glivelaPilotStatePath = filepath.Join(dir, "nodes.json")
	writeDesired := func(p glivela.XrayPlan, certificate string) {
		t.Helper()
		node := map[string]any{
			"id": projection.task.ApplicationID, "certificateId": certificate,
			"desired": map[string]any{"revision": native.Revision, "config": native.Config,
				"configSha256": native.ConfigSHA256, "entry": p,
				"entryNetwork": map[string]any{"source": projection.task.Source, "peers": projection.task.Peers}},
		}
		if finite {
			zero := map[string]any{}
			for _, c := range projection.materials {
				zero[c.Credential.ID] = map[string]string{"upload": "0", "download": "0"}
			}
			node["entryQuota"] = map[string]any{"totalBytes": fmt.Sprint(seed.TotalBytes), "usedBytes": fmt.Sprint(seed.UsedBytes), "resetDay": seed.ResetDay, "timezone": seed.Timezone, "cutoverAt": seed.CutoverAt, "nextResetAt": seed.NextResetAt, "base": node["desired"], "forwarded": map[string]any{"startedAt": seed.CutoverAt, "sampledAt": seed.CutoverAt, "counters": zero}}
		}
		raw, err := json.Marshal([]any{node})
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
	if finite {
		if checkpoint.Quota == nil {
			t.Fatal("finite quota was not retained")
		}
		q := *checkpoint.Quota
		q.UsedBytes += int64(10 * len(input.Counters))
		receipt.Quota = &q
		receipt.StartedAt = checkpoint.CutoverAt
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
	if finite {
		var period int64
		var enabled bool
		if err := store.db.QueryRow(`SELECT used_bytes,quota_applied_enabled FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&period, &enabled); err != nil || period != receipt.Quota.UsedBytes || enabled {
			t.Fatal("over-limit policy not reflected", err)
		}
		now = seed.NextResetAt.Add(time.Second)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.resetDueMeridianEndpoints(ctx, tx); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		var retainedPeriod int64
		if err := store.db.QueryRow(`SELECT used_bytes FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&retainedPeriod); err != nil || retainedPeriod != period {
			t.Fatal("old scheduler reset an owned entry", err)
		}
		q := *receipt.Quota
		q.UsedBytes = 0
		q.LastResetAt = seed.NextResetAt
		q.NextResetAt = seed.NextResetAt.AddDate(0, 1, 0)
		receipt.Quota = &q
		receipt.SampledAt = now
		if err := store.recordGlivelaPilotReceipt(ctx, receipt); err != nil {
			t.Fatal(err)
		}
		if err := store.db.QueryRow(`SELECT used_bytes,quota_applied_enabled FROM meridian_endpoints WHERE id=?`, sharedSnapshotEndpointID).Scan(&period, &enabled); err != nil || period != 0 || !enabled {
			t.Fatal("cycle recovery not reflected", err)
		}
		if used() != before+int64(20*len(input.Counters)) {
			t.Fatal("period reset changed lifetime accounting")
		}
	}
	var retained, stamp string
	if err := store.db.QueryRow(`SELECT checkpoint_json,updated_at FROM glivela_pilot_checkpoint WHERE endpoint_id='second-endpoint'`).Scan(&retained, &stamp); err != nil || retained != `{"retained":true}` || stamp != "original" {
		t.Fatal("another entry checkpoint changed during activation or accounting", err)
	}
}
