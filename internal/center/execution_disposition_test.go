package center

import (
	"context"
	"testing"
	"time"
)

// Validate every task-family statement against the real migrated schema. This
// does not replace each task family's end-to-end operator-disposition test.
func TestExecutionRequeueStatementsMatchSchemaAndDoNotCreateTasks(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	for _, kind := range []string{"node.ip-quality", "application.apply", "application.command", "agent.update", "agent.decommission", "landing.proxy.apply", "landing.server.apply", "gateway.routes.apply", "gateway.component.apply", "node.listener.apply", "tunnel.state.apply"} {
		t.Run(kind, func(t *testing.T) {
			query, args, err := executionRequeueStatement(AgentTask{ID: "nonexistent-task", Kind: kind, Attempt: 2, Revision: 7}, "nonexistent-agent", time.Now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.db.ExecContext(context.Background(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			if rows, err := result.RowsAffected(); err != nil || rows != 0 {
				t.Fatalf("unexpected task created or changed: %d %v", rows, err)
			}
			if kind == "agent.update" || kind == "agent.decommission" {
				return // These have the protected-helper resolution workflow.
			}
			query, args, err = executionAbandonStatement(AgentTask{ID: "nonexistent-task", Kind: kind, Attempt: 2, Revision: 7}, "nonexistent-agent", time.Now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				t.Fatal(err)
			}
			result, err = store.db.ExecContext(context.Background(), query, args...)
			if err != nil {
				t.Fatal(err)
			}
			if rows, err := result.RowsAffected(); err != nil || rows != 0 {
				t.Fatalf("abandon created or changed unrelated task: %d %v", rows, err)
			}
		})
	}
	if _, _, err := executionRequeueStatement(AgentTask{Kind: "arbitrary"}, "node", ""); err == nil {
		t.Fatal("unsupported task kind was accepted")
	}
}
