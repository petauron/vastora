package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPrivatePasswordRequiresARestrictedRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup-password")
	if err := os.WriteFile(path, []byte("correct-horse-battery-staple\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	password, err := readPrivatePassword(path)
	if err != nil {
		t.Fatal(err)
	}
	if password != "correct-horse-battery-staple" {
		t.Fatalf("password = %q", password)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivatePassword(path); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Fatalf("broad password-file permissions were accepted: %v", err)
	}
	if _, err := readPrivatePassword(t.TempDir()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory password file was accepted: %v", err)
	}
}

func TestReadPrivatePasswordRejectsAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup-password")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivatePassword(path); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty password file was accepted: %v", err)
	}
}
