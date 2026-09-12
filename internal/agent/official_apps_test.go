package agent

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

func officialContractFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	raw, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := catalog.ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func cloneOfficialManifest(t *testing.T, app catalog.AppManifest) catalog.AppManifest {
	t.Helper()
	raw, err := json.Marshal(app)
	if err != nil {
		t.Fatal(err)
	}
	var cloned catalog.AppManifest
	if err := json.Unmarshal(raw, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func TestOfficialContractsPermitVersionUpdatesButNotPermissionChanges(t *testing.T) {
	value := officialContractFixture(t)
	if err := ValidateOfficialCatalog(value); err != nil {
		t.Fatal(err)
	}
	for _, app := range value.Apps {
		app.Version = "99.0.0"
		if err := ValidateOfficialContract(app); err != nil {
			t.Fatalf("version-only update: %v", err)
		}
		app.HostAccess = !app.HostAccess
		if err := ValidateOfficialContract(app); err == nil {
			t.Fatalf("permission change accepted for %s", app.ID)
		}
	}
	value.Apps = append(value.Apps, value.Apps[0])
	if err := ValidateOfficialCatalog(value); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestOfficialContractsPermitIndependentReleaseMetadata(t *testing.T) {
	value := officialContractFixture(t)
	for _, original := range value.Apps {
		t.Run(original.ID, func(t *testing.T) {
			app := cloneOfficialManifest(t, original)
			app.Version = "99.1.0"
			app.Name = catalog.LocalizedText{English: "Updated name", SimplifiedChinese: "更新名称"}
			app.Description = catalog.LocalizedText{English: "Updated description", SimplifiedChinese: "更新说明"}
			app.License = "Updated license"
			for index := range app.Images {
				app.Images[index].Reference = "registry.example.invalid/new-release@sha256:" + strings.Repeat("b", 64)
			}
			for index := range app.Artifacts {
				app.Artifacts[index].URL = "https://downloads.example.invalid/new-release/" + app.Artifacts[index].Architecture
				app.Artifacts[index].SHA256 = strings.Repeat("b", 64)
			}
			for index := range app.Config {
				field := &app.Config[index]
				field.Label = catalog.LocalizedText{English: "New label", SimplifiedChinese: "新标签"}
				field.Description = catalog.LocalizedText{English: "New help", SimplifiedChinese: "新说明"}
				if field.Type == "integer" && field.Default != nil {
					raw := json.RawMessage(string(*field.Default) + ".0")
					field.Default = &raw
				}
			}
			slices.Reverse(app.Images)
			slices.Reverse(app.Artifacts)
			slices.Reverse(app.Services)
			slices.Reverse(app.Config)
			if err := ValidateOfficialContract(app); err != nil {
				t.Fatalf("independent app release requires an unnecessary Agent update: %v", err)
			}
		})
	}
}

func TestOfficialContractsRejectChangedExecutorSemantics(t *testing.T) {
	value := officialContractFixture(t)
	for _, original := range value.Apps {
		t.Run(original.ID, func(t *testing.T) {
			check := func(name string, change func(*catalog.AppManifest)) {
				t.Helper()
				t.Run(name, func(t *testing.T) {
					app := cloneOfficialManifest(t, original)
					change(&app)
					if err := catalog.ValidateApp(app); err != nil {
						t.Fatalf("fixture must be valid declarative metadata, not a syntax failure: %v", err)
					}
					if err := ValidateOfficialContract(app); err == nil {
						t.Fatal("changed executor semantics accepted without an Agent update")
					}
				})
			}
			check("new app", func(app *catalog.AppManifest) { app.ID = "unsupported-app" })
			check("host permission", func(app *catalog.AppManifest) { app.HostAccess = !app.HostAccess })
			check("new configuration", func(app *catalog.AppManifest) {
				app.Config = append(app.Config, catalog.ConfigField{Key: "new_option", Type: "boolean", Label: catalog.LocalizedText{English: "New", SimplifiedChinese: "新"}, Description: catalog.LocalizedText{English: "New", SimplifiedChinese: "新"}})
			})
			if len(original.Images) > 0 {
				check("renamed image role", func(app *catalog.AppManifest) { app.Images[0].Name = "other-image" })
				check("new image role", func(app *catalog.AppManifest) {
					image := app.Images[0]
					image.Name = "additional-image"
					app.Images = append(app.Images, image)
				})
			}
			if len(original.Artifacts) > 0 {
				check("renamed native role", func(app *catalog.AppManifest) { app.Artifacts[0].Name = "other-agent" })
				check("removed platform", func(app *catalog.AppManifest) { app.Artifacts = app.Artifacts[:1] })
			}
			if len(original.Services) > 0 {
				check("service port", func(app *catalog.AppManifest) { app.Services[0].ContainerPort++ })
				check("service transport", func(app *catalog.AppManifest) { app.Services[0].Protocol = "https" })
				check("service exposure", func(app *catalog.AppManifest) { app.Services[0].Management = !app.Services[0].Management })
				check("health semantics", func(app *catalog.AppManifest) { app.Services[0].HealthPath = "/new-health" })
			}
			if original.Homepage != nil {
				check("homepage", func(app *catalog.AppManifest) { app.Homepage.Path = "/new-management" })
			}
			if len(original.Config) > 0 {
				check("configuration requirement", func(app *catalog.AppManifest) { app.Config[0].Required = !app.Config[0].Required })
				check("configuration default", func(app *catalog.AppManifest) {
					field := &app.Config[0]
					// Keep secret fields valid while changing their delivery contract.
					if field.Secret {
						field.Secret = false
					}
					raw := json.RawMessage(`"changed"`)
					if field.Type == "boolean" {
						raw = json.RawMessage("true")
						if field.Default != nil && string(*field.Default) == "true" {
							raw = json.RawMessage("false")
						}
					} else if field.Type == "integer" {
						raw = json.RawMessage("12345")
					}
					field.Default = &raw
				})
			}
		})
	}
}

func TestOfficialCatalogRejectsInvalidContainer(t *testing.T) {
	value := officialContractFixture(t)
	value.SchemaVersion++
	if err := ValidateOfficialCatalog(value); err == nil {
		t.Fatal("unsupported catalog schema accepted")
	}
	value = officialContractFixture(t)
	value.GeneratedAt = time.Time{}
	if err := ValidateOfficialCatalog(value); err == nil {
		t.Fatal("catalog without creation time accepted")
	}
}
