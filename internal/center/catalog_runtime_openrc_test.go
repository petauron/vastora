package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/catalog/catalog"
)

func TestCatalogRuntimePortableNativeReceiptCompletesInstallAndUpgrade(t *testing.T) {
	store, node, app := independentRuntimeStore(t)
	ctx := context.Background()
	app.Images = nil
	app.Artifacts = []catalog.Artifact{{Name: "bundle", OperatingSystem: "linux", Architecture: "amd64", URL: "https://example.com/bundle", SHA256: strings.Repeat("a", 64), Format: "raw"}}
	app.Runtime = &catalog.RuntimeSpec{Kind: "native", Version: 2, RequiredCapabilities: []string{"native-services"}, NativeServices: &catalog.NativeServicesRuntime{Users: []string{"worker"}, Services: []catalog.NativeService{{Name: "worker", Start: "automatic", ManagedUser: "worker", Process: catalog.SystemdRuntime{Artifact: "bundle", Executable: "worker", Files: []catalog.ArtifactFile{{Path: "worker", Executable: true}}}}}}}
	caps, _ := json.Marshal(NodeCapabilities{ExecutorVersions: map[string]int{"native": 2}, RuntimeCapabilities: []string{"native-services"}})
	if _, err := store.db.ExecContext(ctx, `UPDATE agents SET capabilities_json=? WHERE id=?`, caps, node.ID); err != nil {
		t.Fatal(err)
	}
	grants := []string{"native-services"}
	for index, operation := range []string{"install", "upgrade"} {
		app.PackageRevision = index + 2
		seedIndependentPackage(t, store, app)
		if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: node.ID, AppKey: OfficialCatalogSourceID + "/" + app.ID, Operation: operation, Config: json.RawMessage(`{}`), AuthorizedCapabilities: &grants}); err != nil {
			t.Fatal(err)
		}
		task := claimTask(t, store, node)
		result, _ := json.Marshal(map[string]any{"resources": map[string]any{"version": 1, "applicationId": task.ApplicationID, "appKey": task.AppKey, "taskId": task.ID, "runtime": "native", "runtimeVersion": 2, "packageVersion": app.Version, "packageRevision": app.PackageRevision, "manifestSha256": task.ManifestSHA256, "authorizedCapabilities": grants, "state": "ready", "resources": []any{map[string]string{"kind": "unit", "logicalName": "worker", "name": "owned-worker.service", "path": "/etc/init.d/owned-worker"}}}})
		if err := store.CompleteTask(ctx, node.ID, node.Credential, task.ID, task.Attempt, true, "", result, task.RequiredRuntimeGeneration); err != nil {
			t.Fatal(err)
		}
		installed, err := store.ListApplications(ctx)
		if err != nil || len(installed) != 1 || installed[0].InstalledPackageRevision != app.PackageRevision {
			t.Fatal("portable native receipt was not committed", err)
		}
	}
}
