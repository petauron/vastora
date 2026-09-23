package center

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestMeridianExportDoesNotBlockPublicationVerification(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	agent := seedVerificationPublication(t, store, publicationCloudflare, "cloudflare", 1, 1, "degraded")
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,attempt,created_at,updated_at)
		VALUES('verification-export','verification-app',?,?,?,'{}','running',1,?,?)`, agent.ID, agent.ID, meridianruntime.LegacyExportKind, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureServicePublicationChangeAllowed(ctx, store.db, "verification-service"); err == nil {
		t.Fatal("export unexpectedly allowed an access change")
	}
	if err := store.ensureServicePublicationVerificationAllowed(ctx, store.db, "verification-service"); err != nil {
		t.Fatalf("read-only export blocked verification: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,attempt,created_at,updated_at)
		VALUES('verification-mutation','verification-app',?,?,'3xui.subscription.configure','{}','running',1,?,?)`, agent.ID, agent.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := store.ensureServicePublicationVerificationAllowed(ctx, store.db, "verification-service"); err == nil {
		t.Fatal("mutating command did not block verification")
	}
}

func TestMeridianImportFailureRetainsSuccessfulExportEvidence(t *testing.T) {
	store := openOrchestrationStore(t)
	t.Cleanup(func() { _ = store.Close() })
	agent := seedVerificationPublication(t, store, publicationCloudflare, "cloudflare", 1, 1, "ready")
	ctx := context.Background()
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO application_commands(id,application_id,agent_id,gateway_node_id,kind,input_json,state,attempt,error,created_at,updated_at)
		VALUES('failed-export','verification-app',?,?,?,'{}','failed',1,'center: Meridian import failed: subscription is not ready',?,?)`, agent.ID, agent.ID, meridianruntime.LegacyExportKind, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at)
		VALUES('failed-export-execution',?,'failed-export','application.command',1,'export-session','digest',X'00','unknown','result_received',?,?,?)`, agent.ID, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	check := func() error {
		tx, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		return validateExecutionProjection(ctx, tx, "failed-export-execution", true)
	}
	if err := check(); err != nil {
		t.Fatalf("Center-side import failure was treated as uncertain Agent execution: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE application_commands SET error='agent: export failed' WHERE id='failed-export'`); err != nil {
		t.Fatal(err)
	}
	if err := check(); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("unrelated failure was accepted: %v", err)
	}
}
