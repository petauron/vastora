package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/networking"
)

func independentTestPackage(t *testing.T) catalog.AppManifest {
	t.Helper()
	var app catalog.AppManifest
	raw := `{"id":"never-coded-app","version":"1.0.0","packageRevision":1,"name":{"en":"Independent app","zh-CN":"独立应用"},"description":{"en":"Test recipe","zh-CN":"测试配方"},"license":"MIT","images":[{"name":"app","reference":"registry.example.test/demo@sha256:` + strings.Repeat("a", 64) + `"}],"config":[],"runtime":{"kind":"docker","version":1,"docker":{"containers":[{"name":"app","image":"app","user":"65532:65532"}]}}}`
	if err := json.Unmarshal([]byte(raw), &app); err != nil {
		t.Fatal(err)
	}
	if err := catalog.ValidateApp(app); err != nil {
		t.Fatal(err)
	}
	return app
}

func seedIndependentPackage(t *testing.T, store *Store, app catalog.AppManifest) {
	t.Helper()
	raw, err := json.Marshal(catalog.Catalog{SchemaVersion: 4, GeneratedAt: store.now().UTC(), Apps: []catalog.AppManifest{app}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SeedOfficialCatalog(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
}

func independentRuntimeStore(t *testing.T) (*Store, AgentCredential, catalog.AppManifest) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	app := independentTestPackage(t)
	seedIndependentPackage(t, store, app)
	node := enrollOrchestrationNode(t, store, "generic-test", NodeCapabilities{Docker: true, ExecutorVersions: map[string]int{"docker": 1}, RuntimeCapabilities: []string{"root", "host-network", "host-path", "devices"}}, []networking.Candidate{{Address: "10.0.0.20", Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: "10.0.0.20", LANAddress: "10.0.0.20", EnabledKinds: []string{networking.KindLAN}})
	return store, node, app
}

func TestCatalogRuntimeNativeBundleRequiresCapableNode(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	app.Images = nil
	app.Artifacts = []catalog.Artifact{{Name: "bundle", OperatingSystem: "linux", Architecture: "amd64", URL: "https://example.com/bundle", SHA256: strings.Repeat("a", 64), Format: "raw"}}
	app.Runtime = &catalog.RuntimeSpec{Kind: "systemd", Version: 2, RequiredCapabilities: []string{"native-services"}, NativeServices: &catalog.NativeServicesRuntime{Users: []string{"worker"}, Services: []catalog.NativeService{{Name: "worker", Start: "automatic", ManagedUser: "worker", Process: catalog.SystemdRuntime{Artifact: "bundle", Executable: "worker", Files: []catalog.ArtifactFile{{Path: "worker", Executable: true}}}}}}}
	if err := requireCatalogRuntime(app); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int{1, 2} {
		raw, err := json.Marshal(NodeCapabilities{ExecutorVersions: map[string]int{"systemd": version}, RuntimeCapabilities: []string{"native-services"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(context.Background(), `UPDATE agents SET capabilities_json=? WHERE id=?`, raw, node.ID); err != nil {
			t.Fatal(err)
		}
		err = requireNodeRuntime(context.Background(), store.db, node.ID, app)
		if version == 1 && err == nil {
			t.Fatal("old Agent admitted v2 bundle")
		}
		if version == 2 && err != nil {
			t.Fatal("capable Agent rejected", err)
		}
	}
}

func confirmIndependentTask(t *testing.T, store *Store, node AgentCredential, task *AgentTask) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"resources": map[string]any{"version": 1, "applicationId": task.ApplicationID, "appKey": task.AppKey, "taskId": task.ID, "runtime": "docker", "packageVersion": task.Manifest.Version, "packageRevision": task.PackageRevision, "manifestSha256": task.ManifestSHA256, "authorizedCapabilities": task.AuthorizedCapabilities, "state": "ready", "resources": []any{map[string]string{"kind": "container", "logicalName": "app", "name": "test-instance", "id": "test-container-id"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteTask(context.Background(), node.ID, node.Credential, task.ID, task.Attempt, true, "", raw, task.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRuntimeIndependentAdmissionAndRecipeUpgrade(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	ctx := context.Background()
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: OfficialCatalogSourceID + "/" + app.ID, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	if task.PackageRevision != 1 || task.ManifestSHA256 == "" || deployment.PackageRevision != 1 {
		t.Fatalf("missing package identity: %+v", task)
	}
	confirmIndependentTask(t, store, node, task)
	app.PackageRevision = 2
	seedIndependentPackage(t, store, app)
	apps, err := store.ListApplications(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || !apps[0].UpdateAvailable || apps[0].InstalledPackageRevision != 1 || apps[0].AvailablePackageRevision != 2 {
		t.Fatalf("same-version recipe update not visible: %+v", apps)
	}
	if _, err = store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: task.AppKey, Operation: "upgrade", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRuntimePermissionExpansionRequiresExplicitApproval(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	ctx := context.Background()
	_, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: OfficialCatalogSourceID + "/" + app.ID, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	confirmIndependentTask(t, store, node, task)
	app.PackageRevision = 2
	app.Runtime.RequiredCapabilities = []string{"root"}
	app.Runtime.Docker.Containers[0].User = "0:0"
	seedIndependentPackage(t, store, app)
	request := DeploymentRequest{AgentID: node.ID, AppKey: task.AppKey, Operation: "upgrade", Config: json.RawMessage(`{}`)}
	if _, err = store.CreateDeployment(ctx, request); err == nil || !strings.Contains(err.Error(), "approval required") {
		t.Fatalf("unapproved permission expansion accepted: %v", err)
	}
	grants := []string{"root"}
	request.AuthorizedCapabilities = &grants
	if _, err = store.CreateDeployment(ctx, request); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRuntimeUnknownExecutorDoesNotRejectCatalog(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	_ = node
	app.Runtime = &catalog.RuntimeSpec{Kind: "future", Version: 8, Unsupported: json.RawMessage(`{"kind":"future","version":8,"settings":{"future":true}}`)}
	app.PackageRevision = 2
	seedIndependentPackage(t, store, app)
	apps, err := store.ListApps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || !apps[0].InstallBlocked || !strings.Contains(apps[0].InstallBlockedReason, "future") {
		t.Fatalf("unknown runtime not shown independently: %+v", apps)
	}
}

func TestCatalogRuntimeReceiptRejectsChangedPackage(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	ctx := context.Background()
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: OfficialCatalogSourceID + "/" + app.ID, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	raw := json.RawMessage(`{"version":1,"applicationId":"wrong","appKey":"wrong","taskId":"wrong","state":"ready"}`)
	if err = storePackageReceipt(ctx, tx, deployment.ID, deployment.ApplicationID, deployment.AppKey, "install", raw, time.Now()); err == nil {
		t.Fatal("accepted unrelated receipt")
	}
}

func TestCatalogRuntimeRejectsCatalogChangedAfterReview(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	_, _, digest, err := canonicalPackage(app)
	if err != nil {
		t.Fatal(err)
	}
	selectedRevision := app.PackageRevision
	app.PackageRevision++
	seedIndependentPackage(t, store, app)
	if _, err = store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: node.ID, AppKey: OfficialCatalogSourceID + "/" + app.ID, Config: json.RawMessage(`{}`), PackageRevision: &selectedRevision, ManifestSHA256: digest}); err == nil || !strings.Contains(err.Error(), "selected package changed") {
		t.Fatalf("stale reviewed recipe accepted: %v", err)
	}
}

func TestCatalogRuntimeReceiptRequiresMatchingRuntimeAndResources(t *testing.T) {
	for _, scenario := range []string{"missing-runtime", "wrong-runtime", "missing-resources"} {
		t.Run(scenario, func(t *testing.T) {
			store, node, app := independentRuntimeStore(t)
			ctx := context.Background()
			deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: OfficialCatalogSourceID + "/" + app.ID, Config: json.RawMessage(`{}`)})
			if err != nil {
				t.Fatal(err)
			}
			task := claimTask(t, store, node)
			var result ApplicationTaskResult
			if err := json.Unmarshal(mockPackageResult(t, task, nil), &result); err != nil {
				t.Fatal(err)
			}
			var receipt map[string]any
			if err := json.Unmarshal(result.Resources, &receipt); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing-runtime":
				delete(receipt, "runtime")
			case "wrong-runtime":
				receipt["runtime"] = "systemd"
			case "missing-resources":
				delete(receipt, "resources")
			}
			raw, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := storePackageReceipt(ctx, tx, deployment.ID, deployment.ApplicationID, deployment.AppKey, "install", raw, store.now()); err == nil {
				t.Fatal("accepted missing or mismatched ownership evidence")
			}
		})
	}
}

func TestCatalogRuntimeAdoptionOnlyChangesManagementRecords(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	ctx := context.Background()
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: OfficialCatalogSourceID + "/" + app.ID, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	confirmIndependentTask(t, store, node, task)
	raw, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var historical map[string]json.RawMessage
	if err = json.Unmarshal(raw, &historical); err != nil {
		t.Fatal(err)
	}
	delete(historical, "runtime")
	delete(historical, "packageRevision")
	raw, err = json.Marshal(historical)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE deployments SET manifest_json=?,package_revision=0,manifest_sha256='' WHERE id=?`, raw, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE application_resources SET adoption_state='pending',package_revision=0,resources_json='{}' WHERE application_id=?`, deployment.ApplicationID); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = store.db.QueryRow(`SELECT status || ':' || updated_at || ':' || image FROM applications WHERE id=?`, deployment.ApplicationID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	id, err := store.QueueApplicationAdoption(ctx, deployment.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	adoption := claimTask(t, store, node)
	if adoption.Kind != "application.adopt" || adoption.ID != id || string(adoption.HistoricalManifest) != string(raw) || adoption.PackageRevision != 0 {
		t.Fatalf("adoption reinterpreted old installation: %+v", adoption)
	}
	confirmIndependentTask(t, store, node, adoption)
	if err = store.db.QueryRow(`SELECT status || ':' || updated_at || ':' || image FROM applications WHERE id=?`, deployment.ApplicationID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("adoption mutated application runtime state: %q -> %q", before, after)
	}
	var count int
	var state string
	if err = store.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE application_id=?`, deployment.ApplicationID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT adoption_state FROM application_resources WHERE application_id=?`, deployment.ApplicationID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if count != 1 || state != "ready" {
		t.Fatalf("adoption created a deployment or did not finish: %d %s", count, state)
	}
	if _, err = store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: task.AppKey, Operation: "configure", Config: json.RawMessage(`{"setting":true}`)}); err == nil {
		t.Fatal("historical audit recipe became executable")
	}
	removal, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: task.AppKey, Operation: "uninstall"})
	if err != nil {
		t.Fatal(err)
	}
	removeTask := claimTask(t, store, node)
	if removeTask.ID != removal.ID || removeTask.Operation != "uninstall" || removeTask.PackageRevision != 0 || string(removeTask.HistoricalManifest) != string(raw) || removeTask.ManifestSHA256 != adoption.ManifestSHA256 || removeTask.DeleteData {
		t.Fatalf("legacy uninstall did not preserve original evidence and data: %+v", removeTask)
	}
}

func TestManagedUpgradeDormantMeridianRequiresAdoptionAndOnlyStagesImage(t *testing.T) {
	store, node, _ := independentRuntimeStore(t)
	ctx := context.Background()
	app := independentTestPackage(t)
	app.ID = "meridian"
	seedIndependentPackage(t, store, app)
	deployment, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: meridianAppKey, Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	install := claimTask(t, store, node)
	confirmIndependentTask(t, store, node, install)
	raw, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var historical map[string]json.RawMessage
	if err := json.Unmarshal(raw, &historical); err != nil {
		t.Fatal(err)
	}
	delete(historical, "runtime")
	delete(historical, "packageRevision")
	raw, err = json.Marshal(historical)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE deployments SET manifest_json=?,package_revision=0,manifest_sha256='' WHERE id=?`, raw, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE application_resources SET adoption_state='pending',package_revision=0,resources_json='{}' WHERE application_id=?`, deployment.ApplicationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.QueueApplicationAdoption(ctx, deployment.ApplicationID); err != nil {
		t.Fatal(err)
	}
	adoption := claimTask(t, store, node)
	if !adoption.DormantRuntime {
		t.Fatal("Center did not recognize Glivela without endpoints")
	}
	result, err := json.Marshal(map[string]any{"resources": map[string]any{"version": 1, "applicationId": adoption.ApplicationID, "appKey": adoption.AppKey, "taskId": adoption.ID, "runtime": "docker", "integrationState": "dormant", "packageVersion": adoption.Manifest.Version, "packageRevision": 0, "manifestSha256": adoption.ManifestSHA256, "state": "ready", "resources": []any{}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteTask(ctx, node.ID, node.Credential, adoption.ID, adoption.Attempt, true, "", result, adoption.RequiredRuntimeGeneration); err != nil {
		t.Fatal(err)
	}
	var state, receipt string
	if err := store.db.QueryRow(`SELECT adoption_state,resources_json FROM application_resources WHERE application_id=?`, deployment.ApplicationID).Scan(&state, &receipt); err != nil {
		t.Fatal(err)
	}
	if state != "ready" || !strings.Contains(receipt, `"integrationState":"dormant"`) {
		t.Fatalf("dormant adoption not recorded: %s %s", state, receipt)
	}
	app.Version = "1.1.0"
	app.Images[0].Name = "xray-core"
	app.Runtime.Docker.Containers[0].Image = "xray-core"
	seedIndependentPackage(t, store, app)
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: meridianAppKey, Operation: "upgrade", Config: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	upgrade := claimTask(t, store, node)
	for _, scenario := range []string{"empty", "activated", "wrong-image", "upgrade"} {
		integration := "awaiting-configuration"
		image := map[string]string{"kind": "image", "logicalName": "xray-core", "name": app.Images[0].Reference, "sha256": strings.Repeat("a", 64)}
		resources := []any{image}
		switch scenario {
		case "empty":
			resources = nil
		case "activated":
			integration = "active"
			image["kind"] = "container"
		case "wrong-image":
			image["name"] = "registry.example.test/other@sha256:" + strings.Repeat("b", 64)
		}
		result, err := json.Marshal(map[string]any{"resources": map[string]any{"version": 1, "applicationId": upgrade.ApplicationID, "appKey": upgrade.AppKey, "taskId": upgrade.ID, "runtime": "docker", "integrationState": integration, "packageVersion": upgrade.Manifest.Version, "packageRevision": upgrade.PackageRevision, "manifestSha256": upgrade.ManifestSHA256, "authorizedCapabilities": upgrade.AuthorizedCapabilities, "state": "ready", "resources": resources}})
		if err != nil {
			t.Fatal(err)
		}
		err = store.CompleteTask(ctx, node.ID, node.Credential, upgrade.ID, upgrade.Attempt, true, "", result, upgrade.RequiredRuntimeGeneration)
		if (err == nil) != (scenario == "upgrade") {
			t.Fatalf("scenario=%s error=%v", scenario, err)
		}
	}
	if err := store.db.QueryRow(`SELECT resources_json FROM application_resources WHERE application_id=?`, deployment.ApplicationID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(receipt, `"integrationState":"awaiting-configuration"`) || !strings.Contains(receipt, `"kind":"image"`) {
		t.Fatal("staged package identity was not retained")
	}
}

func TestCatalogRuntimeRenamedInstanceRetainsDeploymentHistory(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	ctx := context.Background()
	app.Config = []catalog.ConfigField{
		{Key: "setting", Type: "string", Label: catalog.LocalizedText{English: "Setting", SimplifiedChinese: "设置"}, Description: catalog.LocalizedText{English: "Setting", SimplifiedChinese: "设置"}},
		{Key: "token", Type: "string", Secret: true, Label: catalog.LocalizedText{English: "Token", SimplifiedChinese: "令牌"}, Description: catalog.LocalizedText{English: "Token", SimplifiedChinese: "令牌"}},
	}
	app.PackageRevision = 2
	seedIndependentPackage(t, store, app)
	oldKey := OfficialCatalogSourceID + "/" + app.ID
	_, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: oldKey, Config: json.RawMessage(`{"setting":"retained","token":"synthetic-secret"}`)})
	if err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, node)
	confirmIndependentTask(t, store, node, task)
	var originalManifest []byte
	if err = store.db.QueryRowContext(ctx, `SELECT manifest_json FROM deployments WHERE id=?`, task.ID).Scan(&originalManifest); err != nil {
		t.Fatal(err)
	}

	app.ID = "renamed-app"
	app.PackageRevision = 3
	seedIndependentPackage(t, store, app)
	newKey := OfficialCatalogSourceID + "/" + app.ID
	if _, err = store.db.ExecContext(ctx, `UPDATE applications SET app_key=? WHERE id=?`, newKey, task.ApplicationID); err != nil {
		t.Fatal(err)
	}
	active, err := store.activeDeployment(ctx, node.ID, newKey)
	if err != nil || !active.Installed || active.ID != task.ID || active.PackageRevision != 2 {
		t.Fatalf("renamed instance lost its installed deployment: %+v, %v", active, err)
	}
	apps, err := store.ListApplications(ctx)
	if err != nil || len(apps) != 1 || apps[0].ID != task.ApplicationID || !apps[0].UpdateAvailable || apps[0].InstalledPackageRevision != 2 || apps[0].AvailablePackageRevision != 3 {
		t.Fatalf("renamed instance lost upgrade state: %+v, %v", apps, err)
	}
	if _, err = store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: newKey, Operation: "configure", Config: json.RawMessage(`{"unused":true}`)}); err == nil || !strings.Contains(err.Error(), "upgrade the renamed application") {
		t.Fatalf("old manifest was accepted for a renamed application: %v", err)
	}
	upgrade, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: newKey, Operation: "upgrade", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var savedConfig []byte
	var secretID string
	if err = store.db.QueryRowContext(ctx, `SELECT config_json,secret_id FROM deployments WHERE id=?`, upgrade.ID).Scan(&savedConfig, &secretID); err != nil {
		t.Fatal(err)
	}
	savedSecret, err := store.getSecret(ctx, secretID, "deployment:"+upgrade.ID)
	if err != nil || string(savedConfig) != `{"setting":"retained"}` || string(savedSecret) != `{"token":"synthetic-secret"}` {
		t.Fatal("renamed instance lost its prior settings or encrypted secret")
	}
	var historicalKey string
	var historicalManifest []byte
	if err = store.db.QueryRowContext(ctx, `SELECT app_key,manifest_json FROM deployments WHERE id=?`, task.ID).Scan(&historicalKey, &historicalManifest); err != nil {
		t.Fatal(err)
	}
	if historicalKey != oldKey || string(historicalManifest) != string(originalManifest) {
		t.Fatal("renaming rewrote historical deployment evidence")
	}
}
