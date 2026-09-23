package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/networking"
)

func TestLegacyMeridianImportPreservesConvergedEntryTrafficPlan(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "entry-cap-import", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.90", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.90", LANAddress: "10.0.0.90", EnabledKinds: []string{networking.KindLAN}})
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	reset := store.now().UTC().AddDate(0, 1, 0).Format(time.RFC3339Nano)
	const applicationID = "entry-cap-application"
	const serviceID = "entry-cap-service"
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'','running','docker','master',?,?)`, applicationID, "Legacy entry", node.ID, testSiteID(t, store), threeXUIAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,display_name,region_code,protocol,container_port,host_port,endpoint,source,app_protocol,management,observed_listen,status,created_at,updated_at)
		VALUES(?,?,?,?,?,?,'tcp',443,443,?,'observed','vless/tcp/reality',0,'0.0.0.0','ready',?,?)`, serviceID, applicationID, testSiteID(t, store), "inbound-1", "Entry", "US", "10.0.0.90:443", stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_inbound_plans(service_id,inbound_tag,total_bytes,reset_day,next_reset_at,last_reset_at,status,updated_at)
		VALUES(?,'legacy-entry',500,15,?,'','active',?)`, serviceID, reset, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,created_at,updated_at)
		VALUES('entry-cap-publication',?,'public_shared_443','application_node',?,'entry.example.test','www.microsoft.com','manual',0,1,1,'ready',?,?)`, serviceID, node.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO three_x_ui_reality_guards(service_id,target_host,target_ip,server_name,companion_tag,status,created_at,updated_at)
		VALUES(?,'www.microsoft.com','8.8.8.8','www.microsoft.com','','ready',?,?)`, serviceID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	legacy := meridianruntime.LegacyEndpoint{InboundID: 1, Tag: "legacy-entry", DisplayName: "Entry", Port: 443,
		TotalBytes: 500, UsedBytes: 240, Target: "www.microsoft.com:443", ServerNames: []string{"www.microsoft.com"},
		PrivateKey: "private", PublicKey: "public", ShortIDs: []string{"0123456789abcdef"}, VLESSEnabled: true}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	resolved, err := store.resolveLegacyMeridianEndpoint(ctx, tx, applicationID, legacy)
	if err != nil || resolved.totalBytes != 500 || resolved.usedBytes != 240 || resolved.resetDay != 15 || resolved.nextResetAt != reset {
		t.Fatalf("imported entry cap=%d used=%d day=%d next=%q err=%v", resolved.totalBytes, resolved.usedBytes, resolved.resetDay, resolved.nextResetAt, err)
	}
	legacy.Target = "8.8.8.8:443"
	if _, err := store.resolveLegacyMeridianEndpoint(ctx, tx, applicationID, legacy); err != nil {
		t.Fatalf("verified public target pin was rejected: %v", err)
	}
	legacy.Target = "8.8.4.4:443"
	if _, err := store.resolveLegacyMeridianEndpoint(ctx, tx, applicationID, legacy); err == nil {
		t.Fatal("target IP that differed from the verified guard was accepted")
	}
	legacy.Target = "8.8.8.8:443"
	legacy.TotalBytes++
	if _, err := store.resolveLegacyMeridianEndpoint(ctx, tx, applicationID, legacy); err == nil {
		t.Fatal("import accepted a cap that differed from the Center-managed plan")
	}
}
