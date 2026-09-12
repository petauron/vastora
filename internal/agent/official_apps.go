package agent

import "github.com/petauron/vastora/internal/catalog"

// ValidateOfficialCatalog checks executor capabilities, not application versions.
func ValidateOfficialCatalog(value catalog.Catalog) error {
	if err := catalog.ValidateCatalog(value); err != nil {
		return err
	}
	for _, app := range value.Apps {
		if err := ValidateOfficialContract(app); err != nil {
			return err
		}
	}
	return nil
}
