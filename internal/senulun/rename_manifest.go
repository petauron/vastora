package senulun

import (
	"encoding/json"
	"errors"
	"reflect"

	"github.com/petauron/catalog/catalog"
)

// ValidateIdentityRename accepts only a recipe whose executable behavior is
// unchanged. The target must already come from a verified signed catalog; this
// comparison does not authenticate its source. It does not touch running data.
func ValidateIdentityRename(previous, target catalog.AppManifest) error {
	if err := catalog.ValidateApp(previous); err != nil {
		return errors.New("senlun: original recipe is invalid")
	}
	if err := catalog.ValidateApp(target); err != nil {
		return errors.New("senlun: target recipe is invalid")
	}
	if previous.Version != target.Version || target.PackageRevision <= previous.PackageRevision {
		return errors.New("senlun: identity migration requires the same version and a newer recipe revision")
	}

	// Deep copy before normalizing, including the nested runtime and slices.
	raw, err := json.Marshal(previous)
	if err != nil {
		return err
	}
	var normalized catalog.AppManifest
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return err
	}
	if len(normalized.Config) != len(target.Config) {
		return errors.New("senlun: identity migration cannot change configuration fields")
	}
	normalized.ID, normalized.PackageRevision = target.ID, target.PackageRevision
	normalized.Name, normalized.Description = target.Name, target.Description
	for i := range normalized.Config {
		normalized.Config[i].Label = target.Config[i].Label
		normalized.Config[i].Description = target.Config[i].Description
	}

	switch {
	case previous.ID == "pulse" && target.ID == "senlun":
		if normalized.Runtime == nil || normalized.Runtime.Kind != "docker" || normalized.Runtime.Docker == nil || len(normalized.Images) != 1 || len(normalized.Runtime.Docker.Containers) != 1 || normalized.Images[0].Name != "pulse" || normalized.Runtime.Docker.Containers[0].Name != "pulse" || normalized.Runtime.Docker.Containers[0].Image != "pulse" {
			return errors.New("senlun: original service recipe is not the expected identity")
		}
		normalized.Images[0].Name = "senlun"
		normalized.Runtime.Docker.Containers[0].Name = "senlun"
		normalized.Runtime.Docker.Containers[0].Image = "senlun"
	case previous.ID == "pulse-agent" && target.ID == "senlun-agent":
		if normalized.Runtime == nil || normalized.Runtime.Kind != "systemd" || normalized.Runtime.Systemd == nil || normalized.Runtime.Systemd.Artifact != "pulse-agent" || normalized.Runtime.Systemd.Executable != "senlun-agent" || len(normalized.Artifacts) == 0 {
			return errors.New("senlun: original agent recipe is not the expected identity")
		}
		for i := range normalized.Artifacts {
			if normalized.Artifacts[i].Name != "pulse-agent" {
				return errors.New("senlun: original agent artifact identity differs")
			}
			normalized.Artifacts[i].Name = "senlun-agent"
		}
		normalized.Runtime.Systemd.Artifact = "senlun-agent"
	default:
		return errors.New("senlun: unsupported identity migration")
	}

	normalized, err = catalog.CanonicalAppManifest(normalized)
	if err != nil {
		return errors.New("senlun: normalized target recipe is invalid")
	}
	canonicalTarget, err := catalog.CanonicalAppManifest(target)
	if err != nil || !reflect.DeepEqual(normalized, canonicalTarget) {
		return errors.New("senlun: identity migration would change executable behavior")
	}
	return nil
}
