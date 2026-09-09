package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestMultipleLandingMigrationPreservesSelectionAndBacksUp(t *testing.T) {
	for _, nodeID := range []string{"", "agent-v3"} {
		t.Run("selected_"+nodeID, func(t *testing.T) {
			directory := t.TempDir()
			legacy := legacyMigrationStore(t, directory, 66)
			if _, err := legacy.db.Exec(`INSERT INTO settings(key,value) VALUES(?,json_object('nodeId',?,'revision',9))`, landingSelectionKey, nodeID); err != nil {
				t.Fatal(err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			tx, err := store.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			selection, err := readLandingSelection(context.Background(), tx)
			tx.Rollback()
			expected := []string{}
			if nodeID != "" {
				expected = append(expected, nodeID)
			}
			if err != nil || selection.Revision != 9 || !slices.Equal(selection.NodeIDs, expected) {
				t.Fatalf("selection not preserved: %+v %v", selection, err)
			}
			backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v66-before-v%d-*.db", centerSchemaVersion)))
			if err != nil || len(backups) != 1 {
				t.Fatalf("missing pre-migration backup: %v %v", backups, err)
			}
			backup, err := sql.Open("sqlite", backups[0])
			if err != nil {
				t.Fatal(err)
			}
			defer backup.Close()
			var old string
			if err := backup.QueryRow(`SELECT json_extract(value,'$.nodeId') FROM settings WHERE key=?`, landingSelectionKey).Scan(&old); err != nil || old != nodeID {
				t.Fatalf("original selection missing from backup: %v", err)
			}
		})
	}
}

func TestMultipleLandingMigrationRejectsInvalidSelection(t *testing.T) {
	directory := t.TempDir()
	legacy := legacyMigrationStore(t, directory, 66)
	if _, err := legacy.db.Exec(`INSERT INTO settings(key,value) VALUES(?,'{"revision":9}')`, landingSelectionKey); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(directory); err == nil {
		store.Close()
		t.Fatal("malformed released setting was silently replaced")
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 66 {
		t.Fatalf("failed migration advanced schema: %d %v", version, err)
	}
}

func TestMultipleLandingSwitchRetainsOldGrantUntilConfirmed(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{}
	for i, address := range []string{"10.0.0.80", "10.0.0.81", "10.0.0.82", "10.0.0.83"} {
		node := enrollOrchestrationNode(t, store, address, NodeCapabilities{Docker: true}, []networking.Candidate{{Address: address, Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: address, LANAddress: address, EnabledKinds: []string{networking.KindLAN}})
		ids = append(ids, node.ID)
		exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, node.ID)
		exec(`UPDATE agent_network_profiles SET headscale_address=? WHERE agent_id=?`, []string{"100.64.0.8", "100.64.0.9", "100.64.0.10", "100.64.0.11"}[i], node.ID)
	}
	a, b, source, other := ids[0], ids[1], ids[2], ids[3]
	now := store.now().UTC().Format(time.RFC3339Nano)
	site := testSiteID(t, store)
	for _, app := range []struct{ id, node string }{{"multi-proxy", source}, {"other-proxy", other}} {
		exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES(?,?,?,?,'vastora-official/3x-ui','running','docker','worker',?,?)`, app.id, app.id, app.node, site, now, now)
		exec(`INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,status,created_at,updated_at) VALUES(?,?,?,'VLESS','tcp',443,443,'unchanged.example:443','observed','vless/tcp/reality','ready',?,?)`, app.id, app.id, site, now, now)
		exec(`INSERT INTO three_x_ui_inbound_plans(service_id,inbound_tag,updated_at) VALUES(?,'business',?)`, app.id, now)
	}
	claim := func(node string, server bool) *AgentTask {
		t.Helper()
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		var task *AgentTask
		if server {
			task, err = store.claimLandingServerTask(ctx, tx, node)
		} else {
			task, err = store.claimLandingProxyTask(ctx, tx, node)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		return task
	}
	ready := func(node string) {
		t.Helper()
		task := claim(node, true)
		if task == nil {
			t.Fatal("missing server task")
		}
		peer := &landing.PeerIdentity{ID: node, PublicKey: "key-" + node, Address: task.LandingServerState.Plan.Address}
		if err := store.completeLandingServer(ctx, node, task.Revision, task.Attempt, true, peer); err != nil {
			t.Fatal(err)
		}
	}
	grants := func(node string) int {
		t.Helper()
		var raw []byte
		if err := store.db.QueryRow(`SELECT desired_json FROM landing_server_states WHERE node_id=?`, node).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var state landing.ServerState
		if err := json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		if state.Plan == nil {
			return 0
		}
		return len(state.Plan.Sources)
	}
	if err := store.SelectLanding(ctx, LandingSelection{NodeIDs: []string{a, b}}); err != nil {
		t.Fatal(err)
	}
	ready(a)
	ready(b)
	if err := store.SelectLanding(ctx, LandingSelection{NodeIDs: []string{a, a}, Revision: 1}); err == nil {
		t.Fatal("duplicate server accepted")
	}
	targets, err := store.landingLatencyTargets(ctx, source)
	if err != nil || len(targets) != 2 {
		t.Fatalf("missing per-server latency targets: %+v %v", targets, err)
	}
	for _, config := range []struct{ app, owner, node string }{{"multi-proxy", a, source}, {"other-proxy", b, other}} {
		if err := store.ConfigureLandingProxy(ctx, config.app, LandingProxyInput{Enabled: true, LandingNodeID: config.owner}); err != nil {
			t.Fatal(err)
		}
		ready(config.owner)
		task := claim(config.node, false)
		if task == nil {
			t.Fatal("missing proxy task")
		}
		if err := store.completeLandingProxy(ctx, config.node, task.Revision, task.Attempt, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ConfigureLandingProxy(ctx, "multi-proxy", LandingProxyInput{Enabled: true, LandingNodeID: b, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	assertApplied := func(revision uint64, nodeID string) {
		t.Helper()
		view, err := store.Landing(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, proxy := range view.Proxies {
			if proxy.ApplicationID == "multi-proxy" {
				if proxy.Applied == nil || proxy.Applied.Revision != revision || proxy.Applied.LandingNodeID != nodeID {
					t.Fatalf("incorrect confirmed exit: %+v", proxy.Applied)
				}
				if proxy.Connection == "healthy" {
					t.Fatal("configuration receipt alone was reported as healthy")
				}
				return
			}
		}
		t.Fatal("missing proxy view")
	}
	assertApplied(1, a)
	if grants(a) != 1 || grants(b) != 2 {
		t.Fatal("switch revoked old grant before acknowledgement")
	}
	if err := store.SelectLanding(ctx, LandingSelection{NodeIDs: []string{b}, Revision: 1}); err == nil {
		t.Fatal("in-use old exit removed")
	}
	if claim(source, false) != nil {
		t.Fatal("switch claimed before new source grant applied")
	}
	ready(b)
	task := claim(source, false)
	if task == nil {
		t.Fatal("missing switch task")
	}
	if err := store.completeLandingProxy(ctx, source, task.Revision, task.Attempt, false); err != nil {
		t.Fatal(err)
	}
	assertApplied(1, a)
	if grants(a) != 1 || grants(b) != 2 {
		t.Fatal("failed switch removed a grant")
	}
	if err := store.ConfigureLandingProxy(ctx, "multi-proxy", LandingProxyInput{Enabled: true, LandingNodeID: a, Revision: 2}); err == nil {
		t.Fatal("unfinished switch accepted another target")
	}
	if err := store.ConfigureLandingProxy(ctx, "multi-proxy", LandingProxyInput{Enabled: true, LandingNodeID: b, Revision: 2}); err != nil {
		t.Fatal(err)
	}
	retry := claim(source, false)
	if retry == nil {
		t.Fatal("missing retry")
	}
	if err := store.completeLandingProxy(ctx, source, task.Revision, task.Attempt, true); err != nil {
		t.Fatal(err)
	}
	assertApplied(1, a)
	if grants(a) != 1 {
		t.Fatal("stale attempt revoked source")
	}
	if err := store.completeLandingProxy(ctx, source, retry.Revision, retry.Attempt, true); err != nil {
		t.Fatal(err)
	}
	assertApplied(2, b)
	if grants(a) != 0 || grants(b) != 2 {
		t.Fatal("successful switch did not retire exactly the old source")
	}
	if err := store.ConfigureLandingProxy(ctx, "multi-proxy", LandingProxyInput{Revision: 2}); err != nil {
		t.Fatal(err)
	}
	stop := claim(source, false)
	if stop == nil {
		t.Fatal("missing own-exit task")
	}
	if err := store.completeLandingProxy(ctx, source, stop.Revision, stop.Attempt, true); err != nil {
		t.Fatal(err)
	}
	assertApplied(3, "")
	if err := store.completeLandingProxy(ctx, source, retry.Revision, retry.Attempt, true); err != nil {
		t.Fatal(err)
	}
	assertApplied(3, "")
	if grants(b) != 1 {
		t.Fatal("restoration removed another node's source")
	}
	if err := store.SelectLanding(ctx, LandingSelection{NodeIDs: []string{b}, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	view, err := store.Landing(ctx)
	if err != nil || len(view.Servers) != 1 || view.Servers[0].NodeID != b {
		t.Fatalf("wrong retained server: %+v %v", view, err)
	}
}
