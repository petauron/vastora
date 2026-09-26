package center

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVersion94ProjectsUncertainMeridianFailureForExplicitRecovery(t *testing.T) {
	directory := t.TempDir()
	legacy := legacyMigrationStore(t, directory, 93)
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := legacy.db.ExecContext(ctx, `UPDATE applications SET app_key='vastora-official/meridian' WHERE id='application-v3'`); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.ExecContext(ctx, `INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('uncertain-endpoint-key',X'010203',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.ExecContext(ctx, `INSERT INTO meridian_endpoints(id,application_id,service_id,inbound_tag,listen_port,advertise_host,advertise_port,target,target_ip,server_names_json,private_key_secret_id,public_key,short_ids_json,desired_revision,applied_revision,runtime_healthy,status,created_at,updated_at)
		VALUES('uncertain-endpoint','application-v3','service-v3','uncertain-entry',443,'entry.example.test',443,'www.example.com:443','203.0.113.20','["www.example.com"]','uncertain-endpoint-key','test-public-key','["abcd"]',2,1,0,'applying',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,site_id,display_name,agent_id,gateway_node_id,kind,input_json,state,reconciliation_required,attempt,error,created_at,updated_at)
		VALUES('uncertain-command','application-v3','site-v3','Legacy App','agent-v3','agent-v3','meridian.runtime.apply','{"endpointId":"uncertain-endpoint"}','failed',1,1,'legacy worker drift',?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.db.ExecContext(ctx, `INSERT INTO meridian_deployments(endpoint_id,desired_revision,desired_sha256,command_id,status,updated_at)
		VALUES('uncertain-endpoint',2,?,'uncertain-command','applying',?)`, strings.Repeat("a", 64), stamp); err != nil {
		t.Fatal(err)
	}
	if err := legacy.db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated := openBeforeCatalogMaintenanceForTest(t, directory)
	t.Cleanup(func() { _ = migrated.Close() })
	var endpointStatus, deploymentStatus, lastError string
	if err := migrated.db.QueryRowContext(ctx, `SELECT endpoint.status,deployment.status,endpoint.last_error FROM meridian_endpoints endpoint JOIN meridian_deployments deployment ON deployment.endpoint_id=endpoint.id WHERE endpoint.id='uncertain-endpoint'`).Scan(&endpointStatus, &deploymentStatus, &lastError); err != nil {
		t.Fatal(err)
	}
	if endpointStatus != "failed" || deploymentStatus != "failed" || lastError != "legacy worker drift" {
		t.Fatalf("endpoint=%q deployment=%q error=%q", endpointStatus, deploymentStatus, lastError)
	}
	backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v93-before-v%d-*.db", centerSchemaVersion)))
	if err != nil || len(backups) != 1 {
		t.Fatalf("pre-migration backup count=%d err=%v", len(backups), err)
	}
	if info, err := os.Stat(backups[0]); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pre-migration backup permissions: %v", err)
	}
	finishCatalogMaintenanceFixture(t, migrated, directory)
}
