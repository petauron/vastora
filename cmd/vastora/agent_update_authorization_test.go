package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestHostUpdateAuthorizationFailureStopsEveryFollowingMutation(t *testing.T) {
	phases := []string{"stop", "backup", "preserve", "install", "start"}
	for index, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			executable, candidate := filepath.Join(root, "installed"), filepath.Join(root, "candidate")
			for path, value := range map[string]string{executable: "source", candidate: "target"} {
				if err := os.WriteFile(path, []byte(value), 0700); err != nil {
					t.Fatal(err)
				}
			}
			operation := hostUpdateOperation{Executable: executable, SourceVersion: "source", TargetVersion: "target"}
			var called []string
			stops, starts, backups := 0, 0, 0
			environment := hostUpdateActivationEnvironment{
				candidatePath: candidate, recoveryDirectory: filepath.Join(root, "recovery"),
				authorize: func(_ context.Context, p string) error {
					called = append(called, p)
					if p == phase {
						return errors.New("authorization unavailable")
					}
					return nil
				},
				version: func(_ context.Context, p string) (string, error) { raw, err := os.ReadFile(p); return string(raw), err },
				run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
					if args[0] == "stop" {
						stops++
					}
					if args[0] == "start" {
						starts++
					}
					return nil, nil
				},
				prepareRecovery: func(_ context.Context, _ hostUpdateOperation, path string) error {
					backups++
					return os.Mkdir(path, 0700)
				},
				serviceActive: func(context.Context) bool { return true }, wait: func(context.Context) error { return nil },
			}
			if err := activateHostUpdate(context.Background(), operation, environment); !errors.Is(err, errHostUpdateCandidatePending) {
				t.Fatalf("missing uncertain outcome: %v", err)
			}
			if !slices.Equal(called, phases[:index+1]) || starts != 0 {
				t.Fatalf("continued after authorization failure: steps=%v starts=%d", called, starts)
			}
			wantStops, wantBackups := 0, 0
			if index > 0 {
				wantStops = 1
			}
			if index > 1 {
				wantBackups = 1
			}
			if stops != wantStops || backups != wantBackups {
				t.Fatalf("unexpected mutations: stops=%d backups=%d", stops, backups)
			}
			raw, err := os.ReadFile(executable)
			want := "source"
			if phase == "start" {
				want = "target"
			}
			if err != nil || string(raw) != want {
				t.Fatalf("incorrect retained executable: %q %v", raw, err)
			}
			_, err = os.Stat(executable + ".previous")
			if index < 3 && !errors.Is(err, os.ErrNotExist) {
				t.Fatal("preserved executable before authorization")
			}
			if index >= 3 && err != nil {
				t.Fatal("lost preserved executable")
			}
		})
	}
}
