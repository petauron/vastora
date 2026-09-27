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
