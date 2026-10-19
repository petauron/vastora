package center

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func reinstallListenerFixture(t *testing.T, publicExit ...string) (*Store, AgentCredential, AgentReinstallApplicationInput) {
	t.Helper()
	s, node, input := reinstallRuntimeNetworkFixture(t, true, publicExit...)
	ctx := context.Background()
	seedSuccessfulHistoricalRuntime(t, s, node, input)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT INTO publications(id,service_id,kind,ingress_owner,entry_node_id,hostname,sni_hostname,dns_provider,status,created_at,updated_at)
 VALUES('restored-entry','restore-service','public_shared_443','application_node',?,'entry.example.test','www.example.com','manual','degraded',?,?)`, node.ID, now, now); err != nil {
		t.Fatal(err)
	}
	// The replacement must not inherit an old applied revision or attempt.
	if _, err := s.db.Exec(`INSERT INTO node_listener_states(node_id,desired_revision,applied_revision,desired_json,status,attempt,updated_at) VALUES(?,4,4,'{}','ready',2,?)`, node.ID, now); err != nil {
		t.Fatal(err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	return s, node, input
}

// Read an archived intent without authorizing or delivering it.
func archivedListenerTask(t *testing.T, s *Store, node AgentCredential, taskID string) *AgentTask {
	t.Helper()
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	task, err := s.reinstallListenerTask(context.Background(), tx, node.ID, taskID, false)
	if err != nil || task == nil {
		t.Fatalf("archived listener: %v", err)
	}
	return task
}

func TestArchivedListenerIntentDoesNotRestoreRetiredEntry(t *testing.T) {
	s, node, input := reinstallListenerFixture(t)
	ctx := context.Background()
	receipt, err := s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || same != receipt {
		t.Fatalf("repeat: %+v %v", same, err)
	}
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || len(plan.UnclaimedLocalWork) != 0 {
		t.Fatalf("new listener was offered as old work: %+v %v", plan.UnclaimedLocalWork, err)
	}
	task := archivedListenerTask(t, s, node, receipt.TaskID)
	state := task.NodeListenerState
	if task.Revision != 5 || task.Attempt != 3 || state.Listener.Address != "10.0.0.8" || len(state.Listener.Routes) != 1 || state.Listener.Routes[0].ID != "restored-entry" || state.Listener.Routes[0].Hostname != "www.example.com" || state.Listener.Routes[0].Upstreams[0].Address != "10.0.0.8" || state.Listener.Routes[0].ProxyProtocol != "v2" {
		t.Fatalf("wrong restored listener: %+v", state)
	}
	if err := s.completeNodeListenerState(ctx, commitProjectionOnlyForTest, node.ID, task.Revision, task.Attempt, true, ""); err == nil {
		t.Fatal("retired listener result projected")
	}
	plan, err = s.AgentReinstallPlan(ctx, node.ID)
	if err != nil || plan.Applications[0].Preparation != nil || readHistoricalReinstallPreparation(t, s, node, input).Listener.State != "pending" || plan.Recovery.State != "review_required" {
		t.Fatalf("receipt: %+v %v", plan.Recovery, err)
	}
	var address, status string
	var profiles int
	if err = s.db.QueryRow(`SELECT endpoint FROM services WHERE id='restore-service'`).Scan(&address); err != nil || address != "10.0.0.7:10443" {
		t.Fatal("listener receipt changed service publication")
	}
	if err = s.db.QueryRow(`SELECT status FROM publications WHERE id='restored-entry'`).Scan(&status); err != nil || status != "degraded" {
		t.Fatal("listener receipt claimed public verification")
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM agent_network_profiles WHERE agent_id=?`, node.ID).Scan(&profiles); err != nil || profiles != 0 {
		t.Fatal("listener receipt activated profile")
	}
	if _, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); !errors.Is(err, errExecutionBlocked) {
		t.Fatalf("released fence: %v", err)
	}
	directory := s.dataDir
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	same, err = s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil || same.TaskID != receipt.TaskID || same.State != "pending" {
		t.Fatalf("restart: %+v %v", same, err)
	}
}

func TestAgentReinstallListenerRejectsChangedAuthority(t *testing.T) {
	for _, at := range []string{"claim", "result"} {
		for _, mode := range []string{"key", "runtime", "route", "desired", "hash", "receipt", "attempt", "pending-entry", "operation", "runtime-receipt"} {
			t.Run(at+"/"+mode, func(t *testing.T) {
				s, node, input := reinstallListenerFixture(t)
				ctx := context.Background()
				receipt, queueErr := s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input)
				if queueErr != nil {
					t.Fatal(queueErr)
				}
				var task *AgentTask
				var err error
				if at == "result" {
					tx, beginErr := s.db.BeginTx(ctx, nil)
					if beginErr != nil {
						t.Fatal(beginErr)
					}
					task, err = s.reinstallListenerTask(ctx, tx, node.ID, receipt.TaskID, false)
					_ = tx.Rollback()
					if err != nil || task == nil {
						t.Fatal(err)
					}
					if _, err = s.db.Exec(`UPDATE node_listener_states SET status='applying',attempt=? WHERE node_id=?`, task.Attempt, node.ID); err != nil {
						t.Fatal(err)
					}
					if _, err = s.db.Exec(`UPDATE agent_reinstall_app_preparations SET listener_state='running' WHERE listener_task_id=?`, receipt.TaskID); err != nil {
						t.Fatal(err)
					}
				}
				query := ""
				switch mode {
				case "operation":
					query = `UPDATE agent_reinstall_operations SET state='superseded'`
				case "runtime-receipt":
					query = `UPDATE application_commands SET state='failed' WHERE id LIKE 'reinstall-runtime-%'`
				case "pending-entry":
					query = `UPDATE publications SET action_required=1`
				case "key":
					query = `UPDATE agents SET x25519_public_key=x'11'`
				case "runtime":
					query = `UPDATE meridian_credentials SET enabled=0 WHERE kind='native'`
				case "route":
					query = `UPDATE publications SET sni_hostname='changed.example.com'`
				case "desired":
					query = `UPDATE node_listener_states SET desired_json='{}'`
				case "hash":
					query = `UPDATE agent_reinstall_app_preparations SET listener_task_sha256=''`
				case "receipt":
					query = `DELETE FROM agent_reinstall_app_preparations`
				case "attempt":
					query = `UPDATE node_listener_states SET attempt=attempt+2`
				}
				if _, err = s.db.Exec(query); err != nil {
					t.Fatal(err)
				}
				if at == "claim" {
					task, err = s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0)
					if task != nil || err == nil && mode != "operation" {
						t.Fatalf("changed authority claimed: %+v %v", task, err)
					}
				} else {
					if err := s.completeNodeListenerState(ctx, commitProjectionOnlyForTest, node.ID, task.Revision, task.Attempt, true, ""); err == nil {
						t.Fatal("changed authority projected")
					}
				}
			})
		}
	}
}

func TestAgentReinstallListenerRequiresCurrentReviewAndCompleteSharedBackend(t *testing.T) {
	for _, mode := range []string{"administrator", "review", "runtime", "other-route", "stopped", "gateway"} {
		t.Run(mode, func(t *testing.T) {
			s, node, input := reinstallListenerFixture(t)
			admin := "reinstall-review-admin"
			switch mode {
			case "administrator":
				admin = "another-admin"
			case "review":
				if _, err := s.db.Exec(`UPDATE publications SET hostname='different.example.test'`); err != nil {
					t.Fatal(err)
				}
			case "runtime":
				if _, err := s.db.Exec(`UPDATE application_commands SET state='failed' WHERE id LIKE 'reinstall-runtime-%'`); err != nil {
					t.Fatal(err)
				}
			case "other-route":
				if _, err := s.db.Exec(`UPDATE services SET app_protocol='http' WHERE id='restore-service'`); err != nil {
					t.Fatal(err)
				}
			case "stopped":
				if _, err := s.db.Exec(`UPDATE publications SET status='stopped'`); err != nil {
					t.Fatal(err)
				}
			case "gateway":
				if _, err := s.db.Exec(`INSERT INTO site_gateways(site_id,agent_id,created_at) VALUES(?,?,'now')`, testSiteID(t, s), node.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`INSERT INTO gateway_components(gateway_node_id,desired_status,generation,status,updated_at) VALUES(?,'running',1,'failed','now')`, node.ID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "other-route" || mode == "stopped" || mode == "gateway" {
				plan, err := s.AgentReinstallPlan(context.Background(), node.ID)
				if err != nil {
					t.Fatal(err)
				}
				input.PlanRevision = plan.Revision
			}
			if _, err := s.QueueAgentReinstallListener(context.Background(), node.ID, admin, input); err == nil {
				t.Fatal("unreviewed or incomplete listener queued")
			}
			var revision int
			if err := s.db.QueryRow(`SELECT desired_revision FROM node_listener_states WHERE node_id=?`, node.ID).Scan(&revision); err != nil || revision != 4 {
				t.Fatal("rejected queue overwrote listener")
			}
		})
	}
}

func TestArchivedListenerFailureAndLostResultNeverReplay(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired", true: "failed"}[failed], func(t *testing.T) {
			s, node, input := reinstallListenerFixture(t)
			defer s.Close()
			ctx := context.Background()
			receipt, err := s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input)
			if err != nil {
				t.Fatal(err)
			}
			task := archivedListenerTask(t, s, node, receipt.TaskID)
			state, expected := "running", "needs_review"
			if failed {
				state, expected = "failed", "failed"
			}
			// Anonymous pre-upgrade outcome, never a newly executed task.
			if _, err = s.db.Exec(`UPDATE agent_reinstall_app_preparations SET listener_state=? WHERE listener_task_id=?`, state, receipt.TaskID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`UPDATE node_listener_states SET status='applying',attempt=?,lease_expires_at='' WHERE node_id=?`, task.Attempt, node.ID); err != nil {
				t.Fatal(err)
			}
			plan, err := s.AgentReinstallPlan(ctx, node.ID)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Applications[0].Preparation != nil || readHistoricalReinstallPreparation(t, s, node, input).Listener.State != expected {
				t.Fatal("historical outcome lost or exposed as current recovery")
			}
			if next, err := s.claimExecutionTask(ctx, node.ID, node.Credential, "package-preparation-session", 0); next != nil || !errors.Is(err, errExecutionBlocked) {
				t.Fatalf("replayed: %v", err)
			}
			if err := s.completeNodeListenerState(ctx, commitProjectionOnlyForTest, node.ID, task.Revision, task.Attempt, true, ""); err == nil {
				t.Fatal("late obsolete listener result accepted")
			}
			if readHistoricalReinstallPreparation(t, s, node, input).Listener.State != expected {
				t.Fatal("late result changed history")
			}
		})
	}
}

func TestAgentReinstallListenerFinalSealingAndOldWorkSettlementCannotReplaceTask(t *testing.T) {
	s, node, input := reinstallListenerFixture(t)
	ctx := context.Background()
	receipt, err := s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input)
	if err != nil {
		t.Fatal(err)
	}
	archived := archivedListenerTask(t, s, node, receipt.TaskID)
	sealingTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	archived.NodeListenerState.Listener.Address = "127.0.0.1"
	_, err = s.persistExecutionAuthorization(ctx, sealingTx, node.ID, "package-preparation-session", *archived)
	_ = sealingTx.Rollback()
	if err == nil {
		t.Fatal("changed payload sealed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = s.cancelReinstallUnclaimedWork(ctx, tx, node.ID, AgentReinstallUnclaimedWork{TaskID: receipt.TaskID, Kind: "node.listener.apply", Revision: 5}, s.now().UTC().Format(time.RFC3339Nano)); err == nil {
		t.Fatal("new listener cancelled as old work")
	}
	var state string
	if err = tx.QueryRow(`SELECT listener_state FROM agent_reinstall_app_preparations`).Scan(&state); err != nil || state != "pending" {
		t.Fatal("sealing/cancellation failure changed receipt")
	}
}

func TestAgentReinstallListenerMissingPublicApprovalStopsBeforeQueue(t *testing.T) {
	s, node, input := reinstallRuntimeFixture(t)
	ctx := context.Background()
	seedSuccessfulHistoricalRuntime(t, s, node, input)
	plan, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.PlanRevision = plan.Revision
	if _, err = s.QueueAgentReinstallListener(ctx, node.ID, "reinstall-review-admin", input); err == nil || !strings.Contains(err.Error(), "public ingress") {
		t.Fatalf("missing ingress approval: %v", err)
	}
}
