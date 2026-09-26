package center

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestOpenAPICatalogRuntimeAdmissionContracts(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	for _, tc := range []struct {
		path, payload string
		valid         bool
	}{
		{"/api/v1/deployments", `{"agentId":"node","appKey":"community/app","config":{},"packageRevision":1,"manifestSha256":"` + digest + `","authorizedCapabilities":["root"]}`, true},
		{"/api/v1/deployments", `{"agentId":"node","appKey":"community/app","config":{}}`, false},
		{"/api/v1/deployments", `{"agentId":"node","appKey":"community/app","config":{},"operation":"upgrade","packageRevision":0,"manifestSha256":"` + digest + `"}`, false},
		{"/api/v1/deployments", `{"agentId":"node","appKey":"community/app","config":{},"packageRevision":1,"manifestSha256":"invalid"}`, false},
		{"/api/v1/deployments", `{"agentId":"node","appKey":"community/app","config":{},"operation":"configure"}`, true},
		{"/api/v1/deployments", `{"agentId":"node","appKey":"community/app","config":{},"operation":"uninstall","deleteData":false}`, true},
		{"/api/v1/applications/{id}/adopt", `{"backupsConfirmed":true}`, true},
		{"/api/v1/applications/{id}/adopt", `{"backupsConfirmed":false}`, false},
		{"/api/v1/applications/{id}/adopt", `{}`, false},
		{"/api/v1/applications/{id}/maintenance", `{"action":"logs"}`, true},
		{"/api/v1/applications/{id}/maintenance", `{"action":"backup"}`, true},
		{"/api/v1/applications/{id}/maintenance", `{"action":"restore","backupId":"recorded-backup-1"}`, true},
		{"/api/v1/applications/{id}/maintenance", `{"action":"restore"}`, false},
		{"/api/v1/applications/{id}/maintenance", `{"action":"restore","backupId":"../other"}`, false},
		{"/api/v1/applications/{id}/maintenance", `{"action":"logs","backupId":"other"}`, false},
		{"/api/v1/applications/{id}/maintenance", `{"action":"shell"}`, false},
	} {
		op := doc.Paths.Value(tc.path).Post
		var payload any
		if err := json.Unmarshal([]byte(tc.payload), &payload); err != nil {
			t.Fatal(err)
		}
		err := op.RequestBody.Value.Content["application/json"].Schema.Value.VisitJSON(payload)
		if (err == nil) != tc.valid {
			t.Errorf("%s %s valid=%v: %v", tc.path, tc.payload, tc.valid, err)
		}
	}
}

func TestOpenAPICatalogTypesRemainDefinedAfterModuleExtraction(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := doc.Components.Schemas["CatalogAppManifest"]
	if manifest == nil || manifest.Value == nil || manifest.Value.Properties["packageRevision"] == nil || manifest.Value.Properties["runtime"] == nil {
		t.Fatal("external catalog module manifest disappeared from the public contract")
	}
	runtime := doc.Components.Schemas["CatalogRuntimeSpec"]
	if runtime == nil || runtime.Value.Properties["requiredCapabilities"] == nil || runtime.Value.Properties["docker"] == nil || runtime.Value.Properties["systemd"] == nil {
		t.Fatal("runtime capability or executor schemas are missing")
	}
	value := doc.Components.Schemas["CatalogValue"]
	if value == nil || value.Value.Properties["array"].Value.Items.Ref != "#/components/schemas/CatalogValue" {
		t.Fatal("recursive structured values must reference the shared component")
	}
	apps := doc.Paths.Value("/api/v1/catalog/apps").Get.Responses.Value("200").Value.Content["application/json"].Schema.Value.Properties["apps"].Value.Items.Value
	if apps.Properties["manifestSha256"] == nil || apps.Properties["managedConfigFields"] == nil {
		t.Fatal("catalog approval identity or managed configuration contract is missing")
	}
	backups := doc.Paths.Value("/api/v1/applications/{id}/backups").Get.Responses.Value("200").Value.Content["application/json"].Schema.Value.Items.Value
	if backups.Properties["restorable"] == nil || backups.Properties["path"] != nil || backups.Properties["sha256"] != nil {
		t.Fatal("backup selection must expose availability without host paths or digests")
	}
}
