package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRetiredExecutionPreservesRegistrationButExcludesRecovery(t *testing.T) {
	s := openMeridianLandingTestStore(t)
	ctx := context.Background()
	_, err := s.RecordApplied(ctx, AppliedInstallation{InstanceID: "old", ApplicationID: "old-app", AppKey: meridianKey, Version: "test", Config: json.RawMessage(`{"preserved":true}`), Secrets: json.RawMessage(`{"credential":"synthetic"}`)})
	if err != nil {
		t.Fatal(err)
	}
	p := PackageExecutor{StateDirectory: s.dataDir}
	r := &InstanceResources{Version: 1, ApplicationID: "old-app", AppKey: meridianKey, Runtime: "docker", State: "ready", IntegrationState: "execution-retired"}
	if err := p.save(r); err != nil {
		t.Fatal(err)
	}
	retired, err := (ApplicationExecutor{Store: s}).LegacyMeridianObservationRetired(ctx)
	if err != nil || !retired {
		t.Fatalf("retirement=%v err=%v", retired, err)
	}
	all, err := s.ListApplied(ctx)
	if err != nil || len(all) != 1 {
		t.Fatal("historical registration lost", err)
	}
	old, err := s.AppliedInstallation(ctx, meridianKey)
	if err != nil || string(old.Config) != `{"preserved":true}` || string(old.Secrets) != `{"credential":"synthetic"}` {
		t.Fatal("configuration changed", err)
	}
	restore, err := s.RestorableInstallations(ctx)
	if err != nil || len(restore) != 0 {
		t.Fatal("retired installation remained executable", err)
	}
	r.IntegrationState = "active"
	if err := p.save(r); err != nil {
		t.Fatal(err)
	}
	restore, err = s.RestorableInstallations(ctx)
	if err != nil || len(restore) != 1 {
		t.Fatal("ordinary installation affected", err)
	}
}

func TestRetiredExecutionRejectsPackageMutation(t *testing.T) {
	for _, operation := range []string{"install", "upgrade", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			task := packageTestTask(t)
			task.Operation = operation
			e := PackageExecutor{StateDirectory: packageTestDirectory(t), Backend: &fakePackageBackend{}}
			r := &InstanceResources{Version: 1, ApplicationID: task.ApplicationID, AppKey: task.AppKey, Runtime: "docker", State: "ready", IntegrationState: "execution-retired"}
			if err := e.save(r); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Deploy(context.Background(), task); err == nil || !strings.Contains(err.Error(), "execution registration is retired") {
				t.Fatalf("retired mutation: %v", err)
			}
		})
	}
}

func TestExecutionRetirementRequiresExplicitMatchingSource(t *testing.T) {
	s := openMeridianLandingTestStore(t)
	if _, err := (ApplicationExecutor{Store: s}).RetireLegacyExecution(context.Background(), "wrong", "/var/lib/example"); err == nil {
		t.Fatal("missing source accepted")
	}
}
