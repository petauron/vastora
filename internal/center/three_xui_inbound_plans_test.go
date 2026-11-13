package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
)

func TestThreeXUIResetBoundaryUsesMonthlySiteLocalMidnight(t *testing.T) {
	location, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 23, 18, 30, 0, 0, time.UTC)
	if got := threeXUIResetBoundary(now, 1, location); got != "2026-08-31T16:00:00Z" {
		t.Fatalf("next monthly boundary = %q", got)
	}
	advanced, err := advanceThreeXUIResetBoundary("2026-07-31T16:00:00Z", 1, now, location)
	if err != nil || advanced != "2026-08-31T16:00:00Z" {
		t.Fatalf("advanced boundary = %q, err=%v", advanced, err)
	}
	if got := threeXUIResetBoundary(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), 31, time.UTC); got != "2026-02-28T00:00:00Z" {
		t.Fatalf("month-end boundary = %q", got)
	}
}

func TestRetiredThreeXUIQuotaScheduleCannotResetOrQueueWork(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	clock := store.now().UTC()
	store.now = func() time.Time { return clock }
	node := enrollOrchestrationNode(t, store, "controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	deployment := seedLegacyDeployment(t, store, node, "10.0.0.80", "controller-token", threeXUIRoleMaster)
	stamp := clock.Format(time.RFC3339Nano)
	const serviceID = "reality-plan-service"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at) VALUES(?,?,?,'inbound-9','tcp',30009,30009,'10.0.0.80:30009','observed','vless/tcp/reality',0,'10.0.0.80','ready',?,?)`, serviceID, deployment.ApplicationID, testSiteID(t, store), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	boundary := clock.Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(service_id,inbound_tag,total_bytes,reset_day,next_reset_at,revision,status,updated_at) VALUES(?,'vastora-node-9',10737418240,30,?,4,'active',?)`, serviceID, boundary, stamp); err != nil {
		t.Fatal(err)
	}
	read := func() []byte {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := readThreeXUIInboundPlan(ctx, tx, serviceID)
		_ = tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := read()
	for range 3 {
		clock = clock.AddDate(0, 1, 0)
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		task, err := store.claimApplicationCommand(ctx, tx, node.ID)
		_ = tx.Rollback()
		if err != nil || task != nil {
			t.Fatalf("old quota scheduler issued work: %v %v", task, err)
		}
		if string(read()) != string(before) {
			t.Fatal("old quota schedule, revision or reset history changed")
		}
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM application_commands WHERE application_id=?`, deployment.ApplicationID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("old quota command created: %d %v", count, err)
	}
}
