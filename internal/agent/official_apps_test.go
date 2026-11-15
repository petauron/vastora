package agent

import (
	"github.com/petauron/catalog/catalog"
	"os"
	"testing"
)

func officialContractFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	raw, err := os.ReadFile("../center/testdata/reviewed-catalog-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := catalog.ParseCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCatalogRecipesUseGenericRuntimeCapabilities(t *testing.T) {
	for _, app := range officialContractFixture(t).Apps {
		if err := catalog.ValidateApp(app); err != nil {
			t.Fatal(err)
		}
		if app.Runtime == nil {
			t.Fatalf("recipe %s lacks generic runtime", app.ID)
		}
	}
}

// Model the current identity in tests without rewriting the archived fixture.
func currentSenlunFixture(app catalog.AppManifest) catalog.AppManifest {
	switch app.ID {
	case "pulse":
		app.ID = "senlun"
		app.Images[0].Name = "senlun"
		app.Runtime.Docker.Containers[0].Name = "senlun"
		app.Runtime.Docker.Containers[0].Image = "senlun"
	case "pulse-agent":
		app.ID = "senlun-agent"
		for i := range app.Artifacts {
			app.Artifacts[i].Name = "senlun-agent"
		}
		app.Runtime.Systemd.Artifact = "senlun-agent"
	}
	return app
}
