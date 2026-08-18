package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/agent"
)

func TestNodeRolesAndCapabilitiesAreIndependent(t *testing.T) {
	roles, err := parseNodeRoles("worker,gateway")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, err := parseNodeCapabilities("docker")
	if err != nil {
		t.Fatal(err)
	}
	if len(roles) != 2 || !containsValue(roles, "gateway") || !capabilities.Docker || capabilities.Gateway {
		t.Fatalf("roles and capabilities were not parsed independently: roles=%v capabilities=%#v", roles, capabilities)
	}
}

func TestUnimplementedCapabilitiesCannotBeAdvertised(t *testing.T) {
	for _, capability := range []string{"metrics", "logs"} {
		if _, err := parseNodeCapabilities(capability); err == nil {
			t.Fatalf("unimplemented capability %q was accepted", capability)
		}
	}
}

func TestCenterBootstrapDoesNotRequireSuggestedAgentURL(t *testing.T) {
	missingCatalog := filepath.Join(t.TempDir(), "missing-catalog.json")
	err := runCenter([]string{"serve", "--data-dir", t.TempDir(), "--official-catalog", missingCatalog})
	if err == nil || !strings.Contains(err.Error(), "read official catalog") {
		t.Fatalf("Center did not reach startup without --agent-connect-url: %v", err)
	}
}

func TestSystemdAgentUnitUsesPersistentServiceConfiguration(t *testing.T) {
	unit := systemdAgentUnit("/usr/local/bin/vastora", "/var/lib/vastora/agent", "worker,gateway", "docker,gateway,tunnel")
	for _, expected := range []string{
		"Description=Vastora Agent",
		`ExecStart="/usr/local/bin/vastora" agent serve --data-dir "/var/lib/vastora/agent" --roles "worker,gateway" --capabilities "docker,gateway,tunnel"`,
		"Restart=always",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("systemd unit is missing %q:\n%s", expected, unit)
		}
	}
}

func TestAgentUpdateVerifiesAndKeepsRollbackBinary(t *testing.T) {
	newBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then printf '0.2.0\\n'; fi\n")
	digest := sha256.Sum256(newBinary)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		wantPath := "/api/v1/agents/node-id/binary/linux/" + runtime.GOARCH
		if request.URL.Path != wantPath || request.Header.Get("Authorization") != "Bearer node-credential" {
			t.Fatalf("unexpected update request: %s auth=%q", request.URL.Path, request.Header.Get("Authorization"))
		}
		response.Header().Set("X-Vastora-Version", "0.2.0")
		response.Header().Set("X-Vastora-SHA256", fmt.Sprintf("%x", digest[:]))
		_, _ = response.Write(newBinary)
	}))
	defer server.Close()
	executable := filepath.Join(t.TempDir(), "vastora")
	oldBinary := []byte("old-agent-binary")
	if err := os.WriteFile(executable, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	version, err := updateAgentExecutable(context.Background(), server.Client(), agent.Connection{AgentID: "node-id", CenterURL: server.URL, Credential: "node-credential"}, executable, func() error { restarts++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	installed, _ := os.ReadFile(executable)
	rollback, _ := os.ReadFile(executable + ".previous")
	if version != "0.2.0" || restarts != 1 || string(installed) != string(newBinary) || string(rollback) != string(oldBinary) {
		t.Fatalf("unexpected Agent update: version=%q restarts=%d installed=%q rollback=%q", version, restarts, installed, rollback)
	}
}

func TestAgentUpdateRestoresPreviousBinaryWhenRestartFails(t *testing.T) {
	newBinary := []byte("#!/bin/sh\nif [ \"$1\" = version ]; then printf '0.2.0\\n'; fi\n")
	digest := sha256.Sum256(newBinary)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("X-Vastora-Version", "0.2.0")
		response.Header().Set("X-Vastora-SHA256", fmt.Sprintf("%x", digest[:]))
		_, _ = response.Write(newBinary)
	}))
	defer server.Close()
	executable := filepath.Join(t.TempDir(), "vastora")
	oldBinary := []byte("old-agent-binary")
	if err := os.WriteFile(executable, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	_, err := updateAgentExecutable(context.Background(), server.Client(), agent.Connection{AgentID: "node-id", CenterURL: server.URL, Credential: "node-credential"}, executable, func() error {
		restarts++
		if restarts == 1 {
			return fmt.Errorf("new service failed")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "previous binary restored") {
		t.Fatalf("restart failure was not reported: %v", err)
	}
	restored, _ := os.ReadFile(executable)
	if restarts != 2 || string(restored) != string(oldBinary) {
		t.Fatalf("previous Agent was not restored: restarts=%d content=%q", restarts, restored)
	}
}
