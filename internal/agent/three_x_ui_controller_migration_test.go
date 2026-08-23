package agent

import (
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

func TestTransformThreeXUIControllerDatabaseSwapsLocalAndTargetInbounds(t *testing.T) {
	path := createThreeXUIMigrationDatabase(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := transformThreeXUIControllerDatabase(content, ThreeXUIControllerCommandTask{
		SourceApplicationID: "source-controller", SourceName: "source-node", SourceAddress: "100.64.0.10", SourcePanelPort: 2053,
		SourceRemoteNodeID: 7, SourceAPIToken: "source-token",
	}, threeXUIControllerTargetSettings{Address: "100.64.0.20", PanelPort: 3053, PanelGUID: "target-panel-guid"})
	if err != nil {
		t.Fatal(err)
	}
	output := t.TempDir() + "/result.db"
	if err := os.WriteFile(output, result, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", output)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertInboundNodeID(t, db, 1, 7)
	assertInboundNodeID(t, db, 2, 0)
	assertInboundNodeID(t, db, 3, 8)
	var name, remark, address, token string
	var port int
	if err := db.QueryRow(`SELECT name, remark, address, port, api_token FROM nodes WHERE id = 7`).Scan(&name, &remark, &address, &port, &token); err != nil {
		t.Fatal(err)
	}
	if name != threeXUINodeAPIName("source-controller") || remark != "source-node" || address != "100.64.0.10" || port != 2053 || token != "source-token" {
		t.Fatalf("source placeholder = %q %q %q %d %q", name, remark, address, port, token)
	}
	wantSettings := map[string]string{
		"webListen": "100.64.0.20",
		"webPort":   "3053",
		"subListen": "100.64.0.20",
		"subPort":   "2096",
		"panelGuid": "target-panel-guid",
	}
	for key, want := range wantSettings {
		var got string
		if err := db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&got); err != nil {
			t.Fatalf("setting %s: %v", key, err)
		}
		if got != want {
			t.Fatalf("setting %s = %q, want %q", key, got, want)
		}
	}
}

func createThreeXUIMigrationDatabase(t *testing.T) string {
	t.Helper()
	path := t.TempDir() + "/source.db"
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE nodes (
		id INTEGER PRIMARY KEY, name TEXT, remark TEXT, scheme TEXT, address TEXT, port INTEGER, base_path TEXT, api_token TEXT,
		enable INTEGER, allow_private_address INTEGER, tls_verify_mode TEXT, pinned_cert_sha256 TEXT, inbound_sync_mode TEXT,
		inbound_tags TEXT, outbound_tag TEXT, guid TEXT, status TEXT, last_heartbeat INTEGER, latency_ms INTEGER,
		xray_version TEXT, panel_version TEXT, cpu_pct REAL, mem_pct REAL, uptime_secs INTEGER, net_up INTEGER, net_down INTEGER,
		last_error TEXT, xray_state TEXT, xray_error TEXT, config_dirty INTEGER, config_dirty_at INTEGER, inbounds_adopted_at INTEGER
	);
	CREATE TABLE inbounds (id INTEGER PRIMARY KEY, node_id INTEGER);
	CREATE TABLE settings (id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT, value TEXT);
	CREATE TABLE node_client_traffics (id INTEGER PRIMARY KEY, node_id INTEGER);
	CREATE TABLE node_client_ips (id INTEGER PRIMARY KEY, node_id INTEGER);
	INSERT INTO nodes(id, name, address, port, api_token) VALUES(7, 'target', '100.64.0.20', 2053, 'target-token'), (8, 'other', '100.64.0.30', 2053, 'other-token');
	INSERT INTO inbounds(id, node_id) VALUES(1, NULL), (2, 7), (3, 8);
	INSERT INTO settings(key, value) VALUES('webListen', '100.64.0.10'), ('webPort', '2053'), ('panelGuid', 'source-panel-guid');
	INSERT INTO node_client_traffics(id, node_id) VALUES(1, 7);
	INSERT INTO node_client_ips(id, node_id) VALUES(1, 7);`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertInboundNodeID(t *testing.T, db *sql.DB, inboundID, want int) {
	t.Helper()
	var nodeID sql.NullInt64
	if err := db.QueryRow(`SELECT node_id FROM inbounds WHERE id = ?`, inboundID).Scan(&nodeID); err != nil {
		t.Fatal(err)
	}
	if want == 0 && nodeID.Valid {
		t.Fatalf("inbound %d node = %d, want local", inboundID, nodeID.Int64)
	}
	if want != 0 && (!nodeID.Valid || nodeID.Int64 != int64(want)) {
		t.Fatalf("inbound %d node = %#v, want %d", inboundID, nodeID, want)
	}
}
