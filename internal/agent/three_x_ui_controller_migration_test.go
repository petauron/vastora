package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	_ "modernc.org/sqlite"
)

func TestDemoteThreeXUIControllerPersistsWorkerRole(t *testing.T) {
	var restarted atomic.Bool
	var restartProbeFailed atomic.Bool
	var workerSettingsApplied atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/panel/api/inbounds/list", "/panel/api/nodes/list":
			_, _ = response.Write([]byte(`{"success":true,"obj":[]}`))
		case "/panel/api/setting/all":
			if restarted.Load() && restartProbeFailed.CompareAndSwap(false, true) {
				response.WriteHeader(http.StatusServiceUnavailable)
				_, _ = response.Write([]byte(`{"success":false,"msg":"restarting"}`))
				return
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{"subEnable":true,"subClashEnable":true}}`))
		case "/panel/api/setting/update":
			var settings map[string]any
			if json.NewDecoder(request.Body).Decode(&settings) == nil && settings["subEnable"] == false && settings["subClashEnable"] == false {
				workerSettingsApplied.Store(true)
			}
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		case "/panel/api/setting/restartPanel":
			restarted.Store(true)
			_, _ = response.Write([]byte(`{"success":true,"obj":{}}`))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	panelPort, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	installation := AppliedInstallation{
		InstanceID: "source-installation", ApplicationID: "source-application", AppKey: threeXUIKey, Version: "3.7.0",
		Config:  json.RawMessage(`{"timezone":"UTC","panel_port":` + strconv.Itoa(panelPort) + `,"enable_fail2ban":true,"vmess_aead_forced":false}`),
		Secrets: json.RawMessage(`{}`), ServiceAddress: endpoint.Hostname(), ApplicationRole: "master",
	}
	if _, err := store.RecordApplied(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	if err := demoteThreeXUIController(context.Background(), store, server.URL, "controller-token"); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.AppliedInstallation(context.Background(), threeXUIKey)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.ApplicationRole != "worker" || !workerSettingsApplied.Load() {
		t.Fatalf("demoted controller = role %q, settings applied %t", persisted.ApplicationRole, workerSettingsApplied.Load())
	}
}

func TestTransformThreeXUIControllerDatabaseSwapsLocalAndTargetInbounds(t *testing.T) {
	path := createThreeXUIMigrationDatabase(t)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := transformThreeXUIControllerDatabase(content, ThreeXUIControllerCommandTask{
		MigrationID:         "migration-1",
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
		"webListen":                  "100.64.0.20",
		"webPort":                    "3053",
		"subListen":                  "100.64.0.20",
		"subPort":                    "2096",
		"panelGuid":                  "target-panel-guid",
		"vastoraControllerMigration": "migration-1",
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

func TestThreeXUIControllerPromotionSurvivesRestartUntilCompletionAcknowledged(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	command := ThreeXUIControllerCommandTask{
		Action: "promote", MigrationID: "migration-1", ApplicationID: "target", SourceApplicationID: "source",
		SourceName: "source", SourceAddress: "100.64.0.10", SourcePanelPort: 2053, SourceRemoteNodeID: 7,
		BackupRevision: 1, SourceAPIToken: "new-token",
	}
	task := DeploymentTask{ID: "task-1", Attempt: 1, Kind: "application.command", ControllerCommand: &command}
	if completion, err := store.PrepareTaskReceipt(context.Background(), task); err != nil || completion != nil {
		t.Fatalf("prepare task receipt: completion=%#v err=%v", completion, err)
	}
	originalSecrets, _ := json.Marshal(map[string]string{"api_token": "old-token"})
	database := append([]byte("SQLite format 3\x00"), []byte("durable test state")...)
	promotion, err := store.BeginThreeXUIControllerPromotion(context.Background(), task.ID, command, threeXUIControllerPromotionRecovery{
		OriginalDatabase: database, TransformedDB: append([]byte(nil), database...), OriginalSecrets: originalSecrets,
		OldToken: "old-token", NewToken: "new-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if promotion.Phase != "prepared" {
		t.Fatalf("phase = %q, want prepared", promotion.Phase)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if completion, err := store.PrepareTaskReceipt(context.Background(), task); err != nil || completion != nil {
		t.Fatalf("resume task receipt: completion=%#v err=%v", completion, err)
	}
	for _, transition := range [][2]string{{"prepared", "imported"}, {"imported", "api_ready"}, {"api_ready", "role_configured"}, {"role_configured", "applied"}} {
		if err := store.AdvanceThreeXUIControllerPromotion(context.Background(), transition[0], transition[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.RecordTaskCompletion(context.Background(), TaskCompletion{
		TaskID: task.ID, Attempt: task.Attempt,
		Result: ApplicationTaskResult{ControllerCommand: &ThreeXUIControllerCommandResult{Action: "promote", BackupRevision: 1, SourceRemoteNodeID: 7}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ThreeXUIControllerPromotion(context.Background()); err != nil || !found {
		t.Fatalf("promotion before acknowledgement: found=%t err=%v", found, err)
	}
	if err := store.AcknowledgeTaskCompletion(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ThreeXUIControllerPromotion(context.Background()); err != nil || found {
		t.Fatalf("promotion after acknowledgement: found=%t err=%v", found, err)
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
