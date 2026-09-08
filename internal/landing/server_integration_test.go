//go:build linux && integration

package landing

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestNativePinnedXrayConfiguration(t *testing.T) {
	binary := os.Getenv("VASTORA_TEST_XRAY_BIN")
	if !filepath.IsAbs(binary) {
		t.Fatal("CI must supply the checksum-verified pinned Xray executable")
	}
	config, err := testServerPlan().RenderXray()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "xray.json")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "run", "-test", "-config", path)
	command.Dir = filepath.Dir(binary)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("pinned Xray rejected isolated fixture: %v\n%s", err, output)
	}
}
