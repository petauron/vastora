package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/senulun"
)

func TestSenulunGenericRecipeUsesApplicationOwnedSetup(t *testing.T) {
	task := packageTestTask(t)
	for _, app := range officialContractFixture(t).Apps {
		if app.ID == "pulse" {
			task.Manifest = currentSenlunFixture(app)
		}
	}
	task.AppKey = senulun.ServiceKey
	task.Config = json.RawMessage(`{"public_url":"https://senulun.example.com"}`)
	task.Secrets = json.RawMessage(`{}`)
	task.AuthorizedCapabilities = task.Manifest.Runtime.RequiredCapabilities
	packageTaskDigest(t, &task)
	engine := newPackageDocker()
	directory := packageTestDirectory(t)
	backend := &DockerPackageBackend{Docker: engine, StateDirectory: directory}
	receipt := &InstanceResources{ApplicationID: task.ApplicationID}
	if err := validatePackageTask(task); err != nil {
		t.Fatal(err)
	}
	if err := backend.Prepare(context.Background(), task, receipt); err != nil {
		t.Fatal(err)
	}
	if len(backend.plans) != 1 {
		t.Fatal("expected one service")
	}
	plan := backend.plans[0]
	metadata, _ := json.Marshal(plan.options)
	if bytes.Contains(metadata, []byte("SETUP_TOKEN")) || plan.options.Config.User != "65532:65532" {
		t.Fatal("obsolete bootstrap configuration or invalid user")
	}
	if bytes.Contains(plan.files, []byte("setup-token")) {
		t.Fatal("Service installation staged an obsolete bootstrap credential")
	}
}

func TestSenulunRejectsObsoleteBootstrapSecretBeforeDocker(t *testing.T) {
	task := packageTestTask(t)
	for _, app := range officialContractFixture(t).Apps {
		if app.ID == "pulse" {
			task.Manifest = currentSenlunFixture(app)
		}
	}
	task.AppKey = senulun.ServiceKey
	task.Config = json.RawMessage(`{"public_url":"https://senulun.example.com"}`)
	task.Secrets = json.RawMessage(`{"setup_token":"test-test-test-test"}`)
	task.AuthorizedCapabilities = task.Manifest.Runtime.RequiredCapabilities
	packageTaskDigest(t, &task)
	_, err := (ApplicationExecutor{DockerSocket: "not-a-docker-socket"}).Deploy(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "undeclared package secret") {
		t.Fatalf("obsolete secret reached Docker: %v", err)
	}
}
