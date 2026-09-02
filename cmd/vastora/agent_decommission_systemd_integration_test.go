package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agent"
)

const (
	systemdDecommissionIntegrationRoot           = "/var/lib/vastora-systemd-decommission-integration"
	systemdDecommissionIntegrationDataDir        = systemdDecommissionIntegrationRoot + "/agent"
	systemdDecommissionIntegrationBinary         = systemdDecommissionIntegrationRoot + "/vastora"
	systemdDecommissionIntegrationPreviousBinary = systemdDecommissionIntegrationRoot + "/vastora.previous"
	systemdDecommissionIntegrationAgentID        = "systemd-integration"
	systemdDecommissionIntegrationTaskID         = "agent-decommission-" + systemdDecommissionIntegrationAgentID
	systemdDecommissionIntegrationAgentLink      = "/etc/systemd/system/multi-user.target.wants/vastora-agent.service"
)

// This test runs only in the dedicated root CI step. It proves the complete
// systemd boundary which command-sequence unit tests cannot: a persistent
// helper stops a live Agent unit, removes its owned files, reports completion
// only after they disappear, and then removes its own unit and recovery state.
func TestHostDecommissionRealSystemdEndToEnd(t *testing.T) {
	if os.Getenv("VASTORA_SYSTEMD_DECOMMISSION_INTEGRATION") != "1" {
		t.Skip("real systemd integration is enabled only by the dedicated CI step")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("real systemd integration requires a root Linux runner")
	}
	if info, err := os.Stat("/run/systemd/system"); err != nil || !info.IsDir() {
		t.Fatalf("systemd is not the active service manager: %v", err)
	}

	ownedPaths := []string{
		systemdDecommissionIntegrationRoot,
		vastoraAgentUnitPath,
		systemdDecommissionIntegrationAgentLink,
		hostDecommissionDir,
		hostDecommissionUnit,
		hostDecommissionEnabledLink,
		hostDecommissionGenerator,
	}
	for _, path := range ownedPaths {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("refusing to replace an existing host path during integration: %s (%v)", path, err)
		}
	}
	defer decommissionIntegrationCleanup()

	if err := os.MkdirAll(systemdDecommissionIntegrationDataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]struct {
		content string
		mode    os.FileMode
	}{
		filepath.Join(systemdDecommissionIntegrationDataDir, agent.HostInstallStateName): {
			content: "HOST_STATE_VERSION=1\nTAILSCALE_OWNERSHIP=external\nTAILSCALE_ENROLLED=0\n",
			mode:    0o600,
		},
		systemdDecommissionIntegrationBinary:         {content: "owned Agent binary\n", mode: 0o700},
		systemdDecommissionIntegrationPreviousBinary: {content: "owned previous Agent binary\n", mode: 0o700},
		vastoraAgentUnitPath: {
			content: "[Unit]\nDescription=Vastora Agent\n\n[Service]\nType=simple\nExecStart=/bin/sleep infinity\n# managed agent serve command\n\n[Install]\nWantedBy=multi-user.target\n",
			mode:    0o644,
		},
	}
	for path, file := range files {
		if err := writeRootFileAtomic(path, []byte(file.content), file.mode); err != nil {
			t.Fatalf("create integration resource %s: %v", path, err)
		}
	}
	if output, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		t.Fatalf("load integration Agent unit: %s (%v)", output, err)
	}
	if output, err := exec.Command("systemctl", "enable", "--now", "vastora-agent.service").CombinedOutput(); err != nil {
		t.Fatalf("start integration Agent unit: %s (%v)", output, err)
	}
	if err := exec.Command("systemctl", "is-active", "--quiet", "vastora-agent.service").Run(); err != nil {
		t.Fatal("integration Agent unit did not become active")
	}

	var handoffs, completions atomic.Int32
	completed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/agents/" + systemdDecommissionIntegrationAgentID + "/decommission/start":
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer systemd-integration-agent-credential" {
				http.Error(response, "invalid handoff", http.StatusUnauthorized)
				return
			}
			handoffs.Add(1)
			writeSystemdIntegrationJSON(response, map[string]bool{"started": true})
		case "/api/v1/agent-decommission-results/" + systemdDecommissionIntegrationTaskID:
			if request.Method != http.MethodPost || request.Header.Get("Authorization") != "Bearer systemd-integration-callback-token" {
				http.Error(response, "invalid completion", http.StatusUnauthorized)
				return
			}
			var result struct {
				Attempt int64 `json:"attempt"`
			}
			if json.NewDecoder(request.Body).Decode(&result) != nil || result.Attempt != 1 {
				http.Error(response, "invalid result", http.StatusBadRequest)
				return
			}
			for _, path := range []string{
				systemdDecommissionIntegrationDataDir,
				systemdDecommissionIntegrationBinary,
				systemdDecommissionIntegrationPreviousBinary,
				vastoraAgentUnitPath,
				systemdDecommissionIntegrationAgentLink,
			} {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					http.Error(response, "owned host resource remains", http.StatusConflict)
					return
				}
			}
			if exec.Command("systemctl", "is-active", "--quiet", "vastora-agent.service").Run() == nil {
				http.Error(response, "Agent service remains active", http.StatusConflict)
				return
			}
			completions.Add(1)
			writeSystemdIntegrationJSON(response, map[string]bool{"completed": true})
			select {
			case completed <- struct{}{}:
			default:
			}
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	operation := hostDecommissionOperation{
		Version:       2,
		TaskID:        systemdDecommissionIntegrationTaskID,
		Attempt:       1,
		DeleteData:    true,
		DataDir:       systemdDecommissionIntegrationDataDir,
		AgentID:       systemdDecommissionIntegrationAgentID,
		CenterURL:     server.URL,
		Credential:    "systemd-integration-agent-credential",
		CallbackURL:   server.URL + "/api/v1/agent-decommission-results/" + systemdDecommissionIntegrationTaskID,
		CallbackToken: "systemd-integration-callback-token",
	}
	if err := writeSystemdIntegrationOperation(operation); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		t.Fatalf("load persistent host cleanup helper: %s (%v)", output, err)
	}
	if output, err := exec.Command("systemctl", "enable", "--now", hostDecommissionUnitName).CombinedOutput(); err != nil {
		t.Fatalf("start persistent host cleanup helper: %s (%v)", output, err)
	}

	select {
	case <-completed:
	case <-time.After(45 * time.Second):
		t.Fatalf("host cleanup did not report completion:\n%s", decommissionIntegrationDiagnostics())
	}
	deadline := time.Now().Add(45 * time.Second)
	for {
		allAbsent := true
		for _, path := range []string{hostDecommissionDir, hostDecommissionUnit, hostDecommissionEnabledLink, hostDecommissionGenerator} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				allAbsent = false
				break
			}
		}
		loadState, _ := exec.Command("systemctl", "show", "--property=LoadState", "--value", hostDecommissionUnitName).Output()
		if allAbsent && strings.TrimSpace(string(loadState)) == "not-found" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host cleanup finalizer did not disappear:\n%s", decommissionIntegrationDiagnostics())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if handoffs.Load() != 1 || completions.Load() != 1 {
		t.Fatalf("host cleanup callbacks: handoffs=%d completions=%d", handoffs.Load(), completions.Load())
	}
}

// The production unit starts a copied Vastora binary. The integration parent
// uses a tiny wrapper at that exact location so systemd starts this isolated
// child test with the same service and generator lifecycle.
func TestHostDecommissionRealSystemdHelperProcess(t *testing.T) {
	if os.Getenv("VASTORA_SYSTEMD_DECOMMISSION_HELPER") != "1" {
		t.Skip("helper process is started only by the real systemd integration")
	}
	operation, err := readHostDecommissionOperation(hostDecommissionOperationPath)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func(ctx context.Context, operation hostDecommissionOperation) error {
		return uninstallAgentHostWithEnvironment(ctx, operation.DeleteData, false, false, agentUninstallEnvironment{
			dataDir:     operation.DataDir,
			unitPath:    vastoraAgentUnitPath,
			binaryPaths: []string{systemdDecommissionIntegrationBinary, systemdDecommissionIntegrationPreviousBinary},
			purgeRuntime: func(context.Context, bool) error {
				return nil
			},
			run: runHostCommand,
		})
	}
	if err := runHostDecommission(context.Background(), hostDecommissionOperationPath, agent.Client{}, cleanup); err != nil {
		t.Fatal(err)
	}
	finalizer := hostDecommissionFinalizerUnit(hostDecommissionDir, hostDecommissionUnit, hostDecommissionEnabledLink, hostDecommissionGenerator)
	if err := activateHostDecommissionFinalizer(context.Background(), hostDecommissionUnit, hostDecommissionGenerator, hostDecommissionGeneratorScript(hostDecommissionDir, finalizer), runHostCommand); err != nil {
		t.Fatal(err)
	}
}

func writeSystemdIntegrationOperation(operation hostDecommissionOperation) error {
	if err := os.MkdirAll(hostDecommissionDir, 0o700); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	wrapper := "#!/bin/sh\nexec env VASTORA_SYSTEMD_DECOMMISSION_HELPER=1 " + strconv.Quote(executable) + " -test.run '^TestHostDecommissionRealSystemdHelperProcess$' -test.v\n"
	if err := writeRootFileAtomic(hostDecommissionBinary, []byte(wrapper), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(operation)
	if err != nil {
		return err
	}
	if err := writeRootFileAtomic(hostDecommissionOperationPath, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return writeRootFileAtomic(hostDecommissionUnit, []byte(hostDecommissionServiceUnit()), 0o644)
}

func writeSystemdIntegrationJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(response).Encode(value)
}

func decommissionIntegrationCleanup() {
	for _, unit := range []string{hostDecommissionUnitName, "vastora-agent.service"} {
		_, _ = exec.Command("systemctl", "disable", "--now", unit).CombinedOutput()
	}
	for _, path := range []string{hostDecommissionEnabledLink, hostDecommissionUnit, hostDecommissionGenerator, systemdDecommissionIntegrationAgentLink, vastoraAgentUnitPath} {
		_ = os.Remove(path)
	}
	_ = os.RemoveAll(hostDecommissionDir)
	_ = os.RemoveAll(systemdDecommissionIntegrationRoot)
	_, _ = exec.Command("systemctl", "daemon-reload").CombinedOutput()
}

func decommissionIntegrationDiagnostics() string {
	var diagnostics strings.Builder
	for _, command := range [][]string{
		{"systemctl", "status", "--no-pager", "-l", hostDecommissionUnitName},
		{"journalctl", "--no-pager", "-u", hostDecommissionUnitName, "-n", "100"},
		{"systemctl", "status", "--no-pager", "-l", "vastora-agent.service"},
	} {
		output, err := exec.Command(command[0], command[1:]...).CombinedOutput()
		fmt.Fprintf(&diagnostics, "$ %s\n%s\nerror: %v\n", strings.Join(command, " "), output, err)
	}
	return diagnostics.String()
}
