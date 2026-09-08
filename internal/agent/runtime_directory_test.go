package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdminRuntimeDirectoryRecoversAfterReboot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "volatile", "vastora", "admin.sock")
	for attempt := 0; attempt < 2; attempt++ {
		if err := prepareAdminSocketDirectory(socket); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(filepath.Dir(socket))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime directory protection: %v %v", info, err)
	}
}

func TestAdminRuntimeDirectoryRejectsUnsafeExistingPaths(t *testing.T) {
	for _, kind := range []string{"symlink", "permissions", "socket-file"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(root, "vastora")
			socket := filepath.Join(directory, "admin.sock")
			if kind == "symlink" {
				err = os.Symlink(root, directory)
			} else {
				err = os.Mkdir(directory, 0o700)
				if err == nil && kind == "permissions" {
					err = os.Chmod(directory, 0o755)
				}
				if err == nil && kind == "socket-file" {
					err = os.WriteFile(socket, []byte("unrelated"), 0o600)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := prepareAdminSocketDirectory(socket); err == nil {
				t.Fatal("unsafe path was adopted")
			}
		})
	}
}
