package center

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"testing"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/nodesite"
)

func readyMigrationSource(t *testing.T) (*Store, string, string) {
	t.Helper()
	s, node, _, app := nodeSiteCenterFixture(t)
	if _, err := s.configureNodeSite(context.Background(), app, nodeSiteInput{Hostname: "us1.example.com", Port: 24483}); err != nil {
		t.Fatal(err)
	}
	deliverNodeSiteForTest(t, s, app)
	finishNodeSiteForTest(t, s, node, claimNodeSiteForTest(t, s, node))
	return s, node, app
}

func TestNodeSiteHostnameMigrationRequiresPreparedBindingAndExplicitAcceptance(t *testing.T) {
	s, node, app := readyMigrationSource(t)
	ctx := context.Background()
	input := nodeSiteInput{Hostname: "us-random.example.com", PreviousHostname: "us1.example.com", Port: 24483}
	wrong := input
	wrong.PreviousHostname = "other.example.com"
	if _, err := s.configureNodeSite(ctx, app, wrong); err == nil {
		t.Fatal("foreign source hostname accepted")
	}
	v, err := s.configureNodeSite(ctx, app, input)
	if err != nil || v.State != "issuing" || v.MigrationFrom != input.PreviousHostname {
		t.Fatal(v, err)
	}
	if _, err = s.configureNodeSite(ctx, app, input); err != nil {
		t.Fatal("prepare retry", err)
	}
	var manifest catalog.AppManifest
	if err = json.Unmarshal([]byte(`{"runtime":{"kind":"native","version":2,"requiredCapabilities":["native-services","shared-caddy"]}}`), &manifest); err != nil {
		t.Fatal(err)
	}
	request := DeploymentRequest{AgentID: node, AppKey: "vastora-official/daylight", Config: json.RawMessage(`{"hostname":"us-random.example.com"}`)}
	if _, err = s.validateWebsiteHostnameConfigure(ctx, request, manifest); err == nil {
		t.Fatal("configured before certificate was prepared")
	}
	certificate := testManagedCertificate(t, input.Hostname)
	pair, err := tls.X509KeyPair([]byte(certificate.CertificatePEM), []byte(certificate.PrivateKeyPEM))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.deliverNodeSiteCertificate(ctx, app, input.Hostname, v.CADirectory, pair); err != nil {
		t.Fatal(err)
	}
	task := claimNodeSiteForTest(t, s, node)
	if task == nil || task.NodeSite.Action != "prepare-migration" || task.NodeSite.PreviousHostname != input.PreviousHostname {
		t.Fatal("missing bound preparation", task)
	}
	v, err = s.NodeSite(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	raw := nodeSiteResultJSON(t, nodesite.Result{Action: task.NodeSite.Action, Revision: task.Revision, Hostname: input.Hostname, State: "prepared", Fingerprint: v.Fingerprint})
	if err = s.completeNodeSite(ctx, commitProjectionOnlyForTest, node, task.ID, task.Attempt, true, "", raw); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.validateWebsiteHostnameConfigure(ctx, request, manifest); !ok || err != nil {
		t.Fatal("ready migration refused", ok, err)
	}
	manifest.Runtime.Kind = "systemd"
	if ok, err := s.validateWebsiteHostnameConfigure(ctx, request, manifest); !ok || err != nil {
		t.Fatal("existing reviewed v2 recipe refused", ok, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.validateNodeSiteMigrationAction(ctx, tx, app, "accept-migration"); err == nil {
		t.Fatal("accepted original installed binding")
	}
	tx.Rollback()
	if _, err = s.db.Exec(`UPDATE deployments SET config_json='{"hostname":"us-random.example.com","http_port":24481}' WHERE id='website-install'`); err != nil {
		t.Fatal(err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.validateNodeSiteMigrationAction(ctx, tx, app, "accept-migration"); err != nil {
		t.Fatal(err)
	}
	if err = s.queueNodeSitePhase(ctx, tx, app, "accept-migration"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	task = claimNodeSiteForTest(t, s, node)
	raw = nodeSiteResultJSON(t, nodesite.Result{Action: task.NodeSite.Action, Revision: task.Revision, Hostname: input.Hostname, State: "applied", Fingerprint: v.Fingerprint})
	for range 2 {
		if err = s.completeNodeSite(ctx, commitProjectionOnlyForTest, node, task.ID, task.Attempt, true, "", raw); err != nil {
			t.Fatal("accept replay", err)
		}
	}
	v, err = s.NodeSite(ctx, app)
	if err != nil || v.MigrationFrom != "" || v.State != "ready" {
		t.Fatal("completion", v, err)
	}
}

func TestNodeSiteHostnameMigrationCancelRestoresSealedPairWithoutDowngrade(t *testing.T) {
	s, node, app := readyMigrationSource(t)
	ctx := context.Background()
	before, err := s.NodeSite(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	var key []byte
	if err = s.db.QueryRow(`SELECT sealed_key FROM node_sites WHERE application_id=?`, app).Scan(&key); err != nil {
		t.Fatal(err)
	}
	v, err := s.configureNodeSite(ctx, app, nodeSiteInput{Hostname: "us-random.example.com", PreviousHostname: before.Hostname, Port: 24483})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.validateNodeSiteMigrationAction(ctx, tx, app, "cancel-migration"); err != nil {
		t.Fatal(err)
	}
	if err = s.queueNodeSitePhase(ctx, tx, app, "cancel-migration"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	task := claimNodeSiteForTest(t, s, node)
	raw := nodeSiteResultJSON(t, nodesite.Result{Action: "cancel-migration", Revision: task.Revision, Hostname: v.Hostname, State: "applied", Fingerprint: before.Fingerprint})
	for range 2 {
		if err = s.completeNodeSite(ctx, commitProjectionOnlyForTest, node, task.ID, task.Attempt, true, "", raw); err != nil {
			t.Fatal(err)
		}
	}
	after, err := s.NodeSite(ctx, app)
	if err != nil || after.Hostname != before.Hostname || after.Fingerprint != before.Fingerprint || after.Revision <= before.Revision || after.AppliedRevision != after.Revision || after.State != "ready" || after.MigrationFrom != "" {
		t.Fatal(after, err)
	}
	var restored []byte
	if err = s.db.QueryRow(`SELECT sealed_key FROM node_sites WHERE application_id=?`, app).Scan(&restored); err != nil || !bytes.Equal(key, restored) {
		t.Fatal("original sealed key changed", err)
	}
}

func TestNodeSiteHostnameSchema132PreservesReadyBinding(t *testing.T) {
	s, _, app := readyMigrationSource(t)
	ctx := context.Background()
	before, err := s.NodeSite(ctx, app)
	if err != nil {
		t.Fatal(err)
	}
	key := append([]byte(nil), s.key...)
	directory := s.dataDir
	var sealed []byte
	if err = s.db.QueryRow(`SELECT sealed_key FROM node_sites WHERE application_id=?`, app).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`ALTER TABLE managed_domains DROP COLUMN secondary_address; ALTER TABLE managed_domains DROP COLUMN secondary_record_id; ALTER TABLE managed_domains DROP COLUMN secondary_expected_address; ALTER TABLE node_sites DROP COLUMN tls_loopback_only; ALTER TABLE node_sites DROP COLUMN migration_from; ALTER TABLE node_sites DROP COLUMN migration_previous; DELETE FROM goose_db_version WHERE version_id>=132; PRAGMA user_version=131;`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	after, err := migrated.NodeSite(ctx, app)
	if err != nil || after != before || !bytes.Equal(key, migrated.key) {
		t.Fatal("schema migration changed the installed certificate identity", err)
	}
	var current []byte
	if err = migrated.db.QueryRow(`SELECT sealed_key FROM node_sites WHERE application_id=?`, app).Scan(&current); err != nil || !bytes.Equal(sealed, current) {
		t.Fatal("schema migration replaced the sealed key", err)
	}
}
