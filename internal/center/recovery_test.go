package center

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/recovery"
)

func TestCenterBackupDoesNotProveClusterRecovery(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "unprotected-worker", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.80", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.80", LANAddress: "10.0.0.80", EnabledKinds: []string{networking.KindLAN}})
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET version = ? WHERE id = ?`, Version, node.ID); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "center.vastora")
	password := "a-protected-backup-password"
	if err := store.Backup(ctx, output, password); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterRecoveryArtifact(ctx, "center", output, password, recovery.Digest(store.key)); err != nil {
		t.Fatal(err)
	}
	view, err := store.RecoveryReadiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.State != "action_required" {
		t.Fatal("Center-only backup claimed cluster readiness")
	}
	for _, component := range view.Components {
		if component.Key == "center" && component.State != "ready" {
			t.Fatalf("Center artifact rejected: %s", component.Reason)
		}
		if component.Key == "agent:"+node.ID && component.State != "action_required" {
			t.Fatal("unprotected Agent claimed readiness")
		}
	}
	store.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	view, err = store.RecoveryReadiness(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Components[0].State != "action_required" || view.Components[0].Reason != "backup_evidence_expired" {
		t.Fatal("expired artifact claimed readiness")
	}
}
