//go:build linux && integration

package landing

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeDanteConfiguration(t *testing.T) {
	binary := os.Getenv("VASTORA_DANTE_TEST_BINARY")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("VASTORA_DANTE_TEST_BINARY must point to the release-built executable")
	}
	config, err := testServerPlan().RenderDante("127.0.0.2")
	// Render forbids loopback in production. Parser-only fixture uses a valid
	// plan, with a local egress substitution and an existing unprivileged UID.
	if err == nil {
		t.Fatal("production renderer accepted loopback")
	}
	config, err = testServerPlan().RenderDante("10.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	fixture := strings.ReplaceAll(string(config), "vastora-landing", "nobody")
	fixture = strings.ReplaceAll(fixture, "10.0.0.2", "127.0.0.1")
	path := filepath.Join(t.TempDir(), "danted.conf")
	if err := os.WriteFile(path, []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, binary, "-V", "-f", path).CombinedOutput(); err != nil {
		t.Fatalf("Dante rejected fixture: %v\n%s", err, output)
	}
}
