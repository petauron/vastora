package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/landing"
)

func TestRecoveryTaskAllowlistPreservesIdentityAndNegativeIntents(t *testing.T) {
	scope := controlplane.RecoveryScope{Stage: "application", Applications: []controlplane.RecoveryApplication{{AppKey: cpaKey, ApplicationID: "owned", Reason: "image_unavailable"}}}
	for _, test := range []struct {
		name    string
		scope   controlplane.RecoveryScope
		task    DeploymentTask
		allowed bool
	}{
		{"repair", scope, DeploymentTask{Kind: "application.apply", AppKey: cpaKey, ApplicationID: "owned", Operation: "configure"}, true},
		{"remove", scope, DeploymentTask{Kind: "application.apply", AppKey: cpaKey, ApplicationID: "owned", Operation: "uninstall"}, true},
		{"foreign owner", scope, DeploymentTask{Kind: "application.apply", AppKey: cpaKey, ApplicationID: "foreign", Operation: "uninstall"}, false},
		{"unrelated app", scope, DeploymentTask{Kind: "application.apply", AppKey: keeperKey, ApplicationID: "owned", Operation: "install"}, false},
		{"publication", scope, DeploymentTask{Kind: "gateway.routes.apply"}, false},
		{"ordinary command", scope, DeploymentTask{Kind: "application.command"}, false},
		{"landing off", scope, DeploymentTask{Kind: "landing.proxy.apply", LandingProxyState: &landing.DesiredState{}}, true},
		{"landing on", scope, DeploymentTask{Kind: "landing.proxy.apply", LandingProxyState: &landing.DesiredState{Proxy: &landing.ProxyPlan{}}}, false},
		{"gateway off remains fenced", controlplane.RecoveryScope{Stage: "gateway"}, DeploymentTask{Kind: "gateway.component.apply", Operation: "stopped"}, false},
		{"gateway on", controlplane.RecoveryScope{Stage: "gateway"}, DeploymentTask{Kind: "gateway.component.apply", Operation: "running"}, false},
		{"listener off", controlplane.RecoveryScope{Stage: "listener"}, DeploymentTask{Kind: "node.listener.apply", NodeListenerState: &gateway.NodeListenerState{}}, true},
		{"listener on", controlplane.RecoveryScope{Stage: "listener"}, DeploymentTask{Kind: "node.listener.apply", NodeListenerState: &gateway.NodeListenerState{Listener: gateway.SharedHTTPS{Routes: []gateway.Layer4Route{{ID: "public"}}}}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := recoveryTaskAllowed(test.scope, test.task); got != test.allowed {
				t.Fatalf("allowed=%v, want %v", got, test.allowed)
			}
		})
	}
	// A missing local owner is not a licence to delete arbitrary resources:
	// Center must supply the identity; the typed executor checks resource labels.
	scope.Applications[0].ApplicationID = ""
	if !recoveryTaskAllowed(scope, DeploymentTask{Kind: "application.apply", AppKey: cpaKey, ApplicationID: "center-owned", Operation: "uninstall"}) || recoveryTaskAllowed(scope, DeploymentTask{Kind: "application.apply", AppKey: cpaKey, Operation: "uninstall"}) {
		t.Fatal("missing local ownership boundary changed")
	}
}

type recoveryTestExecutor struct {
	t                         *testing.T
	store                     *Store
	reason                    string
	deployCalls, restoreCalls int
	inRestore                 bool
	repaired                  *DeploymentTask
	deployError               error
}

func (e *recoveryTestExecutor) Restore(ctx context.Context, store *Store) error {
	e.inRestore = true
	defer func() { e.inRestore = false }()
	e.restoreCalls++
	values, err := store.RestorableInstallations(ctx)
	if err != nil {
		return err
	}
	if e.repaired != nil {
		if e.repaired.Operation == "uninstall" {
			if len(values) != 0 {
				e.t.Fatal("completed uninstall did not remove the old restoration snapshot")
			}
		} else if len(values) != 1 || values[0].InstanceID != e.repaired.ID || values[0].ApplicationID != e.repaired.ApplicationID || values[0].Manifest.Version != e.repaired.Manifest.Version || string(values[0].Config) != string(e.repaired.Config) {
			e.t.Fatal("repair did not atomically replace the old restoration snapshot")
		}
		if pending, err := store.PendingTaskCompletion(ctx); err != nil || pending != nil {
			e.t.Fatalf("restore ran before completion delivery: pending=%#v err=%v", pending, err)
		}
		return nil
	}
	if len(values) != 1 || values[0].InstanceID != "old" {
		e.t.Fatal("failed repair removed or changed the old restoration snapshot")
	}
	return recoveryFailure(values[0], e.reason, errors.New("private runtime error with credential material"))
}

func (e *recoveryTestExecutor) Deploy(_ context.Context, task DeploymentTask) (ApplicationTaskResult, error) {
	if e.inRestore {
		e.t.Fatal("restore and repair overlapped")
	}
	if e.store.requireGatewayStartup() == nil {
		e.t.Fatal("repair cleared the startup safety fence")
	}
	e.deployCalls++
	if e.deployError != nil {
		return ApplicationTaskResult{}, e.deployError
	}
	e.repaired = &task
	return ApplicationTaskResult{}, nil
}

func TestStartupRecoverySerializesRepairsAndCompletionReplay(t *testing.T) {
	payload, err := os.ReadFile("../../catalog/catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	value, err := catalog.ParseCatalog(payload)
	if err != nil {
		t.Fatal(err)
	}
	var manifest catalog.AppManifest
	for _, app := range value.Apps {
		if app.ID == "cpa" {
			manifest = app
		}
	}
	if manifest.ID == "" {
		t.Fatal("CPA fixture manifest missing")
	}
	for _, test := range []struct {
		name, operation, reason  string
		failRepair, lostResponse bool
	}{
		{"uninstall incomplete state", "uninstall", "state_incomplete", false, false},
		{"uninstall unavailable image", "uninstall", "image_unavailable", false, false},
		{"uninstall unhealthy app", "uninstall", "health_check_failed", false, false},
		{"configure replaces snapshot", "configure", "state_incomplete", false, false},
		{"upgrade replaces snapshot", "upgrade", "image_unavailable", false, false},
		{"failed repair stays fenced", "configure", "health_check_failed", true, false},
		{"lost completion is replayed first", "upgrade", "image_unavailable", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			directory := t.TempDir()
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			task := DeploymentTask{Kind: "application.apply", ID: "repair", Attempt: 1, AppKey: cpaKey, ApplicationID: "owned", Operation: test.operation, Manifest: manifest, Config: json.RawMessage(`{"timezone":"Asia/Tokyo"}`), Secrets: json.RawMessage(`{}`)}
			executor := &recoveryTestExecutor{t: t, store: store, reason: test.reason}
			if test.failRepair {
				executor.deployError = errors.New("repair health check failed")
			}
			var publicKey []byte
			claims, completions := 0, 0
			wantCompletions := 1
			if test.lostResponse {
				wantCompletions = 2
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/result") {
					completions++
					var result TaskCompletion
					if json.NewDecoder(r.Body).Decode(&result) != nil || (result.Error != "") != test.failRepair {
						t.Error("repair result did not match the executor outcome")
					}
					if completions <= wantCompletions && (executor.restoreCalls != 1 || claims != 1) {
						t.Error("restore or another claim overtook pending completion delivery")
					}
					if test.lostResponse && completions == 1 {
						// Center accepted the result, but its acknowledgement was lost.
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					_, _ = w.Write([]byte(`{}`))
					return
				}
				claims++
				if raw := r.URL.Query().Get("recovery"); raw != "" {
					var scope controlplane.RecoveryScope
					if json.Unmarshal([]byte(raw), &scope) != nil || scope.Stage != "application" || len(scope.Applications) != 1 || scope.Applications[0].Reason != test.reason || strings.Contains(raw, "credential material") {
						t.Error("incorrect or sensitive repair scope")
					}
					if test.failRepair && claims == 2 {
						if store.requireGatewayStartup() == nil || completions != wantCompletions {
							t.Error("failed repair released the startup fence or lost its result")
						}
						_, _ = w.Write([]byte(`{"task":null}`))
						cancel()
						return
					}
					payload, _ := json.Marshal(task)
					envelope, err := controlplane.Seal(publicKey, payload, controlplane.TaskAdditionalData("agent-1", task.ID, task.Attempt))
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"task": map[string]any{"id": task.ID, "attempt": task.Attempt, "envelope": envelope}})
					return
				}
				if test.failRepair || completions != wantCompletions || store.requireGatewayStartup() != nil {
					t.Error("ordinary work was claimed before acknowledged recovery")
				}
				_, _ = w.Write([]byte(`{"task":null}`))
				cancel()
			}))
			defer server.Close()
			connection := testConnection(t, "agent-1", "test", server.URL, "credential")
			publicKey, err = controlplane.PublicKey(connection.PrivateKey)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.SaveConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
			// This is deliberately legacy metadata: uninstall must not depend
			// on having the vanished manifest or cached image.
			if _, err := store.RecordApplied(ctx, AppliedInstallation{InstanceID: "old", ApplicationID: "owned", AppKey: cpaKey, Version: "1", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`)}); err != nil {
				t.Fatal(err)
			}
			client := Client{HTTPClient: server.Client(), Capabilities: Capabilities{Docker: true}, Executor: executor}
			client.RunTasks(ctx, store, func(error) {})
			if executor.deployCalls != 1 || executor.restoreCalls != 2 || claims != 2 || completions != wantCompletions {
				t.Fatalf("deploy=%d restore=%d claims=%d completions=%d", executor.deployCalls, executor.restoreCalls, claims, completions)
			}
			if (store.requireGatewayStartup() != nil) != test.failRepair {
				t.Fatal("startup fence did not match recovery outcome")
			}
			if test.lostResponse {
				// Re-delivering the same task with a new lease replays its durable
				// receipt after a process restart, not its application mutation.
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = Open(directory)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				executor.store = store
				task.Attempt++
				client.processTaskWithLease(context.Background(), store, task, func(err error) { t.Error(err) })
				if executor.deployCalls != 1 || executor.restoreCalls != 2 || completions != wantCompletions+1 {
					t.Fatal("acknowledged repair replayed its side effects")
				}
			}
		})
	}
}

func TestRecoveryWithoutSafeTypedTargetDoesNotClaimTasks(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, test := range []struct {
		name, stage string
		cause       error
	}{
		{"gateway requires local repair", "gateway", errors.New("runtime unavailable")},
		{"unreadable application state", "application", errors.New("cannot decrypt state")},
		{"unknown application identity", "application", recoveryFailure(AppliedInstallation{AppKey: "unknown/app"}, "state_incomplete", errors.New("unknown owner"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			store.setGatewayStartupResult(startupRecoveryError{test.stage, test.cause})
			if store.runtimeRecoveryScope() != nil || store.requireGatewayStartup() == nil {
				t.Fatal("unrepairable recovery produced a claim scope or cleared its fence")
			}
		})
	}
}
