package center

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestTaskExecutionLookupUsesLatestAttemptWithoutMutating(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "lookup", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.23", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.23", LANAddress: "10.0.0.23", EnabledKinds: []string{networking.KindLAN}})
	insert := func(id, taskID, state string, attempt int) {
		t.Helper()
		_, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,sealed_result,state,phase,expires_at,created_at,updated_at) VALUES(?,?,?,'application.apply',?,'session','digest',X'00',X'00',?,'reported','','same-time','same-time')`, id, node.ID, taskID, attempt, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("old-attempt", "selected-task", "unknown", 1)
	for i := 0; i < 105; i++ {
		insert(fmt.Sprintf("unrelated-%d", i), fmt.Sprintf("other-task-%d", i), "unknown", 1)
	}
	cookie, _, err := store.CreateFirstAdmin(ctx, "operator", "test-only-long-password")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	get := func(taskID string, authenticated bool, want int) ExecutionView {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/"+taskID+"/execution", nil)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "vastora_session", Value: cookie})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("lookup: %d want %d: %s", w.Code, want, w.Body.String())
		}
		var value ExecutionView
		if want == http.StatusOK {
			if json.Unmarshal(w.Body.Bytes(), &value) != nil || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("invalid or cacheable execution response")
			}
			for _, field := range []string{"sealed", "session", "digest", "credential", "secret"} {
				if strings.Contains(w.Body.String(), field) {
					t.Fatalf("private execution evidence leaked: %s", field)
				}
			}
		}
		return value
	}
	get("selected-task", false, http.StatusUnauthorized)
	get("missing-task", true, http.StatusNotFound)
	first := get("selected-task", true, http.StatusOK)
	if first.ID != "old-attempt" || first.CanConfirm {
		t.Fatalf("wrong or unproven attempt: %+v", first)
	}
	insert("new-attempt", "selected-task", "succeeded", 3)
	insert("older-backfill", "selected-task", "unknown", 2)
	latest := get("selected-task", true, http.StatusOK)
	if latest.ID != "new-attempt" || latest.State != "succeeded" || latest.CanConfirm {
		t.Fatalf("lookup fell back to old unresolved attempt: %+v", latest)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_executions WHERE task_id='selected-task' AND disposition=''`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("read mutated execution: %d %v", count, err)
	}
	if _, err := store.db.Exec(`UPDATE task_executions SET state='unknown',disposition='abandon' WHERE id='new-attempt'`); err != nil {
		t.Fatal(err)
	}
	disposed := get("selected-task", true, http.StatusOK)
	if disposed.ID != "new-attempt" || disposed.Disposition != "abandon" || disposed.CanConfirm {
		t.Fatalf("lookup lost latest disposition: %+v", disposed)
	}
}
