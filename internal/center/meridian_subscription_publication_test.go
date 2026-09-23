package center

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/networking"
)

func seedFailedMeridianSubscriptionPublication(t *testing.T, store *Store, suffix string) (string, string) {
	t.Helper()
	ctx := context.Background()
	profile := networking.Profile{
		ServiceAddress: "10.0.0.61", LANAddress: "10.0.0.61", PublicAddress: "203.0.113.61",
		EnabledKinds: []string{networking.KindLAN, networking.KindPublic}, DirectPublic: true,
	}
	node := enrollOrchestrationNode(t, store, "meridian-publication-"+suffix, NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: profile.ServiceAddress, Interface: "eth0", Kind: networking.KindLAN},
		{Address: profile.PublicAddress, Interface: "eth0", Kind: networking.KindPublic},
	}, profile)
	stamp := store.now().UTC().Format(time.RFC3339Nano)
	applicationID := "meridian-publication-app-" + suffix
	serviceID := "meridian-publication-service-" + suffix
	publicationID := "meridian-publication-entry-" + suffix
	if _, err := store.db.ExecContext(ctx, `INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,role,created_at,updated_at)
		VALUES(?,?,?,?,?,'','running','docker','master',?,?)`, applicationID, "Legacy subscription controller", node.ID, testSiteID(t, store), threeXUIAppKey, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO services(id,application_id,site_id,name,protocol,container_port,host_port,endpoint,source,app_protocol,management,status,created_at,updated_at)
		VALUES(?,?,?,?, 'http',8080,8080,?,'system',?,0,'ready',?,?)`, serviceID, applicationID, testSiteID(t, store), meridianSubscriptionServiceName, dockerruntime.CenterAlias+":8080", meridianSubscriptionProtocol, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,dns_provider,tls_enabled,desired_revision,applied_revision,status,last_error,created_at,updated_at)
		VALUES(?,?,'public_direct','site_gateway',?,'subscription.example.test','manual',1,1,0,'failed','gateway apply failed',?,?)`, publicationID, serviceID, node.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	siteID := testSiteID(t, store)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.upsertPublicationRoute(ctx, tx, publicationID, siteID, serviceID, node.ID, "subscription.example.test", "http", dockerruntime.CenterAlias+":8080", true, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := store.queueGatewayState(ctx, tx, node.ID, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE gateway_states SET status='failed',last_error='gateway apply failed' WHERE gateway_node_id=?`, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE routes SET status='failed',last_error='gateway apply failed' WHERE publication_id=?`, publicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE meridian_cutover SET state='publish',subscription_authority='meridian',legacy_controller_application_id=?,last_error='',updated_at=? WHERE id=1`, applicationID, stamp); err != nil {
		t.Fatal(err)
	}
	return node.ID, publicationID
}

func TestMeridianCutoverRestoresOnlyKnownSubscriptionOriginDrift(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	nodeID, publicationID := seedFailedMeridianSubscriptionPublication(t, store, "origin-drift")
	applicationID := "meridian-publication-app-origin-drift"
	serviceID := "meridian-publication-service-origin-drift"
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET endpoint=? WHERE id=?`, dockerruntime.CenterAlias+":2097", serviceID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.restoreMeridianSubscriptionOrigin(ctx, tx, applicationID, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var endpoint, publicationStatus, gatewayStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT endpoint FROM services WHERE id=?`, serviceID).Scan(&endpoint); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM publications WHERE id=?`, publicationID).Scan(&publicationStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM gateway_states WHERE gateway_node_id=?`, nodeID).Scan(&gatewayStatus); err != nil {
		t.Fatal(err)
	}
	if endpoint != dockerruntime.CenterAlias+":8080" || publicationStatus != "pending" || gatewayStatus != "pending" {
		t.Fatalf("origin=%q publication=%q gateway=%q", endpoint, publicationStatus, gatewayStatus)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE services SET endpoint='unrecognized:2097' WHERE id=?`, serviceID); err != nil {
		t.Fatal(err)
	}
	tx, err = store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := store.restoreMeridianSubscriptionOrigin(ctx, tx, applicationID, store.now().UTC()); err == nil {
		t.Fatal("unrecognized subscription origin was repaired")
	}
}

func TestMeridianCutoverReportsAndExplicitlyRetriesFailedSubscriptionPublication(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	nodeID, publicationID := seedFailedMeridianSubscriptionPublication(t, store, "retry")

	view, err := store.MeridianCutover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.LastError, "gateway apply failed") {
		t.Fatalf("cutover publication issue=%q", view.LastError)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.retryFailedMeridianSubscriptionPublication(ctx, tx, store.now().UTC()); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var publicationStatus, publicationError, gatewayStatus string
	var publicationRevision, gatewayRevision int64
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,status,last_error FROM publications WHERE id=?`, publicationID).Scan(&publicationRevision, &publicationStatus, &publicationError); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,status FROM gateway_states WHERE gateway_node_id=?`, nodeID).Scan(&gatewayRevision, &gatewayStatus); err != nil {
		t.Fatal(err)
	}
	if publicationRevision != 2 || publicationStatus != "pending" || publicationError != "" || gatewayRevision != 2 || gatewayStatus != "pending" {
		t.Fatalf("retry publication=%d/%s/%q gateway=%d/%s", publicationRevision, publicationStatus, publicationError, gatewayRevision, gatewayStatus)
	}
}

func TestMeridianSubscriptionPublicationRetryRejectsActiveGateway(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	nodeID, publicationID := seedFailedMeridianSubscriptionPublication(t, store, "active")
	if _, err := store.db.ExecContext(ctx, `UPDATE gateway_states SET status='applying' WHERE gateway_node_id=?`, nodeID); err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = store.retryFailedMeridianSubscriptionPublication(ctx, tx, store.now().UTC())
	_ = tx.Rollback()
	if err == nil || !strings.Contains(err.Error(), "still active") {
		t.Fatalf("active gateway retry error=%v", err)
	}
	var revision int64
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT desired_revision,status FROM publications WHERE id=?`, publicationID).Scan(&revision, &status); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || status != "failed" {
		t.Fatalf("active retry changed publication=%d/%s", revision, status)
	}
}
