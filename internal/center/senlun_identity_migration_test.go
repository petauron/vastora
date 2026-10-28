package center

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/networking"
)

func TestSenlunNativeIdentityMigrationRetainsInstanceAndHistory(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/reviewed-catalog-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture catalog.Catalog
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var previous catalog.AppManifest
	for _, app := range fixture.Apps {
		if app.ID == "pulse-agent" {
			previous = app
		}
	}
	if previous.Runtime == nil || previous.Runtime.Systemd == nil {
		t.Fatal("missing collector fixture")
	}
	previous.PackageRevision++
	previous.Runtime.Systemd.Executable = "senlun-agent"
	for i := range previous.Runtime.Systemd.Files {
		if previous.Runtime.Systemd.Files[i].Path == "pulse-agent" {
			previous.Runtime.Systemd.Files[i].Path = "senlun-agent"
		}
	}
	seedIndependentPackage(t, store, previous)
	node := enrollOrchestrationNode(t, store, "collector", NodeCapabilities{}, []networking.Candidate{{Address: "10.0.0.11", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.11", LANAddress: "10.0.0.11", EnabledKinds: []string{networking.KindLAN}})
	installed, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: legacySenlunNativeIdentityKey, Config: json.RawMessage(`{"service_url":"https://independent.example.test/","node_name":"same-node","node_group":"same-group","enrollment_token":"fixture-enrollment-token"}`)})
	if err != nil {
		t.Fatal(err)
	}
	originalTask := claimTask(t, store, node)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, originalTask.ID, originalTask.Attempt, true, "", mockPackageResult(t, originalTask, json.RawMessage(`{"services":[]}`)), originalTask.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var originalManifest, receipt []byte
	if err := store.db.QueryRowContext(ctx, `SELECT manifest_json FROM deployments WHERE id=?`, originalTask.ID).Scan(&originalManifest); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT resources_json FROM application_resources WHERE application_id=?`, installed.ApplicationID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(receipt)
	encoded, _ := json.Marshal(previous)
	var target catalog.AppManifest
	if err := json.Unmarshal(encoded, &target); err != nil {
		t.Fatal(err)
	}
	target.ID, target.PackageRevision = "senlun-agent", previous.PackageRevision+1
	target.Runtime.Systemd.Artifact = "senlun-agent"
	for i := range target.Artifacts {
		target.Artifacts[i].Name = "senlun-agent"
	}
	seedIndependentPackage(t, store, target)
	grants := []string{}
	request := DeploymentRequest{AgentID: node.ID, AppKey: senlunNativeIdentityKey, Operation: "install", ReplaceApplicationID: installed.ApplicationID, SourceReceiptSHA256: strings.Repeat("0", 64), AuthorizedCapabilities: &grants, Config: json.RawMessage(`{}`)}
	if _, err := store.CreateDeployment(ctx, request); err == nil {
		t.Fatal("accepted a changed source receipt")
	}
	request.SourceReceiptSHA256 = hex.EncodeToString(digest[:])
	migration, err := store.CreateDeployment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	if err := store.db.QueryRowContext(ctx, `SELECT app_key FROM applications WHERE id=?`, installed.ApplicationID).Scan(&key); err != nil || key != legacySenlunNativeIdentityKey {
		t.Fatal("Center changed identity before an Agent result")
	}
	task := claimTask(t, store, node)
	if task.ID != migration.ID || task.ApplicationID != installed.ApplicationID || task.SourceReceiptSHA256 != request.SourceReceiptSHA256 || strings.Contains(string(task.Config)+string(task.Secrets), "fixture-enrollment-token") || !strings.Contains(string(task.Config), "same-node") {
		t.Fatal("migration did not preserve the original instance configuration")
	}
	result, _ := json.Marshal(map[string]any{"sourceReceiptSha256": request.SourceReceiptSHA256, "services": []any{}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", mockPackageResult(t, task, result), task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT app_key FROM applications WHERE id=?`, installed.ApplicationID).Scan(&key); err != nil || key != senlunNativeIdentityKey {
		t.Fatal("confirmed migration did not retain the instance identity")
	}
	var after []byte
	if err := store.db.QueryRowContext(ctx, `SELECT manifest_json FROM deployments WHERE id=?`, originalTask.ID).Scan(&after); err != nil || string(after) != string(originalManifest) {
		t.Fatal("migration changed the original deployment history")
	}
}

func TestSenlunServiceIdentityMigrationPreservesServiceAndHistory(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/reviewed-catalog-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture catalog.Catalog
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	var previous catalog.AppManifest
	for _, app := range fixture.Apps {
		if app.ID == "pulse" {
			previous = app
		}
	}
	if previous.Runtime == nil || previous.Runtime.Docker == nil {
		t.Fatal("missing historical service fixture")
	}
	seedIndependentPackage(t, store, previous)
	node := enrollOrchestrationNode(t, store, "monitor", NodeCapabilities{Docker: true}, []networking.Candidate{{Address: "10.0.0.10", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.10", LANAddress: "10.0.0.10", EnabledKinds: []string{networking.KindLAN}})
	installed, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: legacySenlunServiceIdentityKey, Config: json.RawMessage(`{"public_url":"https://monitor.example.test"}`)})
	if err != nil {
		t.Fatal(err)
	}
	original := claimTask(t, store, node)
	services := json.RawMessage(`{"services":[{"name":"dashboard","protocol":"http","containerPort":8080,"hostPort":18080,"address":"10.0.0.10"}]}`)
	if err := store.CompleteTask(ctx, node.ID, node.Credential, original.ID, original.Attempt, true, "", mockPackageResult(t, original, services), original.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var originalManifest, receipt []byte
	if err := store.db.QueryRowContext(ctx, `SELECT manifest_json FROM deployments WHERE id=?`, original.ID).Scan(&originalManifest); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT resources_json FROM application_resources WHERE application_id=?`, installed.ApplicationID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(receipt)
	raw, _ = json.Marshal(previous)
	var target catalog.AppManifest
	if err := json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	target.ID, target.PackageRevision = "senlun", previous.PackageRevision+1
	target.Images[0].Name = "senlun"
	target.Runtime.Docker.Containers[0].Name = "senlun"
	target.Runtime.Docker.Containers[0].Image = "senlun"
	seedIndependentPackage(t, store, target)
	grants := []string{}
	request := DeploymentRequest{AgentID: node.ID, AppKey: senlunServiceIdentityKey, Operation: "install", ReplaceApplicationID: installed.ApplicationID, SourceReceiptSHA256: hex.EncodeToString(digest[:]), AuthorizedCapabilities: &grants, Config: json.RawMessage(`{}`)}
	migration, err := store.CreateDeployment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	if err := store.db.QueryRowContext(ctx, `SELECT app_key FROM applications WHERE id=?`, installed.ApplicationID).Scan(&key); err != nil || key != legacySenlunServiceIdentityKey {
		t.Fatal("Center changed service identity before the Agent confirmed it")
	}
	task := claimTask(t, store, node)
	if task.ID != migration.ID || task.ApplicationID != installed.ApplicationID || task.SourceReceiptSHA256 != request.SourceReceiptSHA256 || !strings.Contains(string(task.Config), "monitor.example.test") {
		t.Fatal("migration did not retain the original service and settings")
	}
	result, _ := json.Marshal(map[string]any{"sourceReceiptSha256": request.SourceReceiptSHA256, "sourceResources": json.RawMessage(receipt), "services": []any{map[string]any{"name": "dashboard", "protocol": "http", "containerPort": 8080, "hostPort": 18080, "address": "10.0.0.10"}}})
	if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", mockPackageResult(t, task, result), task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT app_key FROM applications WHERE id=?`, installed.ApplicationID).Scan(&key); err != nil || key != senlunServiceIdentityKey {
		t.Fatal("confirmed service migration did not retain its instance")
	}
	var after []byte
	if err := store.db.QueryRowContext(ctx, `SELECT manifest_json FROM deployments WHERE id=?`, original.ID).Scan(&after); err != nil || string(after) != string(originalManifest) {
		t.Fatal("migration changed the original deployment history")
	}
	var serviceCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE application_id=? AND name='dashboard'`, installed.ApplicationID).Scan(&serviceCount); err != nil || serviceCount != 1 {
		t.Fatal("migration did not preserve the original service")
	}
}
