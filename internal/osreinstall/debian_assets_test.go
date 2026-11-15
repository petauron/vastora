package osreinstall

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDebianArtifactRejectsUnsafeRequests(t *testing.T) {
	dir := t.TempDir()
	if _, err := DownloadDebianInstallerAsset(context.Background(), "amd64", "../../etc", dir); err == nil {
		t.Fatal("unknown artifact accepted")
	}
	unsafe := filepath.Join(dir, "unsafe")
	if err := os.Mkdir(unsafe, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := DownloadDebianInstallerAsset(context.Background(), "amd64", "linux", unsafe); err == nil {
		t.Fatal("non-private staging accepted")
	}
}
