package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

func TestAcknowledgedHistoryDoesNotBlockTaskReceiver(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedLegacyReceipt(t, store, DeploymentTask{ID: "already-completed", Kind: "gateway.routes.apply", Attempt: 1}, "acknowledged", nil)
	// No connection/session is needed to retain completed history locally.
	if err := (Client{}).TransferLegacyReceipts(context.Background(), store); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM task_receipts WHERE state='acknowledged'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("completed audit evidence removed: %d %v", count, err)
	}
}

func TestLegacyTransferReconnectsAfterTemporaryOutageButNotInvalidEvidence(t *testing.T) {
	for _, mode := range []string{"unavailable", "wrong-digest", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			seedLegacyReceipt(t, store, DeploymentTask{ID: "unfinished", Kind: "application.apply", Attempt: 1}, "processing", nil)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 && mode != "wrong-digest" {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				var input controlplane.LegacyReceiptImport
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
					return
				}
				digest := input.Digest
				if mode == "wrong-digest" {
					digest = "unverified"
				}
				json.NewEncoder(w).Encode(map[string]any{"archived": true, "id": controlplane.LegacyReceiptArchiveID("node", input.Receipt.TaskID, input.Receipt.Attempt), "digest": digest})
			}))
			defer server.Close()
			if err := store.SaveConnection(ctx, testConnection(t, "node", "test", server.URL, "credential")); err != nil {
				t.Fatal(err)
			}
			client := Client{HTTPClient: server.Client(), executionSession: "registered-session"}
			ready := client.transferLegacyReceiptsBeforeTasks(ctx, store, func(error) {
				if mode == "cancel" {
					cancel()
				}
			})
			wantCalls := 1
			if mode == "unavailable" {
				wantCalls = 2
			}
			if ready != (mode == "unavailable") || calls != wantCalls {
				t.Fatalf("receiver ready=%v transfer calls=%d mode=%s", ready, calls, mode)
			}
			var remaining int
			if err := store.db.QueryRow(`SELECT count(*) FROM task_receipts`).Scan(&remaining); err != nil || (remaining == 0) != ready {
				t.Fatalf("evidence retirement: remaining=%d ready=%v err=%v", remaining, ready, err)
			}
		})
	}
}

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
