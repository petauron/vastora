package agent

import (
	"fmt"

	"github.com/petauron/vastora/internal/catalog"
)

var officialAppVersions = map[string]string{
	"3x-ui":        "3.7.0",
	"cpa":          "7.2.129",
	"keeper":       "1.14.1",
	"komari-agent": "1.2.60",
}

// OfficialAppVersion is the single version gate shared by official catalog
// seeding and typed Agent executors.
func OfficialAppVersion(id string) (string, bool) {
	version, ok := officialAppVersions[id]
	return version, ok
}

// ValidateOfficialCatalog keeps the signed built-in catalog aligned with the
// packages understood by this Agent build.
func ValidateOfficialCatalog(value catalog.Catalog) error {
	seen := make(map[string]bool, len(value.Apps))
	for _, app := range value.Apps {
		if seen[app.ID] {
			return fmt.Errorf("agent: official app %q is duplicated", app.ID)
		}
		expected, ok := OfficialAppVersion(app.ID)
		if !ok {
			return fmt.Errorf("agent: unsupported official app %q", app.ID)
		}
		if app.Version != expected {
			return fmt.Errorf("agent: official app %q requires version %s", app.ID, expected)
		}
		seen[app.ID] = true
	}
	for id := range officialAppVersions {
		if !seen[id] {
			return fmt.Errorf("agent: official app %q is missing", id)
		}
	}
	return nil
}
