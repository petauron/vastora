package center

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestLegacyReceiptAbandonPreservesSecretsAndTerminatesExactAttempt(t *testing.T) {
	for _, mode := range []string{"success", "audit-failure", "changed-attempt", "missing-business", "obsolete-kind", "not-admin", "not-stopped"} {
		t.Run(mode, func(t *testing.T) {
			store := openOrchestrationStore(t)
			defer store.Close()
			ctx := context.Background()
			node := enrollOrchestrationNode(t, store, "legacy-abandon", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}})
			if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`), AuthorizedCapabilities: testCapabilityGrant("root")}); err != nil {
				t.Fatal(err)
			}
			task := claimTask(t, store, node)
			session := "legacy-abandon-current-process-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			stamp := store.now().UTC().Format(time.RFC3339Nano)
			completion, _ := json.Marshal(map[string]any{"taskId": task.ID, "attempt": task.Attempt, "result": map[string]any{"generatedSecrets": map[string]string{"management_key": "retained-test-secret"}}})
			receipt := controlplane.LegacyReceipt{TaskID: task.ID, Kind: task.Kind, Attempt: task.Attempt, RuntimeGeneration: task.RequiredRuntimeGeneration, TaskHash: make([]byte, 32), State: "completed", Completion: completion, CreatedAt: stamp, UpdatedAt: stamp}
			if mode == "obsolete-kind" {
				receipt.Kind = "retired.operation"
			}
			_, digest, err := controlplane.EncodeLegacyReceipt(receipt)
			if err != nil {
				t.Fatal(err)
			}
			input := controlplane.LegacyReceiptImport{SessionID: session, Digest: digest, Receipt: receipt}
			id, err := store.ImportLegacyReceipt(ctx, node.ID, node.Credential, input)
			if err != nil {
				t.Fatal(err)
			}
			cookie, _, err := store.CreateFirstAdmin(ctx, "legacy-abandon-admin", "test-only-strong-password")
			if err != nil {
				t.Fatal(err)
			}
			adminID, err := store.SessionAdminID(ctx, cookie)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "not-admin" {
				adminID = "missing-admin"
			}
			if mode == "audit-failure" {
				if _, err := store.db.Exec(`CREATE TRIGGER reject_legacy_abandon BEFORE UPDATE ON task_executions WHEN NEW.disposition='abandon' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-attempt" {
				if _, err := store.db.Exec(`UPDATE deployments SET attempt=attempt+1 WHERE id=?`, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "missing-business" {
				if _, err := store.db.Exec(`DELETE FROM deployments WHERE id=?`, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			var before []byte
			if err := store.db.QueryRow(`SELECT sealed_task FROM task_executions WHERE id=?`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			decision := controlplane.ExecutionDisposition{Action: "abandon", ExecutionStopped: mode != "not-stopped", Note: "Verified old execution stopped; retain archive and existing application data."}
			err = store.AbandonLegacyReceipt(ctx, id, adminID, decision)
			archiveRetired := mode == "success" || mode == "changed-attempt" || mode == "missing-business" || mode == "obsolete-kind"
			if (err == nil) != archiveRetired {
				t.Fatalf("abandon: %v", err)
			}
			var state, disposition, business string
			var after []byte
			if err := store.db.QueryRow(`SELECT state,disposition,sealed_task FROM task_executions WHERE id=?`, id).Scan(&state, &disposition, &after); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT COALESCE((SELECT state FROM deployments WHERE id=?),'missing')`, task.ID).Scan(&business); err != nil {
				t.Fatal(err)
			}
			if state != "unknown" || !bytes.Equal(before, after) || bytes.Contains(after, []byte("retained-test-secret")) {
				t.Fatal("archive evidence changed or leaked")
			}
			if mode == "success" {
				if disposition != "abandon" || business != "failed" {
					t.Fatal("old attempt was not terminated atomically")
				}
				if err := store.AbandonLegacyReceipt(ctx, id, adminID, decision); err == nil {
					t.Fatal("duplicate disposition accepted")
				}
				if again, err := store.ImportLegacyReceipt(ctx, node.ID, node.Credential, input); err != nil || again != id {
					t.Fatal("disposed archive no longer acknowledges identical migration")
				}
				if next, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0); err != nil || next != nil {
					t.Fatal("abandoned task was replayed")
				}
			} else if mode == "changed-attempt" || mode == "obsolete-kind" {
				if disposition != "abandon" || business != "running" {
					t.Fatal("archive retirement changed newer attempt")
				}
			} else if mode == "missing-business" {
				if disposition != "abandon" || business != "missing" {
					t.Fatal("archive retirement recreated missing task")
				}
			} else if disposition != "" || business != "running" {
				t.Fatal("failed disposition changed business state")
			}
			if view, err := store.InspectLegacyReceipt(ctx, id); err != nil || !view.HasCompletion {
				t.Fatal("retained completion is no longer inspectable")
			}
		})
	}
}
