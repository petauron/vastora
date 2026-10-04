package center

import (
	"context"
	"testing"
)

func TestAgentReinstallSettledProjectionKeepsRealWork(t *testing.T) {
	s, node := prepareReinstallNode(t)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	empty := `["{\"revision\":1,\"listeners\":null,\"routes\":null}",0]`
	for _, test := range []struct {
		name, kind, state, intent string
		attempt, revision         int64
		want                      bool
	}{
		{"never installed empty", "gateway.routes.apply", "pending", empty, 0, 1, true},
		{"attempted empty", "gateway.routes.apply", "pending", empty, 1, 1, false},
		{"failed gateway", "gateway.routes.apply", "failed", empty, 0, 1, false},
		{"malformed gateway", "gateway.routes.apply", "pending", `["{}",0]`, 0, 1, false},
		{"applied gateway", "gateway.routes.apply", "pending", `["{\"revision\":1}",1]`, 0, 1, false},
		{"unknown intent", "gateway.routes.apply", "pending", `["{\"revision\":1,\"unexpected\":true}",0]`, 0, 1, false},
		{"unsettled landing", "landing.proxy.apply", "failed", "", 1, 1, false},
	} {
		got, err := reinstallSettledProjection(ctx, tx, test.kind, node.ID, test.state, test.attempt, test.revision, test.intent)
		if err != nil || got != test.want {
			t.Fatalf("%s: %t %v", test.name, got, err)
		}
	}
	if _, err = tx.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at,disposition) VALUES('settled',?,?,'landing.proxy.apply',1,'session','digest',X'1234','failed','reported','','','','abandon')`, node.ID, landingProxyTaskID(node.ID, 1)); err != nil {
		t.Fatal(err)
	}
	got, err := reinstallSettledProjection(ctx, tx, "landing.proxy.apply", node.ID, "failed", 1, 1, "")
	if err != nil || !got {
		t.Fatalf("explicit abandonment: %t %v", got, err)
	}
	got, err = reinstallSettledProjection(ctx, tx, "landing.proxy.apply", node.ID, "failed", 2, 1, "")
	if err != nil || got {
		t.Fatalf("new attempt hidden: %t %v", got, err)
	}
	if _, err = tx.Exec(`UPDATE task_executions SET kind='gateway.routes.apply' WHERE id='settled'`); err != nil {
		t.Fatal(err)
	}
	got, err = reinstallSettledProjection(ctx, tx, "gateway.routes.apply", node.ID, "pending", 0, 1, empty)
	if err != nil || got {
		t.Fatalf("previous gateway effects hidden: %t %v", got, err)
	}
}
