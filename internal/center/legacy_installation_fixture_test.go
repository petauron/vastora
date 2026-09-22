package center

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// Reconstruct a persisted installation from before Meridian. This does not
// authorize an obsolete package or execute a new legacy installation.
func seedLegacyControllerDeployment(t *testing.T, store *Store, node AgentCredential, address, apiToken string) DeploymentView {
	t.Helper()
	ctx := context.Background()
	var siteID string
	if err := store.db.QueryRowContext(ctx, `SELECT site_id FROM agents WHERE id=?`, node.ID).Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	applicationID := "legacy-controller-" + node.ID
	deploymentID := "legacy-install-" + node.ID
	manifest, err := os.ReadFile("testdata/legacy-proxy-alpha181.json")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,'Existing subscription controller',?,?,?,'','running','docker','master',?,?)`, applicationID, node.ID, siteID, threeXUIAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,service_address,operation,state,created_at,updated_at,application_id)
		VALUES(?,?,?,'3.7.2',?,'{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}',?,'install','succeeded',?,?,?)`, deploymentID, node.ID, threeXUIAppKey, manifest, address, stamp, stamp, applicationID); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]string{"api_token": apiToken})
	if err != nil {
		t.Fatal(err)
	}
	secretID, err := store.putSecret(ctx, tx, encoded, "application:"+applicationID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_secrets(application_id,secret_id,updated_at) VALUES(?,?,?)`, applicationID, secretID, stamp); err != nil {
		t.Fatal(err)
	}
	for name, port := range map[string]int{"panel": 2053, "subscription": 2096} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,status,created_at,updated_at)
			VALUES(?,?,?,?,'http',?,?,?,'catalog','ready',?,?)`, applicationID+"-"+name, applicationID, siteID, name, port, port, fmt.Sprintf("http://%s:%d", address, port), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	selectTestThreeXUIController(t, store, applicationID)
	return DeploymentView{ID: deploymentID, ApplicationID: applicationID, AgentID: node.ID, AppKey: threeXUIAppKey, State: "succeeded"}
}
