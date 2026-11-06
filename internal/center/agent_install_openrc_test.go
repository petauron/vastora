package center

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAlpineInstallerKeepsPinnedTailscaleFloorAndValidShell(t *testing.T) {
	script := renderAgentInstallScript(AgentEnrollmentInstallProfile{HeadscaleCommand: "tailscale up --login-server=https://headscale.example.test", HeadscaleURL: "https://headscale.example.test"}, "https://center.example.test")
	for _, want := range []string{"alpine:3.24", "install_alpine_tailscale", "1.102.3:amd64", "sha256sum -c", "rc-update add tailscale default"} {
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(script, "apk add --no-cache tailscale") || strings.Contains(script, "gcompat") {
		t.Fatal("used unsupported Alpine package or compatibility layer")
	}
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("invalid generated shell: %s", output)
	}
}
