package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
)

func TestSystemdAgentInstallResumesEveryHostPhaseAfterRestart(t *testing.T) {
	for _, failurePoint := range []string{"write", "rename", "daemon-reload", "enable", "start", "verify"} {
		t.Run(failurePoint, func(t *testing.T) {
			directory := t.TempDir()
			store, err := agent.Open(filepath.Join(directory, "data"))
			if err != nil {
				t.Fatal(err)
			}
			operation, err := store.BeginEnrollmentOperation(context.Background(), "http://127.0.0.1:8080", "one-time-token", "", "", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CompleteEnrollmentOperation(context.Background(), operation, agent.Enrollment{
				ID: "agent-1", Credential: "credential", Name: "node", Roles: []string{"worker"}, Capabilities: agent.Capabilities{Docker: true},
			}); err != nil {
				t.Fatal(err)
			}
			unitPath := filepath.Join(directory, "vastora-agent.service")
			failed := false
			environment := agentSystemdInstallEnvironment{
				unitPath: unitPath,
				readFile: os.ReadFile,
				writeFile: func(path string, content []byte, mode os.FileMode) error {
					if failurePoint == "write" && !failed {
						failed = true
						return errors.New("injected write failure")
					}
					return os.WriteFile(path, content, mode)
				},
				rename: func(oldPath, newPath string) error {
					if failurePoint == "rename" && !failed {
						failed = true
						return errors.New("injected rename failure")
					}
					return os.Rename(oldPath, newPath)
				},
				runSystemctl: func(arguments ...string) ([]byte, error) {
					action := strings.Join(arguments, " ")
					if strings.HasPrefix(action, failurePoint) && !failed {
						failed = true
						return []byte("injected systemctl failure"), errors.New("injected failure")
					}
					return nil, nil
				},
				verify: func(context.Context, *agent.Store, string, string) error {
					if failurePoint == "verify" && !failed {
						failed = true
						return errors.New("injected heartbeat failure")
					}
					return nil
				},
			}
			if err := resumeSystemdAgentInstall(context.Background(), store, "/usr/local/bin/vastora", filepath.Join(directory, "data"), "worker", "docker", false, environment); err == nil {
				t.Fatalf("%s failure was not reported", failurePoint)
			}
			if !failed {
				t.Fatalf("%s failure was not reached", failurePoint)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = agent.Open(filepath.Join(directory, "data"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := resumeSystemdAgentInstall(context.Background(), store, "/usr/local/bin/vastora", filepath.Join(directory, "data"), "worker", "docker", false, environment); err != nil {
				t.Fatal(err)
			}
			if _, exists, err := store.InstallOperation(context.Background()); err != nil || exists {
				t.Fatalf("completed operation remains: exists=%v err=%v", exists, err)
			}
		})
	}
}

func TestReplacementSystemdInstallWaitsForHostSwitchCommit(t *testing.T) {
	directory := t.TempDir()
	store, err := agent.Open(filepath.Join(directory, "data"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	privateKey, _, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConnection(context.Background(), agent.Connection{AgentID: "old", Name: "old", CenterURL: "http://127.0.0.1:8080", Credential: "old-credential", PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	operation, err := store.BeginEnrollmentOperation(context.Background(), "http://127.0.0.1:8081", "token", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteEnrollmentOperation(context.Background(), operation, agent.Enrollment{ID: "new", Name: "new", Credential: "new-credential", Roles: []string{"worker"}, Capabilities: agent.Capabilities{Docker: true}}); err != nil {
		t.Fatal(err)
	}
	environment := agentSystemdInstallEnvironment{
		unitPath:     filepath.Join(directory, "vastora-agent.service"),
		readFile:     os.ReadFile,
		writeFile:    os.WriteFile,
		rename:       os.Rename,
		runSystemctl: func(...string) ([]byte, error) { return nil, nil },
		verify:       func(context.Context, *agent.Store, string, string) error { return nil },
	}
	if err := resumeSystemdAgentInstall(context.Background(), store, "/usr/local/bin/vastora", filepath.Join(directory, "data"), "worker", "docker", true, environment); err != nil {
		t.Fatal(err)
	}
	pending, exists, err := store.InstallOperation(context.Background())
	if err != nil || !exists || pending.Phase != "healthy" {
		t.Fatalf("replacement commit boundary was lost: operation=%#v exists=%v err=%v", pending, exists, err)
	}
}
