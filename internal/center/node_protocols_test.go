package center

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/nodeprotocol"
)

func nodeProtocolFixture(t *testing.T) (*Store, AgentCredential) {
	t.Helper()
	store, node := localRealityRemovalFixture(t)
	if _, err := store.db.Exec(`INSERT INTO three_x_ui_reality_guards(service_id,target_host,target_ip,server_name,companion_tag,status,created_at,updated_at) VALUES('local','www.intel.com','192.0.2.1','www.intel.com','','ready','','')`); err != nil {
		t.Fatal(err)
	}
	return store, node
}

func TestNodeProtocolsDefaultAndImmutablePhases(t *testing.T) {
	store, node := nodeProtocolFixture(t)
	ctx := context.Background()
	view, err := store.NodeProtocols(ctx, "local")
	if err != nil || !view.VLESS || view.HY2 || view.State != "succeeded" {
		t.Fatalf("bad default: %+v %v", view, err)
	}
	if _, err := store.ConfigureNodeProtocols(ctx, "local", nodeprotocol.Selection{}); err == nil {
		t.Fatal("empty selection accepted")
	}
	root, err := store.ConfigureNodeProtocols(ctx, "local", nodeprotocol.Selection{VLESS: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for index, phase := range []string{"prepare", "apply", "verify"} {
		task := claimLocalRemoval(t, store, node.ID)
		if task == nil || task.ProtocolCommand == nil || task.ProtocolCommand.Phase != phase || seen[task.ID] {
			t.Fatalf("phase reused a receipt identity: %+v", task)
		}
		seen[task.ID] = true
		var before []byte
		if err := store.db.QueryRow(`SELECT input_json FROM application_commands WHERE id=?`, task.ID).Scan(&before); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"protocolCommand": nodeprotocol.Result{Phase: phase}})
		if err := store.completeApplicationCommand(ctx, node.ID, task.ID, task.Attempt, true, "", raw, false); err != nil {
			t.Fatal(err)
		}
		var after []byte
		if err := store.db.QueryRow(`SELECT input_json FROM application_commands WHERE id=?`, task.ID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("completion mutated an immutable receipt")
		}
		current, err := store.ApplicationCommand(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		if index < 2 && (current.State != "pending" || current.ID == task.ID) {
			t.Fatal("root reported completion before the next phase")
		}
		if index == 2 && current.State != "succeeded" {
			t.Fatal("root did not follow verified completion")
		}
	}
}

func TestProtocolCertificateOnlyHydratesControllerDelivery(t *testing.T) {
	store, node := nodeProtocolFixture(t)
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	cert := managedCertificate{CertificatePEM: "private-test-leaf", PrivateKeyPEM: "private-test-key", NotAfter: store.now().Add(90 * 24 * time.Hour)}
	encoded, _ := json.Marshal(cert)
	id, err := store.putSecret(ctx, tx, encoded, "node-protocol-certificate:local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO three_x_ui_node_protocols(service_id,certificate_secret_id,certificate_hostname) VALUES('local',?,'local.example.test')`, id); err != nil {
		t.Fatal(err)
	}
	task := nodeprotocol.Task{Selection: nodeprotocol.Selection{VLESS: true, HY2: true}, Phase: "prepare", RootCommandID: "application-command-root", ApplicationID: "controller", ServiceID: "local", TargetAgentID: node.ID, ControllerAgentID: node.ID, InboundID: 9, InboundTag: "local-node", Hostname: "local.example.test"}
	if err := store.hydrateNodeProtocolTask(ctx, tx, &task); err != nil {
		t.Fatal(err)
	}
	if task.PrivateKeyPEM != "" {
		t.Fatal("private key sent to the port-preparation phase")
	}
	task.Phase = "apply"
	if err := store.hydrateNodeProtocolTask(ctx, tx, &task); err != nil {
		t.Fatal(err)
	}
	if task.PrivateKeyPEM != cert.PrivateKeyPEM {
		t.Fatal("controller did not receive its leaf key")
	}
	var sealed []byte
	if err := tx.QueryRow(`SELECT sealed FROM secrets WHERE id=?`, id).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), cert.PrivateKeyPEM) {
		t.Fatal("certificate key stored unencrypted")
	}
}

func TestProtocolVerificationRejectsWrongSibling(t *testing.T) {
	store, node := nodeProtocolFixture(t)
	ctx := context.Background()
	root, err := store.ConfigureNodeProtocols(ctx, "local", nodeprotocol.Selection{VLESS: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"prepare", "apply", "verify"} {
		task := claimLocalRemoval(t, store, node.ID)
		if task == nil {
			t.Fatal("task missing")
		}
		result := nodeprotocol.Result{Phase: phase}
		if phase == "verify" {
			result.HY2InboundID = 999
		}
		raw, _ := json.Marshal(map[string]any{"protocolCommand": result})
		if err := store.completeApplicationCommand(ctx, node.ID, task.ID, task.Attempt, true, "", raw, false); err != nil {
			t.Fatal(err)
		}
	}
	current, err := store.ApplicationCommand(ctx, root.ID)
	if err != nil || current.State != "failed" {
		t.Fatal("wrong final identity marked successful")
	}
}

func TestProtocolPortPreparationRetainsObservedNodeAndDomain(t *testing.T) {
	store, node := nodeProtocolFixture(t)
	ctx := context.Background()
	if _, err := store.ConfigureNodeProtocols(ctx, "local", nodeprotocol.Selection{VLESS: true}); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var cleanups []publicationCleanup
	if err := store.reconcileObservedApplication(ctx, tx, "controller", nil, store.now(), &cleanups); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := tx.QueryRow(`SELECT status FROM services WHERE id='local'`).Scan(&status); err != nil || status != "ready" {
		t.Fatal("container replacement removed node identity")
	}
	if err := tx.QueryRow(`SELECT status FROM publications WHERE id='local-entry'`).Scan(&status); err != nil || status != "ready" {
		t.Fatal("container replacement removed node domain")
	}
	if node.ID == "" {
		t.Fatal("fixture node missing")
	}
}

func TestNodeProtocolMigrationPreservesPendingWork(t *testing.T) {
	dir := t.TempDir()
	legacy := legacyMigrationStore(t, dir, 69)
	if _, err := legacy.db.Exec(`INSERT INTO application_commands(rowid,id,application_id,agent_id,gateway_node_id,kind,input_json,state,attempt,created_at,updated_at) VALUES(71,'application-command-old','application-v3','agent-v3','agent-v3','3xui.reality.rename','{"action":"rename"}','pending',4,'created','updated')`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var rowid, attempt int
	var input, state string
	if err := store.db.QueryRow(`SELECT rowid,attempt,input_json,state FROM application_commands WHERE id='application-command-old'`).Scan(&rowid, &attempt, &input, &state); err != nil {
		t.Fatal(err)
	}
	if rowid != 71 || attempt != 4 || input != `{"action":"rename"}` || state != "pending" {
		t.Fatal("migration altered existing work")
	}
	insert := `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,created_at,updated_at) VALUES('application-command-new','application-v3','agent-v3','agent-v3','3xui.protocols.configure','{}','pending','','')`
	if _, err := store.db.Exec(insert); err == nil {
		t.Fatal("active-operation exclusion lost")
	}
	if _, err := store.db.Exec(`UPDATE application_commands SET state='succeeded' WHERE id='application-command-old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(insert); err != nil {
		t.Fatalf("new protocol command rejected: %v", err)
	}
	backups, err := filepath.Glob(filepath.Join(dir, "migration-backups", fmt.Sprintf("center-v69-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatal("pre-migration backup missing")
	}
	var table string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE name='three_x_ui_node_protocols'`).Scan(&table); err != nil {
		t.Fatal("protocol table missing")
	}
	var trigger string
	if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE name='node_protocols_delete_certificate'`).Scan(&trigger); err != nil {
		t.Fatal("certificate cleanup missing")
	}
}
