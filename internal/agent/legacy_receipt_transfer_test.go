package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
)

func TestLegacyReceiptTransferRetiresOnlyConfirmedUnchangedEvidence(t *testing.T) {
	for _, mode := range []string{"success", "unavailable", "invalid-json", "wrong-digest", "wrong-id", "changed-local"} {
		t.Run(mode, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx := context.Background()
			seedLegacyReceipt(t, store, DeploymentTask{ID: "old-task", Kind: "application.apply", Attempt: 1}, "processing", nil)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/agents/node/legacy-receipts" || r.Header.Get("Authorization") != "Bearer credential" {
					t.Error("unexpected transfer endpoint or credential")
				}
				var input controlplane.LegacyReceiptImport
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if mode == "unavailable" {
					w.WriteHeader(503)
					return
				}
				if mode == "invalid-json" {
					w.Write([]byte(`{`))
					return
				}
				id := controlplane.LegacyReceiptArchiveID("node", input.Receipt.TaskID, input.Receipt.Attempt)
				digest := input.Digest
				if mode == "wrong-id" {
					id = "another-record"
				}
				if mode == "wrong-digest" {
					digest = "different"
				}
				if mode == "changed-local" {
					if _, err := store.db.Exec(`UPDATE task_receipts SET task_kind='changed' WHERE task_id='old-task'`); err != nil {
						t.Error(err)
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"archived": true, "id": id, "digest": digest})
			}))
			defer server.Close()
			if err := store.SaveConnection(ctx, testConnection(t, "node", "test", server.URL, "credential")); err != nil {
				t.Fatal(err)
			}
			client := Client{HTTPClient: server.Client(), executionSession: "test-registered-process-session"}
			err = client.TransferLegacyReceipts(ctx, store)
			if (err == nil) != (mode == "success") || calls != 1 {
				t.Fatalf("transfer calls=%d err=%v", calls, err)
			}
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_receipts`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode == "success" {
				want = 0
			}
			if count != want {
				t.Fatalf("remaining=%d want=%d", count, want)
			}
		})
	}
}
