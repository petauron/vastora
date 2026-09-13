package center

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/networking"
)

func TestExecutionClaimControlAuditAtomic(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	cookie, _, err := store.CreateFirstAdmin(ctx, "operator", "test-only-long-password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := store.SessionAdminID(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	for _, paused := range []bool{true, false, true} {
		if err := store.SetExecutionClaimControl(ctx, admin, paused); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.db.Query(`SELECT paused,actor,created_at FROM execution_claim_control_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var history []bool
	for rows.Next() {
		var paused bool
		var actor, timestamp string
		if err := rows.Scan(&paused, &actor, &timestamp); err != nil {
			t.Fatal(err)
		}
		if actor != admin || timestamp == "" {
			t.Fatal("missing audit identity or time")
		}
		history = append(history, paused)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(history) != 3 || !history[0] || history[1] || !history[2] {
		t.Fatalf("audit history lost: %v", history)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER reject_control_audit BEFORE INSERT ON execution_claim_control_events BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.SetExecutionClaimControl(ctx, admin, false); err == nil {
		t.Fatal("changed control without audit")
	}
	control, err := store.ExecutionClaimControl(ctx)
	if err != nil || !control.Paused {
		t.Fatalf("failed audit did not roll back control: %+v %v", control, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_claim_control_events`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("partial audit: %d %v", count, err)
	}
}

func TestExecutionClaimPausePreservesIntentAndObservation(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	node := enrollOrchestrationNode(t, store, "claim-control", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.23", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.23", LANAddress: "10.0.0.23", EnabledKinds: []string{networking.KindLAN}})
	heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.123", true)
	queued, err := store.QueueAgentUpdate(ctx, node.ID, "0.1.0-alpha.124")
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf, err := store.CreateFirstAdmin(ctx, "operator", "test-only-long-password")
	if err != nil {
		t.Fatal(err)
	}
	adminID, err := store.SessionAdminID(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetExecutionClaimControl(ctx, "not-admin", true); err == nil {
		t.Fatal("non-admin paused claims")
	}
	handler := NewServer(store, "", false).Handler()
	for _, tc := range []struct {
		body, token string
		want        int
	}{
		{`{"paused":true}`, "", 401},
		{`{}`, csrf, 400},
		{`{"paused":null}`, csrf, 400},
		{`{"paused":true}`, csrf, 200},
	} {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/execution-claim-control", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "vastora_session", Value: cookie})
		r.Header.Set("X-CSRF-Token", tc.token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("pause API: %d want %d", w.Code, tc.want)
		}
	}
	value, err := store.ExecutionClaimControl(ctx)
	if err != nil || !value.Paused || value.Actor != adminID || value.UpdatedAt == "" {
		t.Fatalf("pause not persisted with operator: %+v %v", value, err)
	}
	session := "paused-current-process-execution-session"
	if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		heartbeatAgentUpdateVersion(t, store, node, "0.1.0-alpha.123", true)
		if task, err := store.ClaimNextTask(ctx, node.ID, node.Credential); !errors.Is(err, errExecutionBlocked) || task != nil {
			t.Fatalf("pause allowed claim: %+v %v", task, err)
		}
		if _, err := store.PersistExecutionAuthorization(ctx, node.ID, session, AgentTask{ID: queued.ID, Kind: "agent.update", Attempt: 1}); !errors.Is(err, errExecutionBlocked) {
			t.Fatalf("pause allowed authorization: %v", err)
		}
	}
	var state string
	var attempt int
	if err := store.db.QueryRow(`SELECT state,attempt FROM agent_updates WHERE id=?`, queued.ID).Scan(&state, &attempt); err != nil || state != "pending" || attempt != 0 {
		t.Fatalf("pause consumed task: %s %d %v", state, attempt, err)
	}
	for _, raw := range []string{`broken`, `{}`, `{"paused":null}`, `{"paused":0}`, `{"paused":"false"}`} {
		if _, err := store.db.Exec(`UPDATE settings SET value=? WHERE key=?`, raw, executionClaimControlKey); err != nil {
			t.Fatal(err)
		}
		if paused, err := executionClaimsPaused(ctx, store.db); err == nil && !paused {
			t.Fatalf("damaged control enabled claims: %s", raw)
		}
		if _, err := store.ExecutionClaimControl(ctx); err == nil {
			t.Fatalf("damaged control displayed as valid: %s", raw)
		}
	}
	if err := store.SetExecutionClaimControl(ctx, adminID, false); err != nil {
		t.Fatal(err)
	}
	task, err := store.ClaimNextTask(ctx, node.ID, node.Credential)
	if err != nil || task == nil || task.ID != queued.ID || task.Attempt != 1 {
		t.Fatalf("explicit resume lost pending intent: %+v %v", task, err)
	}
}
