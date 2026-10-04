package center

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
)

func TestExecutionPackageRecoveryRequiresExactReviewedAttempt(t *testing.T) {
	for _, mode := range []string{"reviewed", "unreviewed", "abandoned", "retired", "other-application", "other-manifest", "stale-attempt", "corrupt-evidence"} {
		t.Run(mode, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "package-recovery", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
			session := "package-recovery-process-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			task := AgentTask{ID: "upgrade-task", Kind: "application.apply", Attempt: 1, ApplicationID: "application", AppKey: cpaAppKey, Operation: "upgrade", ManifestSHA256: "reviewed-manifest"}
			auth, err := store.PersistExecutionAuthorization(ctx, node.ID, session, task)
			if err != nil {
				t.Fatal(err)
			}
			disposition, retired := "reexecute", ""
			switch mode {
			case "unreviewed":
				disposition = ""
			case "abandoned":
				disposition = "abandon"
			case "retired":
				retired = "retired-identity"
			}
			if _, err := store.db.Exec(`UPDATE task_executions SET state='failed',disposition=?,identity_retired_at=? WHERE id=?`, disposition, retired, auth.ID); err != nil {
				t.Fatal(err)
			}
			task.Attempt = 2
			switch mode {
			case "other-application":
				task.ApplicationID = "different-application"
			case "other-manifest":
				task.ManifestSHA256 = "different-manifest"
			case "stale-attempt":
				task.Attempt = 3
			case "corrupt-evidence":
				if _, err := store.db.Exec(`UPDATE task_executions SET sealed_task=X'00' WHERE id=?`, auth.ID); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			err = store.attachPackageRecovery(ctx, tx, node.ID, &task)
			if mode == "reviewed" {
				if err != nil || task.PackageRecovery == nil || task.PackageRecovery.ExecutionID != auth.ID || task.PackageRecovery.Attempt != 1 {
					t.Fatalf("reviewed evidence missing: %+v %v", task.PackageRecovery, err)
				}
				grant, err := store.persistExecutionAuthorization(ctx, tx, node.ID, session, task)
				if err != nil {
					t.Fatal(err)
				}
				var sealed []byte
				if err := tx.QueryRow(`SELECT sealed_task FROM task_executions WHERE id=?`, grant.ID).Scan(&sealed); err != nil {
					t.Fatal(err)
				}
				raw, err := secret.Open(store.key, sealed, []byte("execution-task:"+grant.ID))
				var retained AgentTask
				if err != nil || json.Unmarshal(raw, &retained) != nil || retained.PackageRecovery == nil || retained.PackageRecovery.ExecutionID != auth.ID {
					t.Fatal("recovery was not sealed into the one-use task")
				}
			} else if task.PackageRecovery != nil {
				t.Fatalf("unreviewed or mismatched evidence authorized recovery: %+v", task.PackageRecovery)
			}
		})
	}
}
