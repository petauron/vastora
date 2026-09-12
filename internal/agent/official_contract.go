package agent

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/petauron/vastora/internal/catalog"
)

// This reviewed capability inventory is compiled into Agent, never downloaded.
// It contains installation semantics only, not distributable app versions.
//
//go:embed official_contracts.json
var officialContractsJSON []byte

type artifactContract struct {
	Name            string `json:"name"`
	OperatingSystem string `json:"operatingSystem"`
	Architecture    string `json:"architecture"`
}
type officialContract struct {
	ID         string                `json:"id"`
	HostAccess bool                  `json:"hostAccess"`
	Images     []string              `json:"images"`
	Artifacts  []artifactContract    `json:"artifacts"`
	Services   []catalog.Service     `json:"services"`
	Config     []catalog.ConfigField `json:"config"`
	Homepage   *catalog.Homepage     `json:"homepage"`
}

func ValidateOfficialContract(app catalog.AppManifest) error {
	canonical, err := catalog.CanonicalAppManifest(app)
	if err != nil {
		return err
	}
	app = canonical
	var contracts []officialContract
	if err := json.Unmarshal(officialContractsJSON, &contracts); err != nil {
		return fmt.Errorf("agent: invalid compiled executor contracts: %w", err)
	}
	actual := officialContract{ID: app.ID, HostAccess: app.HostAccess, Images: []string{}, Artifacts: []artifactContract{}, Services: append([]catalog.Service{}, app.Services...), Config: append([]catalog.ConfigField{}, app.Config...), Homepage: app.Homepage}
	for _, image := range app.Images {
		actual.Images = append(actual.Images, image.Name)
	}
	for _, artifact := range app.Artifacts {
		actual.Artifacts = append(actual.Artifacts, artifactContract{artifact.Name, artifact.OperatingSystem, artifact.Architecture})
	}
	for i := range actual.Config {
		actual.Config[i].Label = catalog.LocalizedText{}
		actual.Config[i].Description = catalog.LocalizedText{}
	}
	sort.Strings(actual.Images)
	sort.Slice(actual.Artifacts, func(i, j int) bool {
		a, b := actual.Artifacts[i], actual.Artifacts[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.OperatingSystem != b.OperatingSystem {
			return a.OperatingSystem < b.OperatingSystem
		}
		return a.Architecture < b.Architecture
	})
	sort.Slice(actual.Services, func(i, j int) bool { return actual.Services[i].Name < actual.Services[j].Name })
	sort.Slice(actual.Config, func(i, j int) bool { return actual.Config[i].Key < actual.Config[j].Key })
	encoded, err := json.Marshal(actual)
	if err != nil {
		return err
	}
	for _, contract := range contracts {
		if contract.ID != app.ID {
			continue
		}
		expected, err := json.Marshal(contract)
		if err != nil {
			return err
		}
		if bytes.Equal(encoded, expected) {
			return nil
		}
		return fmt.Errorf("agent: application %q requires an unsupported executor contract; upgrade Agent", app.ID)
	}
	return fmt.Errorf("agent: unsupported official app %q; upgrade Agent", app.ID)
}
