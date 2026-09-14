package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestHostUpdateStartDoesNotResetUnloadedUnit(t *testing.T) {
	for _, state := range []string{"inactive", "not-found", "failed", "active", "cleanup", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			var calls []string
			run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] != "show" {
					return nil, nil
				}
				if state == "unavailable" {
					return nil, errors.New("systemd unavailable")
				}
				if state == "not-found" {
					return []byte("LoadState=not-found\nActiveState=inactive\n"), nil
				}
				if state == "cleanup" {
					return []byte("LoadState=loaded\nActiveState=inactive\nMainPID=0\nControlPID=123\n"), nil
				}
				return []byte("LoadState=loaded\nActiveState=" + state + "\nMainPID=0\nControlPID=0\n"), nil
			}
			err := startHostUpdateHelper(context.Background(), run)
			want := []string{"daemon-reload", "disable", "show"}
			success := state == "inactive" || state == "not-found" || state == "failed"
			if state == "failed" {
				want = append(want, "reset-failed")
			}
			if success {
				want = append(want, "start")
			}
			if (err == nil) != success || !slices.Equal(calls, want) {
				t.Fatalf("calls=%v want=%v err=%v", calls, want, err)
			}
		})
	}
}

func TestHostUpdateNewAuthorizationClearsObsoleteStaging(t *testing.T) {
	for _, mode := range []string{"superseded", "explicit-retry", "other-agent", "same-execution", "running-helper", "changed-binary", "cancelled", "recovery", "unexpected", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			directory := filepath.Join(root, "update")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			previous := hostUpdateOperation{Version: 1, TaskID: "agent-update-prior", Attempt: 1, TargetVersion: "0.1.0-alpha.126", SourceVersion: "0.1.0-alpha.125", DataDir: filepath.Join(root, "data"), Executable: filepath.Join(root, "installed"), AgentID: "agent", Credential: "synthetic-only"}
			next := previous
			next.ExecutionID, next.SessionID, next.TaskID = "new-execution", "new-session", "agent-update-new"
			next.SourceVersion, next.TargetVersion = "0.1.0-alpha.136", "0.1.0-alpha.137"
			switch mode {
			case "explicit-retry":
				previous.ExecutionID, previous.SessionID = "old-execution", "old-session"
				previous.SourceVersion, previous.TargetVersion = next.SourceVersion, next.TargetVersion
				next.TaskID, next.Attempt = previous.TaskID, 2
			case "other-agent":
				previous.AgentID = "another-agent"
			case "same-execution":
				previous.ExecutionID = next.ExecutionID
			case "cancelled":
				if err := os.WriteFile(filepath.Join(directory, "cancelled"), []byte("cancelled\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(previous)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "operation.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "vastora"), []byte("staged binary"), 0o700); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "recovery":
				if err := os.Mkdir(filepath.Join(directory, "pre-migration-recovery"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "unexpected":
				if err := os.WriteFile(filepath.Join(directory, "unknown"), []byte("retain"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(directory, "vastora"), filepath.Join(directory, "result.json")); err != nil {
					t.Fatal(err)
				}
			}
			run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[0] != "show" {
					t.Fatal("retirement must not start or stop a helper")
				}
				state := "inactive"
				if mode == "running-helper" {
					state = "activating"
				}
				return []byte("LoadState=loaded\nActiveState=" + state + "\nMainPID=0\nControlPID=0\n"), nil
			}
			version := func(context.Context, string) (string, error) {
				if mode == "changed-binary" {
					return next.TargetVersion, nil
				}
				return next.SourceVersion, nil
			}
			err = prepareHostUpdateDirectory(context.Background(), directory, next, run, version)
			success := mode == "superseded" || mode == "explicit-retry"
			if (err == nil) != success {
				t.Fatalf("unexpected retirement result: %v", err)
			}
			archives, err := filepath.Glob(directory + "-retired-*")
			if err != nil || len(archives) != 0 {
				t.Fatalf("unexpected archives: %v %v", archives, err)
			}
			if success {
				entries, err := os.ReadDir(directory)
				if err != nil || len(entries) != 0 {
					t.Fatalf("next attempt inherited old files: %v %v", entries, err)
				}
				return
			}
			retained, err := os.ReadFile(filepath.Join(directory, "operation.json"))
			if err != nil || string(retained) != string(raw) {
				t.Fatal("old operation evidence was changed")
			}
			if _, err := os.Stat(filepath.Join(directory, "vastora")); err != nil {
				t.Fatal("rejected replacement removed prior staging")
			}
		})
	}
}

func TestHostUpdateStartStopsAtCommandFailure(t *testing.T) {
	for _, failed := range []string{"daemon-reload", "disable", "reset-failed", "start"} {
		t.Run(failed, func(t *testing.T) {
			var calls []string
			run := func(_ context.Context, _ string, args ...string) ([]byte, error) {
				calls = append(calls, args[0])
				if args[0] == failed {
					return nil, errors.New("command failed")
				}
				return []byte("LoadState=loaded\nActiveState=failed\nMainPID=0\nControlPID=0\n"), nil
			}
			if err := startHostUpdateHelper(context.Background(), run); err == nil || calls[len(calls)-1] != failed || strings.Count(strings.Join(calls, ","), failed) != 1 {
				t.Fatalf("continued or retried after failure: %v %v", calls, err)
			}
		})
	}
}
