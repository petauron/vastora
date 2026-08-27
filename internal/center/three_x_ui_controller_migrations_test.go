package center

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestThreeXUIControllerMigrationBacksUpRestoresAndSwitchesRoles(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	master := enrollOrchestrationNode(t, store, "old-controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.10", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.10", HeadscaleAddress: "100.64.0.10", EnabledKinds: []string{networking.KindHeadscale}})
	worker := enrollOrchestrationNode(t, store, "new-controller", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "100.64.0.20", Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: "100.64.0.20", HeadscaleAddress: "100.64.0.20", EnabledKinds: []string{networking.KindHeadscale}})
	config := json.RawMessage(`{"timezone":"UTC","panel_port":2053,"enable_fail2ban":true,"vmess_aead_forced":false}`)

	masterDeployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: master.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleMaster, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	masterTask := claimTask(t, store, master)
	completeThreeXUIDeployment(t, store, master, masterTask, "100.64.0.10", "master-token")
	workerDeployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: worker.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleWorker, Config: config})
	if err != nil {
		t.Fatal(err)
	}
	workerTask := claimTask(t, store, worker)
	completeThreeXUIDeployment(t, store, worker, workerTask, "100.64.0.20", "worker-token")
	nodeTask := claimTask(t, store, master)
	nodeResult, _ := json.Marshal(ApplicationTaskResult{NodeCommand: &ThreeXUINodeCommandResult{RemoteNodeID: 7, Status: "ready"}})
	if err := store.CompleteTask(ctx, master.ID, master.Credential, nodeTask.ID, nodeTask.Attempt, true, "", nodeResult); err != nil {
		t.Fatal(err)
	}
	remainingWorkerApplications := []string{}
	for index := range 2 {
		address := fmt.Sprintf("100.64.0.%d", 30+index)
		other := enrollOrchestrationNode(t, store, fmt.Sprintf("remaining-worker-%d", index+1), NodeCapabilities{Docker: true}, []networking.Candidate{{Address: address, Interface: "tailscale0", Kind: networking.KindHeadscale}}, networking.Profile{ServiceAddress: address, HeadscaleAddress: address, EnabledKinds: []string{networking.KindHeadscale}})
		deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: other.ID, AppKey: threeXUIAppKey, Role: threeXUIRoleWorker, Config: config})
		if err != nil {
			t.Fatal(err)
		}
		otherTask := claimTask(t, store, other)
		completeThreeXUIDeployment(t, store, other, otherTask, address, fmt.Sprintf("worker-%d-token", index+1))
		reconcile := claimTask(t, store, master)
		result, _ := json.Marshal(ApplicationTaskResult{NodeCommand: &ThreeXUINodeCommandResult{RemoteNodeID: 8 + index, Status: "ready"}})
		if err := store.CompleteTask(ctx, master.ID, master.Credential, reconcile.ID, reconcile.Attempt, true, "", result); err != nil {
			t.Fatal(err)
		}
		remainingWorkerApplications = append(remainingWorkerApplications, deployment.ApplicationID)
	}

	migration, err := store.CreateThreeXUIControllerMigration(ctx, masterDeployment.ApplicationID, ThreeXUIControllerMigrationInput{TargetApplicationID: workerDeployment.ApplicationID, Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	backupTask := claimTask(t, store, master)
	if backupTask.ControllerCommand == nil || backupTask.ControllerCommand.Action != "backup" || backupTask.ControllerCommand.SourceAPIToken != "master-token" {
		t.Fatalf("backup task = %#v", backupTask)
	}
	database := append([]byte("SQLite format 3\x00"), []byte("migration-test")...)
	backup, err := store.StoreThreeXUIBackup(ctx, master.ID, master.Credential, masterDeployment.ApplicationID, backupTask.ControllerCommand.BackupRevision, bytes.NewReader(database))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(database)
	if backup.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("backup digest = %q", backup.SHA256)
	}
	backupResult, _ := json.Marshal(ApplicationTaskResult{ControllerCommand: &ThreeXUIControllerCommandResult{Action: "backup", BackupRevision: backup.Revision, BackupSHA256: backup.SHA256, BackupSize: backup.Size}})
	if err := store.CompleteTask(ctx, master.ID, master.Credential, backupTask.ID, backupTask.Attempt, true, "", backupResult); err != nil {
		t.Fatal(err)
	}

	promoteTask := claimTask(t, store, worker)
	if promoteTask.ControllerCommand == nil || promoteTask.ControllerCommand.Action != "promote" || promoteTask.ControllerCommand.SourceRemoteNodeID != 7 || promoteTask.ControllerCommand.SourceAPIToken != "master-token" {
		t.Fatalf("promote task = %#v", promoteTask)
	}
	promoteResult, _ := json.Marshal(ApplicationTaskResult{ControllerCommand: &ThreeXUIControllerCommandResult{Action: "promote", BackupRevision: backup.Revision, SourceRemoteNodeID: 7}})
	if err := store.CompleteTask(ctx, worker.ID, worker.Credential, promoteTask.ID, promoteTask.Attempt, true, "", promoteResult); err != nil {
		t.Fatal(err)
	}
	completed, err := store.ThreeXUIControllerMigration(ctx, migration.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "switching" || completed.Step != "cleanup" {
		t.Fatalf("migration = %#v", completed)
	}
	applications, err := store.ListApplications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, application := range applications {
		roles[application.ID] = application.Role
	}
	if roles[masterDeployment.ApplicationID] != threeXUIRoleWorker || roles[workerDeployment.ApplicationID] != threeXUIRoleMaster {
		t.Fatalf("roles after migration = %#v", roles)
	}
	demoteTask := claimTask(t, store, master)
	if demoteTask.ControllerCommand == nil || demoteTask.ControllerCommand.Action != "demote" {
		t.Fatalf("demote task = %#v", demoteTask)
	}
	demoteResult, _ := json.Marshal(ApplicationTaskResult{ControllerCommand: &ThreeXUIControllerCommandResult{Action: "demote"}})
	if err := store.CompleteTask(ctx, master.ID, master.Credential, demoteTask.ID, demoteTask.Attempt, true, "", demoteResult); err != nil {
		t.Fatal(err)
	}
	wantReconciled := map[string]bool{masterDeployment.ApplicationID: true}
	for _, applicationID := range remainingWorkerApplications {
		wantReconciled[applicationID] = true
	}
	for range len(wantReconciled) {
		reconnectTask := claimTask(t, store, worker)
		if reconnectTask.NodeCommand == nil || reconnectTask.NodeCommand.Action != "reconcile" || !wantReconciled[reconnectTask.NodeCommand.WorkerApplicationID] {
			t.Fatalf("serialized migration reconnect task = %#v", reconnectTask)
		}
		remoteNodeID := reconnectTask.NodeCommand.RemoteNodeID
		if remoteNodeID < 1 {
			remoteNodeID = 20
		}
		reconnectResult, _ := json.Marshal(ApplicationTaskResult{NodeCommand: &ThreeXUINodeCommandResult{RemoteNodeID: remoteNodeID, Status: "ready"}})
		if err := store.CompleteTask(ctx, worker.ID, worker.Credential, reconnectTask.ID, reconnectTask.Attempt, true, "", reconnectResult); err != nil {
			t.Fatal(err)
		}
		delete(wantReconciled, reconnectTask.NodeCommand.WorkerApplicationID)
	}
	completed, err = store.ThreeXUIControllerMigration(ctx, migration.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "ready" || completed.Step != "complete" {
		t.Fatalf("completed migration = %#v", completed)
	}
}
