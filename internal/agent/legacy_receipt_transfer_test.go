package agent

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type receiptGRPCFixture struct {
	agentpb.UnimplementedAgentControlServer
	importReceipt func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error)
}

func (f receiptGRPCFixture) ImportLegacyReceipts(ctx context.Context, input *agentpb.AgentDocument) (*agentpb.Document, error) {
	return f.importReceipt(ctx, input)
}
func receiptFixtureServer(importReceipt func(context.Context, *agentpb.AgentDocument) (*agentpb.Document, error)) *httptest.Server {
	server := grpc.NewServer()
	agentpb.RegisterAgentControlServer(server, receiptGRPCFixture{importReceipt: importReceipt})
	return httptest.NewServer(h2c.NewHandler(server, &http2.Server{}))
}

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
			server := receiptFixtureServer(func(_ context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
				calls++
				if calls == 1 && mode != "wrong-digest" {
					return nil, status.Error(codes.Unavailable, "temporary outage")
				}
				var input controlplane.LegacyReceiptImport
				if err := json.Unmarshal(request.Json, &input); err != nil {
					t.Error(err)
					return nil, err
				}
				digest := input.Digest
				if mode == "wrong-digest" {
					digest = "unverified"
				}
				body, err := json.Marshal(map[string]any{"archived": true, "id": controlplane.LegacyReceiptArchiveID("node", input.Receipt.TaskID, input.Receipt.Attempt), "digest": digest})
				return &agentpb.Document{Json: body}, err
			})
			defer server.Close()
			if err := store.SaveConnection(ctx, testConnection(t, "node", "test", server.URL, "credential")); err != nil {
				t.Fatal(err)
			}
			client := Client{executionSession: "registered-session"}
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
			server := receiptFixtureServer(func(ctx context.Context, request *agentpb.AgentDocument) (*agentpb.Document, error) {
				calls++
				md, _ := metadata.FromIncomingContext(ctx)
				if request.AgentId != "node" || len(md.Get("authorization")) != 1 || md.Get("authorization")[0] != "Bearer credential" {
					t.Error("unexpected transfer identity or credential")
				}
				var input controlplane.LegacyReceiptImport
				if err := json.Unmarshal(request.Json, &input); err != nil {
					t.Error(err)
					return nil, err
				}
				if mode == "unavailable" {
					return nil, status.Error(codes.Unavailable, "unavailable")
				}
				if mode == "invalid-json" {
					return &agentpb.Document{Json: []byte(`{`)}, nil
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
				body, err := json.Marshal(map[string]any{"archived": true, "id": id, "digest": digest})
				return &agentpb.Document{Json: body}, err
			})
			defer server.Close()
			if err := store.SaveConnection(ctx, testConnection(t, "node", "test", server.URL, "credential")); err != nil {
				t.Fatal(err)
			}
			client := Client{executionSession: "test-registered-process-session"}
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
