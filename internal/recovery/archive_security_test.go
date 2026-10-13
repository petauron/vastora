package recovery

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/backupcrypto"
)

func TestReadRejectsTraversalAndLinkMembers(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		headers []tar.Header
		want    string
	}{
		{name: "parent", headers: []tar.Header{{Name: "../escape", Typeflag: tar.TypeReg}}, want: "invalid backup archive"},
		{name: "absolute", headers: []tar.Header{{Name: "/escape", Typeflag: tar.TypeReg}}, want: "invalid backup archive"},
		{name: "windows traversal", headers: []tar.Header{{Name: `..\escape`, Typeflag: tar.TypeReg}}, want: "invalid backup archive"},
		{name: "drive relative", headers: []tar.Header{{Name: "C:escape", Typeflag: tar.TypeReg}}, want: "invalid backup archive"},
		{name: "symlink", headers: []tar.Header{{Name: "agent.key", Typeflag: tar.TypeSymlink, Linkname: "../escape"}}, want: "invalid backup archive"},
		{name: "hardlink", headers: []tar.Header{{Name: "agent.key", Typeflag: tar.TypeLink, Linkname: "../escape"}}, want: "invalid backup archive"},
		{name: "duplicate", headers: []tar.Header{{Name: "agent.key", Typeflag: tar.TypeReg}, {Name: "agent.key", Typeflag: tar.TypeReg}}, want: "duplicate backup member"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var archive bytes.Buffer
			writer := tar.NewWriter(&archive)
			for _, header := range scenario.headers {
				if err := writer.WriteHeader(&header); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			const password = "a-protected-backup-password"
			encrypted, err := backupcrypto.Encrypt(archive.Bytes(), password)
			if err != nil {
				t.Fatal(err)
			}
			input := filepath.Join(t.TempDir(), "malicious.vastora")
			if err := os.WriteFile(input, encrypted, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Read(input, password); err == nil || !strings.Contains(err.Error(), scenario.want) {
				t.Fatalf("malicious archive was not rejected at the member boundary: %v", err)
			}
		})
	}
}

func TestArtifactStagingRejectsAllUnsafeNamesBeforeWriting(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../escape", "dir/escape", `dir\escape`, "C:escape", "file\x00suffix"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			if err := writeArtifactFiles(directory, map[string][]byte{"agent.key": []byte("valid"), name: []byte("malicious")}); err == nil {
				t.Fatal("unsafe artifact member accepted")
			}
			entries, err := os.ReadDir(directory)
			if err != nil || len(entries) != 0 {
				t.Fatal("invalid member allowed partial artifact staging", err)
			}
		})
	}
}

func TestArtifactStagingIsPrivateExclusiveAndSymlinkConfined(t *testing.T) {
	directory := t.TempDir()
	if err := writeArtifactFiles(directory, map[string][]byte{"agent.key": []byte("original")}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "agent.key")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("artifact member is not private", err)
	}
	if err := writeArtifactFiles(directory, map[string][]byte{"agent.key": []byte("replacement")}); err == nil {
		t.Fatal("existing artifact member overwritten")
	}
	outside := filepath.Join(t.TempDir(), "outside.key")
	if err := os.WriteFile(outside, []byte("outside original"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"outside-link": outside, "inside-link": "agent.key"} {
		if err := os.Symlink(target, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
		if err := writeArtifactFiles(directory, map[string][]byte{name: []byte("replacement")}); err == nil {
			t.Fatal("pre-existing symlink followed")
		}
	}
	for path, want := range map[string]string{path: "original", outside: "outside original"} {
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != want {
			t.Fatal("artifact staging changed an existing or out-of-root file", err)
		}
	}
}
