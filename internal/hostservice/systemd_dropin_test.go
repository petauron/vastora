package hostservice

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedSystemdDropinPreservesOwnershipAndRollsBack(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sample.service.d")
	path := filepath.Join(dir, "90-vastora.conf")
	old := []byte("# owned\nold\n")
	next := []byte("# owned\nnew\n")
	owned := func(raw []byte) bool { return bytes.Equal(raw, old) || bytes.Equal(raw, next) }
	calls := 0
	run := func(context.Context, string, ...string) error { calls++; return nil }
	ensure := func(raw []byte) error {
		return EnsureSystemdDropin(context.Background(), dir, filepath.Base(path), raw, owned, run)
	}
	if err := ensure(old); err != nil || calls != 1 {
		t.Fatal("initial installation failed", err)
	}
	if err := ensure(old); err != nil || calls != 1 {
		t.Fatal("unchanged drop-in must not reload systemd", err)
	}
	recoveryCalls := 0
	run = func(context.Context, string, ...string) error {
		recoveryCalls++
		if recoveryCalls == 1 {
			return errors.New("reload failed")
		}
		return nil
	}
	if err := ensure(next); err == nil || recoveryCalls != 2 {
		t.Fatal("reload failure was not rolled back and reloaded")
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, old) {
		t.Fatal("reload failure did not restore the owned file", err)
	}
	if err := os.WriteFile(path, []byte("unowned\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ensure(next); err == nil {
		t.Fatal("unowned drop-in was overwritten")
	}
	got, _ = os.ReadFile(path)
	if string(got) != "unowned\n" {
		t.Fatal("unowned drop-in changed")
	}
}

func TestManagedSystemdDropinRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	target := t.TempDir()
	dir := filepath.Join(root, "example.service.d")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	err := EnsureSystemdDropin(context.Background(), dir, "90-vastora.conf", []byte("owned"), func([]byte) bool { return true }, func(context.Context, string, ...string) error { return nil })
	if err == nil {
		t.Fatal("symlinked drop-in directory was accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "90-vastora.conf")); !os.IsNotExist(err) {
		t.Fatal("symlink target was changed")
	}
}
