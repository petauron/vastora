package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestXrayConfigurationRestartFailureDoesNotRollback(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	previous := testXrayWorkerState()
	active, err := store.writeXrayWorkerConfig(previous)
	if err != nil {
		t.Fatal(err)
	}
	candidate := cloneXrayWorkerState(previous)
	candidate.Revision++
	candidate.XraySetting = json.RawMessage(`{"outbounds":[{"protocol":"freedom","tag":"changed"}]}`)
	want, err := renderXrayWorkerConfig(candidate)
	if err != nil {
		t.Fatal(err)
	}
	var restarts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("API-Version", "1.55")
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/containers/"+xrayWorkerContainer+"/json"):
			labels := applicationResourceLabels(threeXUIKey, "xray", previous.ApplicationID, "deployment")
			labels[xrayWorkerRuntimeLabel] = "xray"
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "worker", "Config": map[string]any{"Image": previous.ImageReference, "Labels": labels}})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"validator"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/validator/wait"):
			_, _ = w.Write([]byte(`{"StatusCode":0}`))
		case strings.HasSuffix(r.URL.Path, "/containers/validator/start"), r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/validator"):
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/containers/worker/restart"):
			restarts.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"restart response lost"}`))
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	err = dockerXrayWorkerApply(store, "tcp://"+server.Listener.Addr().String())(context.Background(), previous, candidate)
	if err == nil || !taskOutcomeIsUncertain(err) || !strings.Contains(err.Error(), "restart response lost") {
		t.Fatalf("restart failure was not preserved: %v", err)
	}
	got, readErr := os.ReadFile(active)
	if readErr != nil || !bytes.Equal(got, want) || restarts.Load() != 1 {
		t.Fatalf("failed restart rewrote configuration or restarted again: restarts=%d err=%v", restarts.Load(), readErr)
	}
	if store.xrayWorkerAppliedReceiptMatches(candidate) {
		t.Fatal("failed restart recorded the candidate as applied")
	}
}

func TestXrayReplacementStopsAtFirstFailure(t *testing.T) {
	for _, runtime := range []string{"legacy", "xray", "meridian"} {
		t.Run(runtime, func(t *testing.T) {
			current, candidate, backup, cleanup := xrayWorkerContainer, xrayWorkerCandidateContainer, xrayWorkerBackupContainer, xrayWorkerCleanupContainer
			if runtime == "meridian" {
				current, candidate, backup, cleanup = meridianXrayContainer, meridianXrayCandidateContainer, meridianXrayBackupContainer, meridianXrayCleanupContainer
			}
			sequence := []string{"create:" + candidate, "stop:old", "rename:old:" + backup, "start:candidate-id", "rename:candidate-id:" + current, "rename:old:" + cleanup, "remove:old"}
			for _, test := range []struct {
				name  string
				steps int
			}{
				{"create", 1}, {"before-stop", 1}, {"cancel-before-stop", 1},
				{"stop", 2}, {"stop-response-lost", 2}, {"backup", 3}, {"backup-response-lost", 3},
				{"start", 4}, {"validate", 4}, {"cancel-validate", 4},
				{"promote", 5}, {"promote-response-lost", 5}, {"verify", 5}, {"cancel-verify", 5},
				{"cleanup", 6}, {"remove", 7}, {"success", 7},
			} {
				t.Run(test.name, func(t *testing.T) {
					engine := newFakeThreeXUIContainerEngine(t, true)
					options := xrayWorkerTestCreateOptions("deployment-2")
					options.Name = candidate
					if runtime != "legacy" {
						delete(engine.names, threeXUIContainer)
						engine.names[current] = "old"
						engine.containers["old"].name = current
						engine.containers["old"].labels = maps.Clone(options.Config.Labels)
					}
					if runtime == "meridian" {
						options.Config.Labels[applicationIdentityLabel] = meridianKey
						engine.containers["old"].labels[applicationIdentityLabel] = meridianKey
					}
					switch test.name {
					case "create":
						engine.failCreate = true
					case "stop":
						engine.failStopName = engine.containers["old"].name
					case "stop-response-lost":
						engine.failStopAfter = engine.containers["old"].name
					case "backup":
						engine.failRenameName = backup
					case "backup-response-lost":
						engine.failRenameAfterName = backup
					case "start":
						engine.failStartName = candidate
					case "promote":
						engine.failRenameName = current
					case "promote-response-lost":
						engine.failRenameAfterName = current
					case "cleanup":
						engine.failRenameName = cleanup
					case "remove":
						engine.failRemoveName = cleanup
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					phase := func(name string) error {
						if test.name == name {
							return errors.New("injected " + name)
						}
						if test.name == "cancel-"+name {
							cancel()
						}
						return nil
					}
					result, err := replaceXrayWorkerContainer(ctx, engine, options, func() error { return phase("before-stop") }, func(string) (string, error) {
						return "retained-result", phase("validate")
					}, func(string, string) error { return phase("verify") })
					if !slices.Equal(engine.mutations, sequence[:test.steps]) {
						t.Fatalf("unexpected changes after %s: got %v want %v", test.name, engine.mutations, sequence[:test.steps])
					}
					if test.name == "success" {
						if err != nil || result != "retained-result" || len(engine.containers) != 1 || !engine.containers["candidate-id"].running {
							t.Fatalf("successful replacement changed: result=%q err=%v", result, err)
						}
						return
					}
					if err == nil || !taskOutcomeIsUncertain(err) {
						t.Fatalf("failure was not retained as uncertain: %v", err)
					}
					if test.steps >= 4 && test.name != "start" && result != "retained-result" {
						t.Fatal("partial result was lost")
					}
					if test.name != "create" && len(engine.containers) != 2 {
						t.Fatal("failed replacement deleted recovery evidence")
					}
					// A new call must not retry or discard an interrupted candidate.
					if test.name != "create" {
						before := slices.Clone(engine.mutations)
						_, retryErr := replaceXrayWorkerContainer(context.Background(), engine, options, nil, nil, nil)
						if retryErr == nil || !slices.Equal(before, engine.mutations) {
							t.Fatal("unfinished replacement was automatically replayed")
						}
					}
				})
			}
		})
	}
}

func TestRuntimeRestartKeepsUncertainRevisionBlocked(t *testing.T) {
	for _, mode := range []string{"pending", "handover", "retiring-gates", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			store := openMeridianLandingTestStore(t)
			ctx := context.Background()
			state := meridianLandingFixture()
			key, id := meridianKey, state.ApplicationID
			legacy := testXrayWorkerState()
			if mode == "legacy" {
				key, id = threeXUIKey, legacy.ApplicationID
				legacy.Revision++
				if err := store.saveXrayWorkerState(ctx, legacy); err != nil {
					t.Fatal(err)
				}
			} else {
				switch mode {
				case "pending":
					pending := meridianRecoveryArtifact(8, `{"outbounds":[]}`)
					state.Pending = &pending
				case "handover":
					state.HandoverPending = true
				case "retiring-gates":
					state.RetiringGates = state.knownLandingGates()
				}
				if err := store.saveMeridianRuntimeState(ctx, state); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.RecordApplied(ctx, AppliedInstallation{InstanceID: "deployment", ApplicationID: id, AppKey: key, Version: "0.1.0", ApplicationRole: "worker", Config: []byte(`{}`), Secrets: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				directory := store.dataDir
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				var err error
				store, err = Open(directory)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				// An invalid socket proves startup stops before any Docker calls.
				if err := store.ResumeXrayWorker(ctx, "not-a-docker-socket"); err == nil || !strings.Contains(err.Error(), "requires explicit recovery") {
					t.Fatalf("startup bypassed recovery fence: %v", err)
				}
			}
			if mode == "legacy" {
				got, err := store.loadXrayWorkerState(ctx)
				if err != nil || got.Revision != legacy.Revision || got.AppliedRevision != legacy.AppliedRevision {
					t.Fatal("startup advanced legacy revision")
				}
			} else {
				got, err := store.loadMeridianRuntimeState(ctx)
				if err != nil || !reflect.DeepEqual(got, state) {
					t.Fatalf("startup changed pending evidence: %v", err)
				}
			}
			_, recovery := store.runtimeRecovery()
			if len(recovery) != 1 || recovery[0].ApplicationID != id || recovery[0].Reason != "state_incomplete" {
				t.Fatalf("missing operator recovery state: %+v", recovery)
			}
		})
	}
}
