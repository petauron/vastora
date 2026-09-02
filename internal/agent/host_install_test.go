package agent

import (
	"os"
	"path/filepath"
	"strings"
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

func TestReadHostInstallStateRejectsUnprotectedFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "dangling-symlink", "directory", "public-permissions"} {
		t.Run(kind, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, HostInstallStateName)
			target := filepath.Join(t.TempDir(), "external-state")
			content := []byte("HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=managed\nTAILSCALE_ENROLLED=1\n")
			if err := os.WriteFile(target, content, 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, path)
			case "dangling-symlink":
				err = os.Symlink(filepath.Join(directory, "missing"), path)
			case "directory":
				err = os.Mkdir(path, 0o700)
			case "public-permissions":
				err = os.WriteFile(path, content, 0o600)
				if err == nil {
					err = os.Chmod(path, 0o644)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReadHostInstallState(directory); err == nil {
				t.Fatal("unsafe ownership record was accepted")
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != string(content) {
				t.Fatalf("external ownership record was changed: %v", err)
			}
		})
	}
}

func TestReadHostInstallStateRejectsAmbiguousRecords(t *testing.T) {
	valid := "HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=managed\nTAILSCALE_ENROLLED=1\n"
	for name, content := range map[string]string{
		"duplicate-ownership": valid + "TAILSCALE_OWNERSHIP=external\n",
		"duplicate-version":   valid + "HOST_STATE_VERSION=1\n",
		"missing-enrollment":  strings.ReplaceAll(valid, "TAILSCALE_ENROLLED=1\n", ""),
		"invalid-enrollment":  strings.ReplaceAll(valid, "TAILSCALE_ENROLLED=1", "TAILSCALE_ENROLLED=true"),
		"missing-ownership":   strings.ReplaceAll(valid, "TAILSCALE_OWNERSHIP=managed\n", ""),
		"unknown-field":       valid + "UNEXPECTED=synthetic-private-value\n",
		"malformed-line":      valid + "synthetic-private-value\n",
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, HostInstallStateName), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadHostInstallState(directory); err == nil {
				t.Fatal("ambiguous ownership record was accepted")
			} else if strings.Contains(err.Error(), "synthetic-private-value") {
				t.Fatal("invalid ownership record leaked its contents")
			}
		})
	}
}

func TestReadHostInstallStateAcceptsAllExplicitOwnershipStates(t *testing.T) {
	for _, ownership := range []string{"managed", "external", "none"} {
		for _, enrolled := range []string{"0", "1"} {
			t.Run(ownership+enrolled, func(t *testing.T) {
				directory := t.TempDir()
				content := "HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=" + ownership + "\nTAILSCALE_ENROLLED=" + enrolled + "\n"
				if err := os.WriteFile(filepath.Join(directory, HostInstallStateName), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				state, err := ReadHostInstallState(directory)
				if err != nil || state.TailscaleOwnership != ownership || state.TailscaleEnrolled != (enrolled == "1") {
					t.Fatalf("valid ownership record rejected: %#v (%v)", state, err)
				}
			})
		}
	}
}
