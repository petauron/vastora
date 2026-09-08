package recovery

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
)

func TestAgentArtifactPreservesIdentityAndRejectsUnsafeRestore(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	store, err := agent.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	private, _, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	connection := agent.Connection{AgentID: "agent-recovery", Name: "Recovery", CenterURL: "http://127.0.0.1:8080", Credential: "protected-credential", PrivateKey: private}
	if err := store.SaveConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, agent.HostInstallStateName), []byte("HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=none\nTAILSCALE_ENROLLED=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "agent.vastora")
	password := "a-protected-backup-password"
	artifact, err := ExportAgent(ctx, directory, "", "test-release", output, password)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(connection.Credential)) || bytes.Contains(raw, private) {
		t.Fatal("artifact contains plaintext credentials")
	}
	if _, err := Inspect(ctx, output, "wrong-password-value", "test-release"); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err := Inspect(ctx, output, password, "different-release"); err == nil {
		t.Fatal("release mismatch accepted")
	}
	for _, scenario := range []string{"identity", "existing", "valid"} {
		destination := filepath.Join(t.TempDir(), "restored")
		expected := artifact.Manifest.IdentityHash
		if scenario == "identity" {
			expected = Digest([]byte("another identity"))
		}
		if scenario == "existing" {
			if err := os.Mkdir(destination, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		_, err := Restore(ctx, output, destination, password, "test-release", connection.AgentID, expected)
		if scenario != "valid" {
			if err == nil {
				t.Fatal("unsafe restore accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		restored, err := agent.InspectConnection(destination)
		if err != nil || restored.AgentID != connection.AgentID {
			t.Fatalf("restored identity changed: %v", err)
		}
		if _, err := Restore(ctx, output, destination, password, "test-release", connection.AgentID, expected); err == nil {
			t.Fatal("retry overwrote restored state")
		}
	}
	raw[len(raw)-1] ^= 1
	tampered := filepath.Join(t.TempDir(), "tampered.vastora")
	if err := os.WriteFile(tampered, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(ctx, tampered, password, "test-release"); err == nil {
		t.Fatal("tampering accepted")
	}
}

func TestArtifactPathsAndExclusivePublication(t *testing.T) {
	output := filepath.Join(t.TempDir(), "artifact")
	if err := PublishPrivateFile(output, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := PublishPrivateFile(output, []byte("second")); err == nil {
		t.Fatal("artifact overwritten")
	}
	actual, _ := os.ReadFile(output)
	if string(actual) != "first" {
		t.Fatal("original artifact changed")
	}
	for _, value := range []string{"../key", "/etc/key", "..", "dir/key", "dir\\key"} {
		if safeName(value) {
			t.Fatalf("unsafe path accepted: %q", value)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(output, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularFile(link, 1024); err == nil {
		t.Fatal("symlink artifact accepted")
	}
}
