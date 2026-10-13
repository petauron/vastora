package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompositeRejectsCustomScriptsBeforeDocker(t *testing.T) {
	for _, section := range []string{"routing", "dns"} {
		t.Run(section, func(t *testing.T) {
			filename := filepath.Join(t.TempDir(), "candidate.json")
			if err := os.WriteFile(filename, []byte(`{"`+section+`":{"script":"/etc/vastora/obsolete.lua"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			// No Docker client: obsolete candidates must be rejected before
			// any validator, container, running configuration or service changes.
			err := validateXrayWorkerConfigAs(context.Background(), nil, "official-fixture", filename, os.Geteuid())
			if err == nil || !strings.Contains(err.Error(), "only official script-free Xray configuration") {
				t.Fatal("obsolete runtime configuration reached Docker validation")
			}
		})
	}
}
