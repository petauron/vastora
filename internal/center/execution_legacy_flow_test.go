package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestLegacyReceiptCutoverPreservesLostArchiveAcknowledgement(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "cutover-test", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.24", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.24", LANAddress: "10.0.0.24", EnabledKinds: []string{networking.KindLAN}})
	private, _, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	var uploads, claims atomic.Int64
	var lose atomic.Bool
	lose.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/legacy-receipts") {
			uploads.Add(1)
			if lose.Swap(false) {
				recorder := httptest.NewRecorder()
				handler.ServeHTTP(recorder, r)
				if recorder.Code != 200 {
					t.Errorf("archive failed: %d", recorder.Code)
				}
				w.WriteHeader(503)
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/tasks/next") {
			claims.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	directory := t.TempDir()
	local, err := agent.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if err := local.SaveConnection(ctx, agent.Connection{AgentID: node.ID, Name: "cutover", CenterURL: server.URL, Credential: node.Credential, PrivateKey: private}); err != nil {
		t.Fatal(err)
	}
	// Only the fixture writes legacy bytes; the current Agent has no receipt writer.
	fixture, err := sql.Open("sqlite", filepath.Join(directory, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("legacy-command"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, seedErr := fixture.Exec(`INSERT INTO task_receipts(task_id,task_kind,runtime_generation,attempt,task_hash,state,created_at,updated_at)
		VALUES('legacy-command','application.apply',0,1,?,'processing',?,?)`, digest[:], now, now)
	closeErr := fixture.Close()
	if seedErr != nil || closeErr != nil {
		t.Fatalf("seed legacy archive: %v %v", seedErr, closeErr)
	}
	executor := &executionFlowExecutor{}
	client := agent.Client{HTTPClient: server.Client(), Executor: executor, Capabilities: agent.Capabilities{Docker: true}}
	run := func() {
		t.Helper()
		runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() { defer close(done); client.RunTasks(runCtx, local, func(error) { cancel() }) }()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Fatal("cutover did not stop after error")
		}
	}
	run()
	if item, _, err := local.NextLegacyReceipt(ctx, ""); err != nil || item == nil {
		t.Fatal("lost acknowledgement discarded local evidence")
	}
	if claims.Load() != 0 {
		t.Fatal("claim bypassed unconfirmed transfer")
	}
	run()
	if item, _, err := local.NextLegacyReceipt(ctx, ""); err != nil || item != nil {
		t.Fatal("confirmed evidence was not retired")
	}
	views, err := executionViewsForTest(ctx, store)
	if err != nil || len(views) != 1 || views[0].Kind != "legacy.receipt" || views[0].State != "unknown" {
		t.Fatal("cutover lost unknown execution fence")
	}
	if uploads.Load() != 2 || claims.Load() != 1 || executor.calls.Load() != 0 || executor.restored.Load() != 0 || executor.maintained.Load() != 0 {
		t.Fatalf("cutover replayed effects: uploads=%d claims=%d effects=%d", uploads.Load(), claims.Load(), executor.calls.Load())
	}
}
