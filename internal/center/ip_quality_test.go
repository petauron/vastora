package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/ipquality"
	"github.com/petauron/vastora/internal/networking"
)

func TestIPQualityLatestResultAndStaleAttempt(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, s, "quality-test", NodeCapabilities{Docker: true, IPQuality: true}, []networking.Candidate{{Address: "10.0.0.18", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.18", LANAddress: "10.0.0.18", EnabledKinds: []string{networking.KindLAN}})
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,status,updated_at) VALUES(?,1,1,'{}','ready',?)`, node.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET public_egress_address='203.0.113.8',public_egress_bind_address='10.0.0.18',public_egress_mode='nat',public_egress_observed_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartIPQuality(ctx, node.ID, "203.0.113.8"); err != nil {
		t.Fatal(err)
	}
	if err := s.StartIPQuality(ctx, node.ID, "203.0.113.8"); err == nil {
		t.Fatal("duplicate diagnostic queued")
	}
	claim := func() *AgentTask {
		t.Helper()
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		task, err := s.claimIPQuality(ctx, tx, node.ID)
		if err != nil || task == nil {
			t.Fatalf("claim: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return task
	}
	task := claim()
	report := ipquality.Report{Address: "203.0.113.8", Version: "test", Scores: []ipquality.Score{{Source: "IPQS", Value: "75"}}, Services: []ipquality.Service{{Name: "Netflix", Status: "Yes", RegionCode: "CA"}}}
	report.RecordObservations(s.now())
	report.IPPure = &ipquality.IPPureResult{Provider: ipquality.IPPureProvider, Status: "unavailable", CheckedAt: now}
	raw, _ := json.Marshal(map[string]any{"ipQuality": ipquality.Result{Report: &report}})
	if err := s.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt+1, true, "", raw, false); err == nil {
		t.Fatal("stale attempt accepted")
	}
	if err := s.completeTaskWithDisposition(ctx, commitProjectionOnlyForTest, node.ID, node.Credential, task.ID, task.Attempt, true, "", raw, false); err != nil {
		t.Fatal(err)
	}
	if err := s.StartIPQuality(ctx, node.ID, "203.0.113.8"); err != nil {
		t.Fatal(err)
	}
	second := claim()
	if err := s.completeIPQuality(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt, true, raw); err == nil {
		t.Fatal("old check overwrote current check")
	}
	if err := s.completeIPQuality(ctx, commitProjectionOnlyForTest, node.ID, second.ID, second.Attempt, true, json.RawMessage(`{"ipQuality":{"error":"timeout"}}`)); err != nil {
		t.Fatal(err)
	}
	values, err := s.ListIPQuality(ctx)
	if err != nil || len(values) != 1 || values[0].State != "succeeded" || values[0].Error != "timeout" || values[0].Report == nil || values[0].Report.Scores[0].Value != "75" {
		t.Fatalf("failed diagnostic lost previous report: %#v %v", values, err)
	}
	if values[0].Report.IPPure == nil || len(values[0].Report.Observations) == 0 || values[0].Assessment.Status != "partial" || values[0].Assessment.Score != nil {
		t.Fatalf("evidence did not round trip or partial report was ranked: %+v", values[0])
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET public_egress_address='203.0.113.9' WHERE id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	values, err = s.ListIPQuality(ctx)
	if err != nil || !values[0].Stale {
		t.Fatal("IP change did not invalidate report")
	}
	if values[0].Assessment.Status != "ip_changed" || values[0].Assessment.Score != nil || values[0].Assessment.Advice != "recheck" {
		t.Fatal("changed IP retained a usable assessment")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE landing_server_states SET status='stopped' WHERE node_id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	values, err = s.ListIPQuality(ctx)
	if err != nil || len(values) != 0 {
		t.Fatalf("stopped landing server remained in IP quality results: %#v %v", values, err)
	}
}

func TestIPQualityRequiresVLESSOrLandingTarget(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, s, "compute-only", NodeCapabilities{Docker: true, IPQuality: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
	if _, err := s.db.ExecContext(ctx, `UPDATE agents SET public_egress_address='203.0.113.9',public_egress_bind_address='10.0.0.19',public_egress_mode='nat',public_egress_observed_at=? WHERE id=?`, s.now().UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.StartIPQuality(ctx, node.ID, "203.0.113.9"); err == nil || err.Error() != "ip_quality_target_required" {
		t.Fatalf("compute-only node accepted IP quality check: %v", err)
	}
}

func TestIPQualityMigration78AddsEmptyLatestResults(t *testing.T) {
	directory := t.TempDir()
	createLegacyVersion3Database(t, directory)
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	legacy := &Store{db: db}
	if err := legacy.initializeMigrationHistory(ctx, schemaBaselineVersion); err != nil {
		t.Fatal(err)
	}
	provider, err := newMigrationProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 76); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 78); err != nil {
		t.Fatal(err)
	}
	var version, count int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 78 {
		t.Fatalf("version: %d %v", version, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM ip_quality_checks`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration generated diagnostic tasks: %d %v", count, err)
	}
}

func TestIPQualitySeparatesExactEgressReportsAndRetiresMissingAddress(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, s, "dual-stack-quality", NodeCapabilities{Docker: true, IPQuality: true}, []networking.Candidate{{Address: "10.0.0.18", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.18", LANAddress: "10.0.0.18", EnabledKinds: []string{networking.KindLAN}})
	const v4 = "203.0.113.8"
	const v6 = "2001:4860:4860::8888"
	const secondV6 = "2001:4860:4860::8844"
	now := s.now().UTC().Format(time.RFC3339Nano)
	inventory := `[{"address":"2001:4860:4860::8888","interface":"eth0"},{"address":"2001:4860:4860::8844","interface":"eth0"},{"address":"10.0.0.18","interface":"eth0"}]`
	if _, err := s.db.Exec(`UPDATE agents SET public_egress_address=?,public_egress_bind_address='10.0.0.18',public_egress_mode='nat',public_egress_observed_at=?,landing_egress_addresses_json=? WHERE id=?`, v4, now, inventory, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO landing_server_states(node_id,desired_revision,applied_revision,desired_json,status,updated_at) VALUES(?,1,1,?,'ready',?)`, node.ID, `{"plan":{"egressIp":"2001:4860:4860::8888"}}`, now); err != nil {
		t.Fatal(err)
	}
	targets, err := listIPQualityTargets(ctx, s.db, node.ID)
	if err != nil || len(targets) != 3 {
		t.Fatalf("expected native IPv4 and two independent IPv6 exits: %+v %v", targets, err)
	}
	selected := 0
	for _, target := range targets {
		if target.Selected {
			selected++
			if target.Address != v6 || target.Family != "ipv6" {
				t.Fatalf("wrong selected exit: %+v", target)
			}
		}
		if target.Address == v4 && (target.bindAddress != "10.0.0.18" || !target.native) {
			t.Fatalf("native NAT mapping lost: %+v", target)
		}
	}
	if selected != 1 {
		t.Fatalf("selected %d exits", selected)
	}
	for _, address := range []string{"", "1.1.1.1", "10.0.0.18"} {
		if err := s.StartIPQuality(ctx, node.ID, address); err == nil || err.Error() != "ip_quality_address_unavailable" {
			t.Fatalf("accepted unreported or non-public target %q: %v", address, err)
		}
	}
	complete := func(address string) {
		t.Helper()
		if err := s.StartIPQuality(ctx, node.ID, address); err != nil {
			t.Fatal(err)
		}
		if err := s.StartIPQuality(ctx, node.ID, secondV6); err == nil {
			t.Fatal("accepted concurrent egress probes on the same agent")
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		task, err := s.claimIPQuality(ctx, tx, node.ID)
		if err != nil || task == nil {
			tx.Rollback()
			t.Fatalf("claim: %+v %v", task, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if task.IPQuality.Address != address || address == v6 && task.IPQuality.BindAddress != v6 {
			t.Fatalf("wrong egress dispatched: %+v", task.IPQuality)
		}
		wrong := ipquality.Report{Address: secondV6, Version: "test", Scores: []ipquality.Score{{Source: "IPQS", Value: "10"}}, Services: []ipquality.Service{{Name: "Netflix", Status: "Yes", RegionCode: "US"}}}
		wrong.RecordObservations(s.now())
		raw, _ := json.Marshal(map[string]any{"ipQuality": ipquality.Result{Report: &wrong}})
		if err := s.completeIPQuality(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt, true, raw); err == nil {
			t.Fatal("accepted another exit's report")
		}
		report := wrong
		report.Address = address
		report.RecordObservations(s.now())
		raw, _ = json.Marshal(map[string]any{"ipQuality": ipquality.Result{Report: &report}})
		if err := s.completeIPQuality(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt, true, raw); err != nil {
			t.Fatal(err)
		}
	}
	complete(v4)
	complete(v6)
	checks, err := s.ListIPQuality(ctx)
	if err != nil || len(checks) != 2 {
		t.Fatalf("reports were overwritten: %+v %v", checks, err)
	}
	for _, check := range checks {
		if check.Report == nil || check.Report.Address != check.Address || check.Stale || check.Selected != (check.Address == v6) {
			t.Fatalf("report identity or selection crossed exits: %+v", check)
		}
	}
	if _, err := s.db.Exec(`UPDATE agents SET landing_egress_addresses_json=? WHERE id=?`, `[{"address":"2001:4860:4860::8844","interface":"eth0"}]`, node.ID); err != nil {
		t.Fatal(err)
	}
	checks, err = s.ListIPQuality(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range checks {
		if check.Address == v4 && check.Stale {
			t.Fatal("retiring IPv6 invalidated native IPv4 report")
		}
		if check.Address == v6 && (!check.Stale || check.Selected || check.Assessment.Status != "ip_changed") {
			t.Fatalf("removed IPv6 remained usable: %+v", check)
		}
	}
	targets, err = listIPQualityTargets(ctx, s.db, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.Selected {
			t.Fatal("missing selected IPv6 silently selected another exit")
		}
	}
	if err := s.StartIPQuality(ctx, node.ID, v6); err == nil {
		t.Fatal("removed IPv6 could still be queued")
	}
	if err := s.StartIPQuality(ctx, node.ID, secondV6); err != nil {
		t.Fatal(err)
	}
	checks, err = s.ListIPQuality(ctx)
	if err != nil || len(checks) != 3 {
		t.Fatalf("second IPv6 missing: %+v %v", checks, err)
	}
	for _, check := range checks {
		if check.Address == secondV6 && (check.Report != nil || check.Assessment.Status != "partial" || check.Assessment.Score != nil) {
			t.Fatalf("new IPv6 borrowed historical report: %+v", check)
		}
	}
}
