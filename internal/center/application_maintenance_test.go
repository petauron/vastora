package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

func maintenanceTestInstance(t *testing.T) (*Store, AgentCredential, *AgentTask) {
	t.Helper()
	store, node, app := independentRuntimeStore(t)
	_, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: node.ID, AppKey: OfficialCatalogSourceID + "/" + app.ID, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	raw := maintenanceTestResult(t, task, "ready", nil, nil)
	if err := store.CompleteTask(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, true, "", raw, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	return store, node, task
}

func maintenanceTestResult(t *testing.T, task *AgentTask, state string, result *PackageMaintenanceResult, backups []maintenanceBackup) json.RawMessage {
	t.Helper()
	receipt := map[string]any{"version": 1, "applicationId": task.ApplicationID, "appKey": task.AppKey, "runtime": "docker", "packageVersion": task.Manifest.Version, "packageRevision": task.PackageRevision, "manifestSha256": task.ManifestSHA256, "authorizedCapabilities": task.AuthorizedCapabilities, "state": state, "taskId": task.ID, "resources": []any{map[string]any{"kind": "container", "logicalName": "app", "name": "test-instance", "id": "unchanged-container-id"}}, "backups": backups}
	raw, err := json.Marshal(map[string]any{"resources": receipt, "packageMaintenance": result})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func maintenanceTestBackup(task *AgentTask) maintenanceBackup {
	return maintenanceBackup{ID: task.ID, Resource: "test-volume", Target: "/data", Path: "/private/agent/backup.tar", SHA256: strings.Repeat("a", 64), PackageVersion: task.Manifest.Version, CreatedAt: time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC)}
}

func TestApplicationMaintenanceBackupRestoreAndImmutableIdentity(t *testing.T) {
	store, node, installed := maintenanceTestInstance(t)
	ctx := context.Background()
	id, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "backup"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "logs"}); err == nil {
		t.Fatal("concurrent maintenance accepted")
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: installed.AppKey, Operation: "configure", Config: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("deployment bypassed queued maintenance")
	}
	task := claimTask(t, store, node)
	if task.ID != id || task.Kind != "application.maintenance" || task.PackageMaintenance.Action != "backup" || task.ManifestSHA256 != installed.ManifestSHA256 || string(task.Resources) == "" {
		t.Fatalf("wrong task: %+v", task)
	}
	if _, err := store.RenewTaskLease(ctx, node.ID, node.Credential, id, task.Attempt); err != nil {
		t.Fatal(err)
	}
	backup := maintenanceTestBackup(task)
	raw := maintenanceTestResult(t, task, "ready", &PackageMaintenanceResult{BackupID: id}, []maintenanceBackup{backup})
	tampered := strings.Replace(string(raw), task.ManifestSHA256, strings.Repeat("f", 64), 1)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, id, task.Attempt, true, "", json.RawMessage(tampered), 0); err == nil {
		t.Fatal("accepted mismatched receipt")
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, id, task.Attempt, true, "", raw, 0); err != nil {
		t.Fatal(err)
	}
	view, err := store.ApplicationMaintenance(ctx, installed.ApplicationID, id)
	if err != nil || view.State != "succeeded" || view.Result.BackupID != id {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	backups, err := store.ApplicationBackups(ctx, installed.ApplicationID)
	if err != nil || len(backups) != 1 || !backups[0].Restorable {
		t.Fatalf("backups=%+v err=%v", backups, err)
	}
	encoded, _ := json.Marshal(backups)
	if strings.Contains(string(encoded), "/private") || strings.Contains(string(encoded), "/data") {
		t.Fatal("host paths exposed")
	}
	for _, bad := range []string{"/private/agent/backup.tar", "../other", "unrecorded"} {
		if _, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "restore", BackupID: bad}); err == nil {
			t.Fatalf("accepted backup %q", bad)
		}
	}
	restoreID, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "restore", BackupID: id})
	if err != nil {
		t.Fatal(err)
	}
	restore := claimTask(t, store, node)
	if restore.ID != restoreID || restore.PackageMaintenance.BackupID != id {
		t.Fatal("restore changed backup identity")
	}
	currentCopy := maintenanceTestBackup(restore)
	result := maintenanceTestResult(t, restore, "ready", &PackageMaintenanceResult{BackupID: restoreID}, []maintenanceBackup{backup, currentCopy})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, restoreID, restore.Attempt, true, "", result, 0); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationMaintenanceLogsRedactStoredSecrets(t *testing.T) {
	store, node, installed := maintenanceTestInstance(t)
	ctx := context.Background()
	const credential = "private-log-token-never-display"
	sealed, err := secret.Seal(store.key, []byte(`{"token":"`+credential+`"}`), []byte("deployment:"+installed.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`INSERT INTO secrets(id,sealed,created_at,updated_at) VALUES('maintenance-secret',?,?,?)`, sealed, store.now().UTC().Format(time.RFC3339Nano), store.now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE deployments SET secret_id='maintenance-secret' WHERE id=?`, installed.ID); err != nil {
		t.Fatal(err)
	}
	id, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if !strings.Contains(string(task.Secrets), credential) {
		t.Fatal("encrypted task missing redaction credentials")
	}
	raw := maintenanceTestResult(t, task, "ready", &PackageMaintenanceResult{Logs: []RuntimeLog{{Resource: "test-instance", Content: "token=" + credential}}}, nil)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, id, task.Attempt, true, "", raw, 0); err != nil {
		t.Fatal(err)
	}
	view, err := store.ApplicationMaintenance(ctx, installed.ApplicationID, id)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(view.Result.Logs[0].Content, credential) || !strings.Contains(view.Result.Logs[0].Content, "[REDACTED]") {
		t.Fatalf("unsafe log: %+v", view.Result)
	}
}

func TestApplicationMaintenanceFailureRetainsEvidenceAndBlocksMutation(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "reported", true: "expired"}[expired], func(t *testing.T) {
			store, node, installed := maintenanceTestInstance(t)
			ctx := context.Background()
			id, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "restore", BackupID: "missing"})
			if err == nil {
				t.Fatal("missing restore backup accepted")
			}
			id, err = store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "backup"})
			if err != nil {
				t.Fatal(err)
			}
			task := claimTask(t, store, node)
			if expired {
				if _, err := store.db.Exec(`UPDATE application_maintenance SET lease_expires_at=? WHERE id=?`, store.now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), id); err != nil {
					t.Fatal(err)
				}
				if err := store.recoverExpiredTasks(ctx, node.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := store.CompleteTask(ctx, node.ID, node.Credential, id, task.Attempt, false, "backup interrupted", nil, 0); err != nil {
					t.Fatal(err)
				}
			}
			view, err := store.ApplicationMaintenance(ctx, installed.ApplicationID, id)
			if err != nil || view.State != "failed" || !view.ReconciliationRequired {
				t.Fatalf("view=%+v err=%v", view, err)
			}
			var state string
			var receipt []byte
			if err := store.db.QueryRow(`SELECT adoption_state,resources_json FROM application_resources WHERE application_id=?`, installed.ApplicationID).Scan(&state, &receipt); err != nil {
				t.Fatal(err)
			}
			if state != "blocked" || !strings.Contains(string(receipt), "unchanged-container-id") {
				t.Fatal("lost original evidence or failed open")
			}
			if _, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "logs"}); err == nil {
				t.Fatal("failed package accepted new work")
			}
			if _, _, err := executionRequeueStatement(*task, node.ID, "now"); err == nil {
				t.Fatal("uncertain maintenance replay allowed")
			}
		})
	}
}

func TestApplicationMaintenanceRejectsUnresolvedExecutionStates(t *testing.T) {
	for _, state := range []string{"offered", "running", "helper_running", "failed", "unknown"} {
		t.Run(state, func(t *testing.T) {
			store, node, installed := maintenanceTestInstance(t)
			_, err := store.db.Exec(`INSERT INTO task_executions(id,agent_id,task_id,kind,attempt,session_id,digest,sealed_task,state,phase,expires_at,created_at,updated_at) VALUES('blocked-maintenance',?,'other-task','application.apply',1,'session','digest',X'00',?,'start','','now','now')`, node.ID, state)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.QueueApplicationMaintenance(context.Background(), installed.ApplicationID, PackageMaintenanceTask{Action: "logs"}); err == nil {
				t.Fatalf("ignored unresolved %s", state)
			}
		})
	}
}

func TestApplicationMaintenanceRoutesRequireAuthenticationAndCSRF(t *testing.T) {
	store, _, installed := maintenanceTestInstance(t)
	ctx := context.Background()
	session, csrf, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(store, "", false).Handler()
	endpoint := "/api/v1/applications/" + installed.ApplicationID + "/maintenance"
	for _, authorization := range []string{"none", "session", "csrf"} {
		request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(`{"action":"logs"}`))
		request.Header.Set("Content-Type", "application/json")
		if authorization != "none" {
			request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
		}
		if authorization == "csrf" {
			request.Header.Set("X-CSRF-Token", csrf)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusUnauthorized
		if authorization == "csrf" {
			want = http.StatusAccepted
		}
		if response.Code != want {
			t.Fatalf("%s: %d %s", authorization, response.Code, response.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodGet, endpoint+"/unknown", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatal("logs readable without auth")
	}
}

func TestApplicationMaintenanceExecutionProjectionAndRetainedConfirmation(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "authorized", true: "retained"}[retained], func(t *testing.T) {
			store, node, installed := maintenanceTestInstance(t)
			ctx := context.Background()
			id, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "logs"})
			if err != nil {
				t.Fatal(err)
			}
			const session = "package-maintenance-execution-session"
			if err := store.RegisterExecutionSession(ctx, node.ID, node.Credential, session, controlplane.ExecutionProtocol); err != nil {
				t.Fatal(err)
			}
			task, err := store.claimExecutionTask(ctx, node.ID, node.Credential, session, 0)
			if err != nil || task == nil || task.ID != id {
				t.Fatalf("claim=%+v err=%v", task, err)
			}
			if err := store.StartExecution(ctx, node.ID, session, task.Authorization.ID, task.Authorization.Digest); err != nil {
				t.Fatal(err)
			}
			raw := maintenanceTestResult(t, task, "ready", &PackageMaintenanceResult{Logs: []RuntimeLog{{Resource: "test-instance", Content: "healthy"}}}, nil)
			if err := store.StoreExecutionResult(ctx, node.ID, session, task.Authorization.ID, raw, true, false, "", nil); err != nil {
				t.Fatal(err)
			}
			if retained {
				if _, err := store.db.Exec(`UPDATE task_executions SET state='unknown' WHERE id=?`, task.Authorization.ID); err != nil {
					t.Fatal(err)
				}
				cookie, _, err := store.CreateFirstAdmin(ctx, "maintenance-admin", "strong-password-for-maintenance-test")
				if err != nil {
					t.Fatal(err)
				}
				adminID, err := store.SessionAdminID(ctx, cookie)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.ConfirmExecution(ctx, task.Authorization.ID, adminID, controlplane.ExecutionDisposition{Action: "confirm-completed", ExecutionStopped: true, Note: "Inspected task-bound retained receipt; previous process stopped."}); err != nil {
					t.Fatal(err)
				}
			} else {
				commit := func(tx *sql.Tx) error {
					if err := validateExecutionProjection(ctx, tx, task.Authorization.ID, true); err != nil {
						return err
					}
					if err := store.finalizeExecution(ctx, tx, node.ID, session, task.Authorization.ID); err != nil {
						return err
					}
					return tx.Commit()
				}
				if err := store.completeTaskWithDisposition(ctx, commit, node.ID, node.Credential, id, task.Attempt, true, "", raw, false); err != nil {
					t.Fatal(err)
				}
			}
			view, err := store.ApplicationMaintenance(ctx, installed.ApplicationID, id)
			if err != nil || view.State != "succeeded" {
				t.Fatalf("view=%+v err=%v", view, err)
			}
			if _, err := store.QueueApplicationMaintenance(ctx, installed.ApplicationID, PackageMaintenanceTask{Action: "logs"}); err != nil {
				t.Fatalf("completed evidence retained a stale fence: %v", err)
			}
		})
	}
}
