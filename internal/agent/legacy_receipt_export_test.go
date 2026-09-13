package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

func TestLegacyReceiptExportPreservesEvidenceWithoutReplay(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, id := range []string{"a-processing", "b-completed"} {
		seedLegacyReceipt(t, store, DeploymentTask{ID: id, Kind: "application.apply", Attempt: 1}, "processing", nil)
	}
	original := []byte(`{"taskId":"b-completed","attempt":1,"result":{"secrets":{"password":"synthetic-secret"}},"unknownOldField":{"keep":true}}`)
	sealed, err := secret.Seal(store.key, original, legacyTaskCompletionContext("b-completed"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE task_receipts SET state='completed',sealed_completion=? WHERE task_id='b-completed'`, sealed); err != nil {
		t.Fatal(err)
	}
	first, digest, err := store.NextLegacyReceipt(ctx, "")
	if err != nil || first == nil || first.TaskID != "a-processing" || len(digest) != 64 {
		t.Fatalf("first export: %v %v", first != nil, err)
	}
	second, digest2, err := store.NextLegacyReceipt(ctx, first.TaskID)
	if err != nil || second == nil || string(second.Completion) != string(original) || digest == digest2 {
		t.Fatalf("completion altered: %v %v", second != nil, err)
	}
	if item, _, err := store.NextLegacyReceipt(ctx, second.TaskID); err != nil || item != nil {
		t.Fatal("pagination did not terminate")
	}
	again, digest3, err := store.NextLegacyReceipt(ctx, first.TaskID)
	if err != nil || again == nil || digest3 != digest2 {
		t.Fatal("repeated read changed evidence")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_receipts WHERE state<>'acknowledged'`).Scan(&count); err != nil || count != 2 {
		t.Fatal("export retired evidence before Center acknowledgement")
	}
}

func TestLegacyReceiptExportRejectsDamagedEvidence(t *testing.T) {
	for _, mode := range []string{"ciphertext", "identity", "oversize", "missing"} {
		t.Run(mode, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			seedLegacyReceipt(t, store, DeploymentTask{ID: "receipt", Kind: "application.apply", Attempt: 1}, "processing", nil)
			var sealed []byte
			switch mode {
			case "ciphertext":
				sealed = []byte("invalid")
			case "identity":
				raw, _ := json.Marshal(taskCompletion{TaskID: "different", Attempt: 1})
				sealed, err = secret.Seal(store.key, raw, legacyTaskCompletionContext("receipt"))
				if err != nil {
					t.Fatal(err)
				}
			case "oversize":
				sealed = make([]byte, controlplane.LegacyReceiptMaxCompletionBytes+1025)
			}
			if _, err := store.db.Exec(`UPDATE task_receipts SET state='completed',sealed_completion=? WHERE task_id='receipt'`, sealed); err != nil {
				t.Fatal(err)
			}
			if item, _, err := store.NextLegacyReceipt(ctx, ""); err == nil || item != nil {
				t.Fatal("damaged evidence accepted")
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_receipts`).Scan(&count); err != nil || count != 1 {
				t.Fatal("invalid evidence discarded")
			}
		})
	}
}
