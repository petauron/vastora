package center

import (
	"context"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/recovery"
)

func TestExternalVolumeEvidenceCannotHideMissingOrChangedCoverage(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "recovery-volumes", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.81", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.81", LANAddress: "10.0.0.81", EnabledKinds: []string{networking.KindLAN}})
	now := store.now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	site := testSiteID(t, store)
	if _, err := store.db.Exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,image,status,runtime,created_at,updated_at) VALUES('recovery-app','CPA',?,?,'vastora-official/cpa','image','running','docker',?,?)`, node.ID, site, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,operation,state,application_id,created_at,updated_at) VALUES('recovery-deployment',?,'vastora-official/cpa','7.2.130','{}','{}','install','succeeded','recovery-app',?,?)`, node.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	policy, _ := catalog.OfficialRecoveryPolicy("vastora-official/cpa", "7.2.130")
	digest := recovery.Digest([]byte("external encrypted artifact"))
	value := ExternalRecoveryEvidence{FormatVersion: 1, ApplicationID: "recovery-app", NodeID: node.ID, SiteID: site, AppKey: "vastora-official/cpa", AppVersion: "7.2.130", PolicyVersion: policy.Version, Consistency: policy.Consistency, Volumes: policy.Volumes, Reference: "backup-system:restore-point-1", ArtifactDigest: digest, Encrypted: true, CreatedAt: now.Add(-time.Minute), RestoreVerifiedAt: now}
	for _, scenario := range []string{"missing volume", "digest", "identity", "unencrypted", "stale", "reference credential"} {
		bad := value
		switch scenario {
		case "missing volume":
			bad.Volumes = bad.Volumes[:1]
		case "digest":
			bad.ArtifactDigest = recovery.Digest([]byte("other"))
		case "identity":
			bad.NodeID = "other-node"
		case "unencrypted":
			bad.Encrypted = false
		case "stale":
			bad.CreatedAt = now.Add(-25 * time.Hour)
		case "reference credential":
			bad.Reference = "https://user:password@example.com/backup"
		}
		if err := store.RegisterExternalRecoveryEvidence(ctx, bad, digest); err == nil {
			t.Fatalf("%s evidence accepted", scenario)
		}
	}
	if err := store.RegisterExternalRecoveryEvidence(ctx, value, digest); err != nil {
		t.Fatal(err)
	}
	component := RecoveryComponent{Key: "application:recovery-app", ID: value.ApplicationID, NodeID: node.ID, SiteID: site, State: "action_required"}
	if err := store.evaluateExternalRecoveryEvidence(ctx, &component, policy, value.AppKey, value.AppVersion, now); err != nil {
		t.Fatal(err)
	}
	if component.State != "ready" {
		t.Fatal("valid operator evidence rejected")
	}
	component.State = "action_required"
	if err := store.evaluateExternalRecoveryEvidence(ctx, &component, policy, value.AppKey, "new-version", now); err != nil {
		t.Fatal(err)
	}
	if component.State == "ready" {
		t.Fatal("old artifact used for changed application")
	}
}
