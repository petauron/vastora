package center

import (
	"context"
	"fmt"
	"github.com/petauron/vastora/internal/networking"
	"net/http"
	"net/http/httptest"
	"testing"
)

func executionViewsForTest(ctx context.Context, store *Store) ([]ExecutionView, error) {
	page, err := store.ListExecutions(ctx, 0)
	return page.Executions, err
}

func TestExecutionPagesRetainOldUnresolvedHistory(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "history", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.23", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.23", LANAddress: "10.0.0.23", EnabledKinds: []string{networking.KindLAN}})
	insert := func(index int) {
		t.Helper()
		id := fmt.Sprintf("history-%d", index)
		state := "succeeded"
		if index == 0 {
			state = "unknown"
		}
		_, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at) VALUES(?,?,?,'application.apply',1,'session','digest',X'00',?,'apply','','same-time','same-time')`, id, node.ID, id, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 205; i++ {
		insert(i)
	}
	page, err := store.ListExecutions(ctx, 0)
	if err != nil || len(page.Executions) != 100 || page.NextCursor == 0 {
		t.Fatalf("first page: %d %d %v", len(page.Executions), page.NextCursor, err)
	}
	seen := map[string]bool{}
	for _, item := range page.Executions {
		if item.CanConfirm {
			t.Fatal("missing result offered for confirmation")
		}
		seen[item.ID] = true
	}
	insert(205) // A newer insertion must not shift the remaining pages.
	for page.NextCursor != 0 {
		page, err = store.ListExecutions(ctx, page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Executions {
			if item.CanConfirm {
				t.Fatal("unknown result without evidence offered for confirmation")
			}
			if seen[item.ID] {
				t.Fatal("duplicate across pages")
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != 205 || !seen["history-0"] || seen["history-205"] {
		t.Fatalf("history lost or changed snapshot traversal: %d", len(seen))
	}
	if _, err := store.ListExecutions(ctx, -1); err == nil {
		t.Fatal("negative cursor accepted")
	}
	cookie, _, err := store.CreateFirstAdmin(ctx, "operator", "test-only-long-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"before=0", "before=-1", "before=", "before=x", "before=1&before=2", "other=1", "before=%xx"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/executions?"+q, nil)
		r.AddCookie(&http.Cookie{Name: "vastora_session", Value: cookie})
		w := httptest.NewRecorder()
		NewServer(store, "", false).Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid cursor %s accepted: %d", q, w.Code)
		}
	}
}
