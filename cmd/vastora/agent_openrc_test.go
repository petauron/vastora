package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRCAgentAndUpdaterAreIndependentServices(t *testing.T) {
	agent := openRCAgentService("/usr/local/bin/vastora", "/var/lib/vastora/agent", "worker", "landing")
	for _, part := range []string{"#!/sbin/openrc-run", "supervisor=supervise-daemon", "agent prepare-runtime-directories", "--data-dir", "respawn_delay=5"} {
		if !strings.Contains(agent, part) {
			t.Fatal("missing startup behavior", part)
		}
	}
	update := openRCHostUpdateService()
	if !strings.Contains(update, "command_background=true") || !strings.Contains(update, "agent finish-update --operation-file") || strings.Contains(update, "supervisor=") {
		t.Fatal("updater must remain a separate bounded oneshot")
	}
	if strings.Contains(agent, "systemctl") || strings.Contains(update, "systemctl") {
		t.Fatal("systemd CLI used on OpenRC")
	}
}

func TestOpenRCUpdateServiceOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vastora-agent-update")
	if err := verifyOpenRCUpdateService(path); err != nil {
		t.Fatal("absent helper must be installable", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyOpenRCUpdateService(path); err == nil {
		t.Fatal("unowned service must not be replaced")
	}
	if err := os.WriteFile(path, []byte(openRCHostUpdateService()), 0700); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := verifyOpenRCUpdateService(path); err != nil {
			t.Fatal("owned helper rejected", err)
		}
	}
	if err := os.Chmod(path, 0777); err != nil {
		t.Fatal(err)
	}
	if err := verifyOpenRCUpdateService(path); err == nil {
		t.Fatal("writable helper must be rejected")
	}
	link := path + "-link"
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := verifyOpenRCUpdateService(link); err == nil {
		t.Fatal("symlink helper must be rejected")
	}
}
