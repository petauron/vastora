package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/controlplane"
)

func TestControlPlaneSwitchRollbackRestoresExternalTailscaleAndAgent(t *testing.T) {
	directory := t.TempDir()
	dataDir := filepath.Join(directory, "agent")
	store, err := agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, _, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	previous := agent.Connection{AgentID: "old-agent", Name: "old-node", CenterURL: "https://old.example.com", Credential: "old-credential", PrivateKey: privateKey, CAFingerprint: strings.Repeat("a", 64)}
	if err := store.SaveConnection(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(directory, "host.env"), filepath.Join(directory, "unit"), filepath.Join(directory, "hosts")}
	for index, path := range paths {
		content := []byte{byte('a' + index)}
		if path == paths[1] {
			content = []byte(systemdAgentUnit("/usr/local/bin/vastora", dataDir, "worker", "docker"))
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	originalFiles := make(map[string][]byte, len(paths))
	for _, path := range paths {
		originalFiles[path], _ = os.ReadFile(path)
	}
	var commands []string
	profileLists := 0
	verified := false
	environment := controlPlaneSwitchEnvironment{
		journalPath: filepath.Join(dataDir, "control-plane-switch.json"),
		unitPath:    paths[1],
		paths:       paths,
		lookPath: func(string) (string, error) {
			return "/usr/bin/tailscale", nil
		},
		run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			command := strings.Join(append([]string{name}, arguments...), " ")
			commands = append(commands, command)
			if command == "tailscale switch --list --json" {
				profileLists++
				if profileLists == 1 {
					return []byte(`[{"id":"old-profile","selected":true}]`), nil
				}
				return []byte(`[{"id":"old-profile","selected":false},{"id":"new-profile","selected":true}]`), nil
			}
			return nil, nil
		},
		verify: func(_ context.Context, store *agent.Store, roles []string, capabilities agent.Capabilities) error {
			connection, err := store.Connection(context.Background())
			verified = err == nil && connection.AgentID == previous.AgentID && len(roles) == 1 && roles[0] == "worker" && capabilities.Docker
			return err
		},
	}
	if complete, err := beginControlPlaneSwitch(context.Background(), dataDir, "https://new.example.com", environment); err != nil || complete {
		t.Fatal(err)
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("mutated"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store, err = agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := store.BeginEnrollmentOperation(context.Background(), "https://new.example.com", "token", strings.Repeat("b", 64), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteEnrollmentOperation(context.Background(), operation, agent.Enrollment{ID: "new-agent", Name: "new-node", Credential: "new-credential", Roles: []string{"worker"}, Capabilities: agent.Capabilities{Docker: true}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rollbackControlPlaneSwitch(context.Background(), dataDir, environment); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != string(originalFiles[path]) {
			t.Fatalf("file %s was not restored: content=%q err=%v", path, content, err)
		}
	}
	store, err = agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := store.Connection(context.Background())
	store.Close()
	if err != nil || restored.AgentID != previous.AgentID || restored.CenterURL != previous.CenterURL || restored.Credential != previous.Credential {
		t.Fatalf("previous Agent was not restored: connection=%#v err=%v", restored, err)
	}
	if !containsCommand(commands, "tailscale switch old-profile") {
		t.Fatalf("previous Tailscale profile was not restored: %#v", commands)
	}
	if !containsCommand(commands, "tailscale switch new-profile") || !containsCommand(commands, "tailscale logout") {
		t.Fatalf("new Tailscale profile was not removed: %#v", commands)
	}
	if !verified {
		t.Fatal("restored Agent heartbeat was not verified")
	}
	if _, err := os.Stat(environment.journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("completed rollback journal remains: %v", err)
	}
}

func TestControlPlaneSwitchRollbackRemovesManagedTailscaleInstalledDuringFailure(t *testing.T) {
	directory := t.TempDir()
	dataDir := filepath.Join(directory, "agent")
	store, err := agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, _, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConnection(context.Background(), agent.Connection{AgentID: "old", Name: "old", CenterURL: "http://127.0.0.1:8080", Credential: "credential", PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	installed := false
	var commands []string
	stateDir := filepath.Join(directory, "tailscale-state")
	unitPath := filepath.Join(directory, "unit")
	if err := os.WriteFile(unitPath, []byte(systemdAgentUnit("/usr/local/bin/vastora", dataDir, "worker", "docker")), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := controlPlaneSwitchEnvironment{
		journalPath: filepath.Join(dataDir, "control-plane-switch.json"),
		unitPath:    unitPath,
		stateDir:    stateDir,
		paths:       []string{filepath.Join(directory, "host.env"), unitPath},
		lookPath: func(string) (string, error) {
			if installed {
				return "/usr/bin/tailscale", nil
			}
			return "", exec.ErrNotFound
		},
		removeAll: os.RemoveAll,
		run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			commands = append(commands, strings.Join(append([]string{name}, arguments...), " "))
			return nil, nil
		},
		verify: func(context.Context, *agent.Store, []string, agent.Capabilities) error { return nil },
	}
	if complete, err := beginControlPlaneSwitch(context.Background(), dataDir, "https://new.example.com", environment); err != nil || complete {
		t.Fatal(err)
	}
	installed = true
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := rollbackControlPlaneSwitch(context.Background(), dataDir, environment); err != nil {
		t.Fatal(err)
	}
	if !containsCommand(commands, "apt-get remove -y tailscale tailscale-archive-keyring") {
		t.Fatalf("Tailscale installed by the failed switch was not removed: %#v", commands)
	}
	if _, err := os.Stat(stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Tailscale state created by failed switch remains: %v", err)
	}
}

func TestControlPlaneSwitchCommitIsReplayableAfterDurableDecision(t *testing.T) {
	directory := t.TempDir()
	dataDir := filepath.Join(directory, "agent")
	store, err := agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, _, err := controlplane.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConnection(context.Background(), agent.Connection{AgentID: "old", Name: "old", CenterURL: "http://127.0.0.1:8080", Credential: "old-credential", PrivateKey: privateKey}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	unitPath := filepath.Join(directory, "unit")
	if err := os.WriteFile(unitPath, []byte(systemdAgentUnit("/usr/local/bin/vastora", dataDir, "worker", "docker")), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := controlPlaneSwitchEnvironment{
		journalPath: filepath.Join(dataDir, "control-plane-switch.json"),
		unitPath:    unitPath,
		stateDir:    filepath.Join(directory, "tailscale-state"),
		paths:       []string{filepath.Join(dataDir, "host-install.env"), unitPath},
		lookPath:    func(string) (string, error) { return "", exec.ErrNotFound },
		removeAll:   os.RemoveAll,
		run:         func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		verify:      func(context.Context, *agent.Store, []string, agent.Capabilities) error { return nil },
	}
	if complete, err := beginControlPlaneSwitch(context.Background(), dataDir, "http://127.0.0.1:8081", environment); err != nil || complete {
		t.Fatalf("begin switch: complete=%v err=%v", complete, err)
	}
	store, err = agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := store.BeginEnrollmentOperation(context.Background(), "http://127.0.0.1:8081", "token", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteEnrollmentOperation(context.Background(), operation, agent.Enrollment{ID: "new", Name: "new", Credential: "new-credential", Roles: []string{"worker"}, Capabilities: agent.Capabilities{Docker: true}}); err != nil {
		t.Fatal(err)
	}
	for _, transition := range [][2]string{{"enrolled", "unit_written"}, {"unit_written", "reloaded"}, {"reloaded", "enabled"}, {"enabled", "started"}, {"started", "healthy"}} {
		if err := store.AdvanceInstallOperation(context.Background(), transition[0], transition[1]); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	if err := commitControlPlaneSwitch(dataDir, "auto", true, environment); err != nil {
		t.Fatal(err)
	}
	store, err = agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists, err := store.InstallOperation(context.Background()); err != nil || exists {
		t.Fatalf("committed install operation remains: exists=%v err=%v", exists, err)
	}
	store.Close()
	hostState, err := os.ReadFile(filepath.Join(dataDir, "host-install.env"))
	if err != nil || !strings.Contains(string(hostState), "TAILSCALE_OWNERSHIP=managed") || !strings.Contains(string(hostState), "TAILSCALE_ENROLLED=1") {
		t.Fatalf("committed host state = %q, err=%v", hostState, err)
	}
	if _, err := os.Stat(environment.journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed switch journal remains: %v", err)
	}

	if complete, err := beginControlPlaneSwitch(context.Background(), dataDir, "http://127.0.0.1:8082", environment); err != nil || complete {
		t.Fatalf("begin replay switch: complete=%v err=%v", complete, err)
	}
	store, err = agent.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	operation, err = store.BeginEnrollmentOperation(context.Background(), "http://127.0.0.1:8082", "next-token", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteEnrollmentOperation(context.Background(), operation, agent.Enrollment{ID: "final", Name: "final", Credential: "final-credential", Roles: []string{"worker"}, Capabilities: agent.Capabilities{Docker: true}}); err != nil {
		t.Fatal(err)
	}
	for _, transition := range [][2]string{{"enrolled", "unit_written"}, {"unit_written", "reloaded"}, {"reloaded", "enabled"}, {"enabled", "started"}, {"started", "healthy"}} {
		if err := store.AdvanceInstallOperation(context.Background(), transition[0], transition[1]); err != nil {
			t.Fatal(err)
		}
	}
	store.Close()
	journal, err := readControlPlaneSwitchJournal(environment)
	if err != nil {
		t.Fatal(err)
	}
	journal.Committed = true
	if err := writeControlPlaneSwitchJournal(environment, journal); err != nil {
		t.Fatal(err)
	}
	complete, err := beginControlPlaneSwitch(context.Background(), dataDir, "http://127.0.0.1:8082", environment)
	if err != nil || !complete {
		t.Fatalf("resume committed switch: complete=%v err=%v", complete, err)
	}
}

func containsCommand(commands []string, expected string) bool {
	for _, command := range commands {
		if command == expected {
			return true
		}
	}
	return false
}
