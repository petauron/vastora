package center

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodesecurity"
)

func TestNodeSecurityTaskLifecycleAndRevisionFence(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, s, "security-fixture", NodeCapabilities{Docker: true, NodeSecurity: true}, []networking.Candidate{{Address: "10.0.0.9", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.9", LANAddress: "10.0.0.9", EnabledKinds: []string{networking.KindLAN}})
	if _, err := s.db.Exec(`INSERT INTO admins(id,username,password_hash,created_at) VALUES('security-admin','security-admin','test-hash','')`); err != nil {
		t.Fatal(err)
	}
	p := nodesecurity.Policy{PublicInterfaces: []string{"eth0"}, ManagementSources: []string{"203.0.113.10/32"}, SSHPorts: []int{22}, PublicRules: []nodesecurity.Rule{{Protocol: "tcp", Port: 443}}}
	view, err := s.QueueNodeSecurity(ctx, node.ID, "security-admin", nodesecurity.Task{Action: "preview", Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "pending" {
		t.Fatalf("not queued: %#v", view)
	}
	if _, err = s.QueueNodeSecurity(ctx, node.ID, "security-admin", nodesecurity.Task{Action: "inspect"}); err == nil {
		t.Fatal("concurrent task accepted")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.claimNodeSecurity(ctx, tx, node.ID)
	if err != nil || task == nil {
		_ = tx.Rollback()
		t.Fatalf("claim=%#v err=%v", task, err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if task.Kind != nodesecurity.Kind || task.NodeSecurity.Policy.Digest() != p.Digest() {
		t.Fatalf("untyped task: %#v", task)
	}
	result := nodesecurity.View{State: "preview", Policy: p, Revision: task.NodeSecurity.Revision, Supported: true, Listeners: []string{}, Banned: []string{}, Reasons: []string{}}
	raw, _ := json.Marshal(map[string]any{"nodeSecurity": result})
	if err = s.completeNodeSecurity(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt+1, true, "", raw); err == nil {
		t.Fatal("stale attempt accepted")
	}
	if err = s.completeNodeSecurity(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt, true, "", raw); err != nil {
		t.Fatal(err)
	}
	if err = s.completeNodeSecurity(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt, true, "", raw); err != nil {
		t.Fatal("idempotent receipt rejected", err)
	}
	view, err = s.NodeSecurity(ctx, node.ID)
	if err != nil || view.Result == nil || view.Result.Revision != task.NodeSecurity.Revision {
		t.Fatalf("view=%#v err=%v", view, err)
	}
	if _, err = s.QueueNodeSecurity(ctx, node.ID, "security-admin", nodesecurity.Task{Action: "confirm", Revision: p.Digest()}); err == nil {
		t.Fatal("preview confused with pending application")
	}
	changed := p
	changed.PublicRules = []nodesecurity.Rule{{Protocol: "tcp", Port: 80}}
	if _, err = s.QueueNodeSecurity(ctx, node.ID, "security-admin", nodesecurity.Task{Action: "apply", Policy: changed, Revision: task.NodeSecurity.Revision}); err == nil {
		t.Fatal("changed preview policy accepted")
	}
	if _, err = s.QueueNodeSecurity(ctx, node.ID, "security-admin", nodesecurity.Task{Action: "preview", Policy: p}); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.claimNodeSecurity(ctx, tx, node.ID)
	if err != nil || next == nil {
		_ = tx.Rollback()
		t.Fatal("second preview", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if next.NodeSecurity.Revision == task.NodeSecurity.Revision {
		t.Fatal("same-policy preview reused an old activation revision")
	}
	result.Revision = next.NodeSecurity.Revision
	raw, _ = json.Marshal(map[string]any{"nodeSecurity": result})
	if err = s.completeNodeSecurity(ctx, commitProjectionOnlyForTest, node.ID, next.ID, next.Attempt, true, "", raw); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueNodeSecurity(ctx, node.ID, "security-admin", nodesecurity.Task{Action: "apply", Policy: p, Revision: task.NodeSecurity.Revision}); err == nil {
		t.Fatal("old-generation apply accepted")
	}
	if _, err = s.db.Exec(`UPDATE agents SET last_seen_at=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.QueueNodeSecurity(ctx, node.ID, "security-admin", nodesecurity.Task{Action: "inspect"}); err == nil {
		t.Fatal("offline node accepted")
	}
}

func TestNodeSecurityPendingReceiptExpiresWithoutClaimingRollbackSucceeded(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, s, "security-expiry", NodeCapabilities{Docker: true, NodeSecurity: true}, []networking.Candidate{{Address: "10.0.0.9", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.9", LANAddress: "10.0.0.9", EnabledKinds: []string{networking.KindLAN}})
	if _, err := s.db.Exec(`INSERT INTO admins(id,username,password_hash,created_at) VALUES('security-admin','security-admin','test-hash','')`); err != nil {
		t.Fatal(err)
	}
	p := nodesecurity.Policy{PublicInterfaces: []string{"eth0"}, ManagementSources: []string{"203.0.113.10/32"}, SSHPorts: []int{22}}
	v := nodesecurity.View{State: "pending_confirmation", Policy: p, Revision: p.Digest(), Deadline: s.now().Add(-time.Second).UTC().Format(time.RFC3339Nano)}
	raw, _ := json.Marshal(v)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO node_security_tasks(agent_id,id,input_json,state,result_json,actor_id,created_at,updated_at) VALUES(?,'node-security-expired','{}','succeeded',?,'security-admin',?,?)`, node.ID, string(raw), now, now); err != nil {
		t.Fatal(err)
	}
	got, err := s.NodeSecurity(ctx, node.ID)
	if err != nil || got.Result == nil || got.Result.State != "action_required" {
		t.Fatalf("expired evidence treated as verified/rolled back: %#v %v", got, err)
	}
}

func TestOrdinaryProxyAndLandingNodesCanPreviewWithoutPublicUDP(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, s, "ordinary-proxy-security", NodeCapabilities{Docker: true, NodeSecurity: true}, []networking.Candidate{{Address: "10.0.0.9", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.9", LANAddress: "10.0.0.9", EnabledKinds: []string{networking.KindLAN}})
	if _, err := s.db.Exec(`INSERT INTO admins(id,username,password_hash,created_at) VALUES('security-admin','security-admin','test-hash','')`); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{meridianAppKey, threeXUIAppKey} {
		if _, err := s.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,created_at,updated_at) VALUES(?,?,?,?,?,'running','','')`, "fixture-"+key, key, node.ID, testSiteID(t, s), key); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO landing_server_states(node_id,desired_revision,desired_json,status,updated_at) VALUES(?,1,?,'ready','')`, node.ID, `{"revision":1,"address":"100.64.0.8","sources":[]}`); err != nil {
		t.Fatal(err)
	}
	p := nodesecurity.Policy{PublicInterfaces: []string{"eth0"}, ManagementSources: []string{"203.0.113.10/32"}, SSHPorts: []int{22}, PublicRules: []nodesecurity.Rule{{Protocol: "tcp", Port: 443}}}
	view, err := s.QueueNodeSecurity(ctx, node.ID, "security-admin", nodesecurity.Task{Action: "preview", Policy: p})
	if err != nil || view.State != "pending" {
		t.Fatal("ordinary proxy/landing topology was refused", err)
	}
}

func TestSecurityMigration114MatchesFreshSchema(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	// Reconstruct the released v113 shape, not a current database with only
	// its marker decremented. Otherwise the later ADD COLUMN correctly fails.
	if _, err := s.db.Exec(`ALTER TABLE services DROP COLUMN proxy_protocol; DROP TABLE node_security_tasks; DELETE FROM goose_db_version WHERE version_id>=114; PRAGMA user_version=113`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateSchema(ctx); err != nil {
		t.Fatal(err)
	}
	var sql string
	if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='node_security_tasks'`).Scan(&sql); err != nil {
		t.Fatal(err)
	}
	if sql == "" {
		t.Fatal("migration did not create security state")
	}
	if version, err := sqliteSchemaVersion(ctx, s.db); err != nil || version != centerSchemaVersion {
		t.Fatalf("schema=%d err=%v", version, err)
	}
}
