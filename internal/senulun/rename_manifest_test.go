package senulun

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/petauron/catalog/catalog"
)

func TestIdentityRenameOnlyAcceptsEquivalentRecipes(t *testing.T) {
	raw, err := os.ReadFile("../center/testdata/reviewed-catalog-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture catalog.Catalog
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, original := range fixture.Apps {
		if original.ID != "pulse" && original.ID != "pulse-agent" {
			continue
		}
		t.Run(original.ID, func(t *testing.T) {
			// The fixture predates alpha.31. Model the already-installed Senlun
			// executable without changing the retained historical fixture file.
			if original.ID == "pulse-agent" {
				original.Runtime.Systemd.Executable = "senlun-agent"
				for i := range original.Runtime.Systemd.Files {
					if original.Runtime.Systemd.Files[i].Path == "pulse-agent" {
						original.Runtime.Systemd.Files[i].Path = "senlun-agent"
					}
				}
			}
			before, _ := json.Marshal(original)
			var renamed catalog.AppManifest
			if err := json.Unmarshal(before, &renamed); err != nil {
				t.Fatal(err)
			}
			renamed.PackageRevision++
			if original.ID == "pulse" {
				renamed.ID = "senlun"
				renamed.Images[0].Name = "senlun"
				renamed.Runtime.Docker.Containers[0].Name = "senlun"
				renamed.Runtime.Docker.Containers[0].Image = "senlun"
			} else {
				renamed.ID = "senlun-agent"
				for i := range renamed.Artifacts {
					renamed.Artifacts[i].Name = "senlun-agent"
				}
				renamed.Runtime.Systemd.Artifact = "senlun-agent"
			}
			if err := ValidateIdentityRename(original, renamed); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(original)
			if string(before) != string(after) {
				t.Fatal("comparison changed the original signed recipe")
			}
			if original.ID == "pulse" {
				renamed.Images[0].Reference = "ghcr.io/petauron/senlun@sha256:" + strings.Repeat("a", 64)
			} else {
				renamed.Artifacts[0].SHA256 = strings.Repeat("a", 64)
			}
			if err := ValidateIdentityRename(original, renamed); err == nil {
				t.Fatal("accepted a changed image or agent artifact")
			}
		})
	}
}
