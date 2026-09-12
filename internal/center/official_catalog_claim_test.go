package center

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestOfficialCatalogRejectedUpgradeRestoresConfirmedState(t *testing.T) {
	for _, status := range []string{"running", "failed", "stopped"} {
		for _, change := range []string{"expired", "removed", "replaced"} {
			t.Run(status+"/"+change, func(t *testing.T) {
				store, node := newOfficialClaimStore(t)
				applicationID := installOfficialClaimCPA(t, store, node, status)
				before := readOfficialClaimApplication(t, store, applicationID)
				queued := queueOfficialClaimUpgrade(t, store, node)
				assertOfficialClaimApplication(t, store, applicationID, "pending", before)
				if snapshot := readOfficialClaimDeployment(t, store, queued.ID); snapshot.PreDispatchStatus != status {
					t.Fatalf("queued upgrade lost confirmed status: %#v", snapshot)
				}

				changeOfficialClaimCatalog(t, store, change)
				for claim := 0; claim < 3; claim++ {
					task, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential, queued.ID)
					if err != nil || task != nil {
						t.Fatalf("claim %d dispatched unauthorized upgrade: task=%#v err=%v", claim, task, err)
					}
					assertOfficialClaimApplication(t, store, applicationID, status, before)
					assertOfficialClaimFailedEvent(t, store, queued.ID, 1)
				}
				failed := readOfficialClaimDeployment(t, store, queued.ID)
				if failed.State != "failed" || failed.Attempt != 0 || failed.Lease != "" || failed.Error != "Refresh the app catalog and retry this operation." {
					t.Fatalf("unauthorized upgrade was not terminated before issue: %#v", failed)
				}
				var eventMessage string
				if err := store.db.QueryRow(`SELECT message FROM task_events WHERE task_id = ? AND event = 'failed'`, queued.ID).Scan(&eventMessage); err != nil || eventMessage != failed.Error {
					t.Fatalf("terminal event lost refresh guidance: message=%q err=%v", eventMessage, err)
				}
			})
		}
	}
}

func TestOfficialCatalogUpgradeOnlyDeploysAfterClaim(t *testing.T) {
	store, node := newOfficialClaimStore(t)
	applicationID := installOfficialClaimCPA(t, store, node, "running")
	before := readOfficialClaimApplication(t, store, applicationID)
	queued := queueOfficialClaimUpgrade(t, store, node)
	assertOfficialClaimApplication(t, store, applicationID, "pending", before)
	if state := readOfficialClaimDeployment(t, store, queued.ID); state.State != "pending" || state.Attempt != 0 || state.Lease != "" {
		t.Fatalf("queued upgrade was already issued: %#v", state)
	}
	task, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential, queued.ID)
	if err != nil || task == nil || task.ID != queued.ID || task.Attempt != 1 || task.Kind != "application.apply" {
		t.Fatalf("valid upgrade was not claimed: task=%#v err=%v", task, err)
	}
	assertOfficialClaimApplication(t, store, applicationID, "deploying", before)
	if state := readOfficialClaimDeployment(t, store, queued.ID); state.State != "running" || state.Attempt != 1 || state.Lease == "" {
		t.Fatalf("claimed upgrade has no running lease: %#v", state)
	}
	assertOfficialClaimFailedEvent(t, store, queued.ID, 0)
}

func TestOfficialCatalogRejectedUpgradePreservesNewerReport(t *testing.T) {
	store, node := newOfficialClaimStore(t)
	applicationID := installOfficialClaimCPA(t, store, node, "running")
	before := readOfficialClaimApplication(t, store, applicationID)
	queued := queueOfficialClaimUpgrade(t, store, node)
	// Model an Agent report arriving after the pending lock was established.
	if _, err := store.db.Exec(`UPDATE applications SET status = 'failed' WHERE id = ?`, applicationID); err != nil {
		t.Fatal(err)
	}
	changeOfficialClaimCatalog(t, store, "expired")
	task, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential, queued.ID)
	if err != nil || task != nil {
		t.Fatalf("expired upgrade was dispatched: task=%#v err=%v", task, err)
	}
	assertOfficialClaimApplication(t, store, applicationID, "failed", before)
	assertOfficialClaimFailedEvent(t, store, queued.ID, 1)
}

func TestOfficialCatalogRejectionAndFailedEventAreAtomic(t *testing.T) {
	for _, operation := range []string{"install", "upgrade"} {
		t.Run(operation, func(t *testing.T) {
			store, node := newOfficialClaimStore(t)
			if operation == "upgrade" {
				installOfficialClaimCPA(t, store, node, "running")
			}
			queued, err := store.CreateDeployment(context.Background(), DeploymentRequest{
				AgentID: node.ID, AppKey: cpaAppKey, Operation: operation, Config: json.RawMessage(`{"debug":false}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			beforeApplication := readOfficialClaimApplication(t, store, queued.ApplicationID)
			beforeDeployment := readOfficialClaimDeployment(t, store, queued.ID)
			changeOfficialClaimCatalog(t, store, "expired")
			if _, err := store.db.Exec(`CREATE TRIGGER reject_official_claim_failed_event BEFORE INSERT ON task_events
				WHEN NEW.event = 'failed' BEGIN SELECT RAISE(ABORT, 'forced failed event'); END`); err != nil {
				t.Fatal(err)
			}
			task, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential, queued.ID)
			if task != nil || err == nil || !strings.Contains(err.Error(), "forced failed event") {
				t.Fatalf("event insertion failure did not abort rejection: task=%#v err=%v", task, err)
			}
			if after := readOfficialClaimDeployment(t, store, queued.ID); after != beforeDeployment {
				t.Fatalf("failed event did not roll back deployment: before=%#v after=%#v", beforeDeployment, after)
			}
			assertOfficialClaimApplication(t, store, queued.ApplicationID, "pending", beforeApplication)
			assertOfficialClaimFailedEvent(t, store, queued.ID, 0)

			if _, err := store.db.Exec(`DROP TRIGGER reject_official_claim_failed_event`); err != nil {
				t.Fatal(err)
			}
			task, err = store.ClaimNextTask(context.Background(), node.ID, node.Credential, queued.ID)
			if err != nil || task != nil {
				t.Fatalf("rejection did not recover after removing trigger: task=%#v err=%v", task, err)
			}
			status := "failed"
			if operation == "upgrade" {
				status = "running"
			}
			assertOfficialClaimApplication(t, store, queued.ApplicationID, status, beforeApplication)
			assertOfficialClaimFailedEvent(t, store, queued.ID, 1)
			if after := readOfficialClaimDeployment(t, store, queued.ID); after.State != "failed" || after.Attempt != 0 {
				t.Fatalf("recovered rejection has unexpected task state: %#v", after)
			}
		})
	}
}

func TestOfficialCatalogPreDispatchStateRejectsInvalidSnapshot(t *testing.T) {
	store, node := newOfficialClaimStore(t)
	installOfficialClaimCPA(t, store, node, "running")
	queued := queueOfficialClaimUpgrade(t, store, node)
	for _, invalid := range []any{"", "pending", "deploying", "unknown", nil} {
		if _, err := store.db.Exec(`UPDATE deployments SET pre_dispatch_application_status = ? WHERE id = ?`, invalid, queued.ID); err == nil {
			t.Fatalf("invalid pre-dispatch state was stored: %#v", invalid)
		}
	}
	if after := readOfficialClaimDeployment(t, store, queued.ID); after.PreDispatchStatus != "running" {
		t.Fatalf("invalid update changed the confirmed snapshot: %#v", after)
	}
}

func TestOfficialCatalogChangeDoesNotBlockIssuedRecovery(t *testing.T) {
	for _, recovery := range []string{"released", "reconciliation"} {
		for _, change := range []string{"expired", "removed", "replaced"} {
			t.Run(recovery+"/"+change, func(t *testing.T) {
				store, node := newOfficialClaimStore(t)
				installOfficialClaimCPA(t, store, node, "running")
				queued := queueOfficialClaimUpgrade(t, store, node)
				first := claimTask(t, store, node)
				if first.ID != queued.ID {
					t.Fatalf("wrong initial task claimed: %s", first.ID)
				}
				if recovery == "released" {
					if err := store.releaseClaimedTask(context.Background(), node.ID, *first); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := store.completeTaskWithDisposition(context.Background(), node.ID, node.Credential,
						first.ID, first.Attempt, false, "operation requires reconciliation", nil, true, first.RequiredRuntimeGeneration); err != nil {
						t.Fatal(err)
					}
				}
				changeOfficialClaimCatalog(t, store, change)
				if recovery == "reconciliation" {
					retry, err := store.RetryTaskReconciliation(context.Background(), first.ID)
					if err != nil || !retry.Queued || retry.TaskID != first.ID {
						t.Fatalf("issued reconciliation was blocked by catalog: retry=%#v err=%v", retry, err)
					}
				}
				failedEvents := 0
				if recovery == "reconciliation" {
					failedEvents = 1
				}
				assertOfficialClaimFailedEvent(t, store, queued.ID, failedEvents)
				replayed, err := store.ClaimNextTask(context.Background(), node.ID, node.Credential, first.ID)
				if err != nil || replayed == nil || replayed.ID != first.ID || replayed.Attempt != first.Attempt+1 || replayed.Reconcile != (recovery == "reconciliation") {
					t.Fatalf("issued task could not recover: first=%#v replayed=%#v err=%v", first, replayed, err)
				}
				if !reflect.DeepEqual(replayed.Manifest, first.Manifest) || !reflect.DeepEqual(replayed.Config, first.Config) {
					t.Fatal("issued recovery did not retain its authorized manifest and configuration")
				}
				assertOfficialClaimFailedEvent(t, store, queued.ID, failedEvents)
			})
		}
	}
}

func newOfficialClaimStore(t *testing.T) (*Store, AgentCredential) {
	t.Helper()
	store := openOrchestrationStore(t)
	t.Cleanup(func() { store.Close() })
	node := enrollOrchestrationNode(t, store, "official-claim", NodeCapabilities{Docker: true},
		[]networking.Candidate{{Address: "10.0.0.31", Interface: "eth0", Kind: networking.KindLAN}},
		networking.Profile{ServiceAddress: "10.0.0.31", LANAddress: "10.0.0.31", EnabledKinds: []string{networking.KindLAN}})
	return store, node
}

func installOfficialClaimCPA(t *testing.T, store *Store, node AgentCredential, status string) string {
	t.Helper()
	installed, err := store.CreateDeployment(context.Background(), DeploymentRequest{
		AgentID: node.ID, AppKey: cpaAppKey, Config: json.RawMessage(`{"debug":false}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	completeNextTask(t, store, node, "application.apply", cpaApplicationResult("10.0.0.31"))
	// Keep the real successful installation, but model a version older than
	// the current catalog so CreateDeployment exercises the upgrade path.
	if _, err := store.db.Exec(`UPDATE deployments SET app_version = '0.0.1' WHERE id = ? AND state = 'succeeded'`, installed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE applications SET status = ? WHERE id = ?`, status, installed.ApplicationID); err != nil {
		t.Fatal(err)
	}
	serviceStatus, lastError := "ready", ""
	if status == "failed" {
		serviceStatus, lastError = "degraded", "previous application failure"
	} else if status == "stopped" {
		serviceStatus = "stopped"
	}
	if _, err := store.db.Exec(`UPDATE services SET status = ?, last_error = ? WHERE application_id = ?`, serviceStatus, lastError, installed.ApplicationID); err != nil {
		t.Fatal(err)
	}
	installedState := readOfficialClaimApplication(t, store, installed.ApplicationID)
	if installedState.Version != "0.0.1" || len(installedState.Services) != 2 {
		t.Fatalf("CPA fixture was not installed successfully: %#v", installedState)
	}
	return installed.ApplicationID
}

func queueOfficialClaimUpgrade(t *testing.T, store *Store, node AgentCredential) DeploymentView {
	t.Helper()
	queued, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: node.ID, AppKey: cpaAppKey, Operation: "upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	return queued
}

func changeOfficialClaimCatalog(t *testing.T, store *Store, change string) {
	t.Helper()
	if change == "expired" {
		if _, err := store.db.Exec(`UPDATE official_catalog_trust SET expires_at = '1970-01-01T00:00:00Z' WHERE channel = 'stable'`); err != nil {
			t.Fatal(err)
		}
		return
	}
	value, _, err := readAcceptedOfficialCatalog(context.Background(), store.db, "stable")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range value.Apps {
		if value.Apps[i].ID != "cpa" {
			continue
		}
		found = true
		switch change {
		case "removed":
			value.Apps = append(value.Apps[:i], value.Apps[i+1:]...)
		case "replaced":
			value.Apps[i].Version = "99.0.0"
		default:
			t.Fatalf("unknown catalog change %q", change)
		}
		break
	}
	if !found {
		t.Fatal("CPA manifest not found in official catalog fixture")
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture writes a new accepted target and matching digest/revision,
	// so rejection tests exercise manifest authorization, not cache corruption.
	if err := store.SeedOfficialCatalog(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
}

type officialClaimServiceState struct {
	ID, Name, Protocol, Endpoint, Status, LastError, UpdatedAt string
	ContainerPort, HostPort                                    int
}

type officialClaimApplicationState struct {
	Status, Version string
	Services        []officialClaimServiceState
}

func readOfficialClaimApplication(t *testing.T, store *Store, applicationID string) officialClaimApplicationState {
	t.Helper()
	var value officialClaimApplicationState
	if err := store.db.QueryRow(`SELECT status, COALESCE((SELECT app_version FROM deployments
		WHERE application_id = applications.id AND state = 'succeeded'
		ORDER BY created_at DESC, rowid DESC LIMIT 1), '') FROM applications WHERE id = ?`, applicationID).Scan(&value.Status, &value.Version); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query(`SELECT id, name, protocol, container_port, host_port, endpoint, status, last_error, updated_at
		FROM services WHERE application_id = ? ORDER BY id`, applicationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var service officialClaimServiceState
		if err := rows.Scan(&service.ID, &service.Name, &service.Protocol, &service.ContainerPort, &service.HostPort,
			&service.Endpoint, &service.Status, &service.LastError, &service.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		value.Services = append(value.Services, service)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertOfficialClaimApplication(t *testing.T, store *Store, applicationID, status string, before officialClaimApplicationState) {
	t.Helper()
	before.Status = status
	if after := readOfficialClaimApplication(t, store, applicationID); !reflect.DeepEqual(after, before) {
		t.Fatalf("application state/version/services changed unexpectedly: want=%#v got=%#v", before, after)
	}
}

type officialClaimDeploymentState struct {
	State, Lease, Error, PreDispatchStatus, UpdatedAt string
	Attempt                                           int64
}

func readOfficialClaimDeployment(t *testing.T, store *Store, deploymentID string) officialClaimDeploymentState {
	t.Helper()
	var value officialClaimDeploymentState
	if err := store.db.QueryRow(`SELECT state, attempt, lease_expires_at, error, pre_dispatch_application_status, updated_at
		FROM deployments WHERE id = ?`, deploymentID).Scan(&value.State, &value.Attempt, &value.Lease, &value.Error, &value.PreDispatchStatus, &value.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertOfficialClaimFailedEvent(t *testing.T, store *Store, taskID string, expected int) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM task_events WHERE task_id = ? AND kind = 'application.apply' AND event = 'failed'`, taskID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("failed event count = %d, want %d", count, expected)
	}
}
