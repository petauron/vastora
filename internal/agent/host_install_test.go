package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadHostInstallStatePreservesUnknownLegacyTailscale(t *testing.T) {
	directory := t.TempDir()
	state, err := ReadHostInstallState(directory)
	if err != nil {
		t.Fatal(err)
	}
	if state.TailscaleOwnership != "external" || state.TailscaleEnrolled {
		t.Fatalf("legacy install was treated as Vastora-owned: %#v", state)
	}
}

func TestReadHostInstallStateTracksManagedTailscaleWithoutSecrets(t *testing.T) {
	directory := t.TempDir()
	content := "HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=managed\nTAILSCALE_ENROLLED=1\n"
	if err := os.WriteFile(filepath.Join(directory, HostInstallStateName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := ReadHostInstallState(directory)
	if err != nil {
		t.Fatal(err)
	}
	if state.TailscaleOwnership != "managed" || !state.TailscaleEnrolled {
		t.Fatalf("managed Tailscale provenance was lost: %#v", state)
	}
}
