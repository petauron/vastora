package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/pulse"
)

func TestPulseGenericRecipeCopiesPrivateBootstrap(t *testing.T) {
	task := packageTestTask(t)
	for _, app := range officialContractFixture(t).Apps {
		if app.ID == "pulse" {
			task.Manifest = app
		}
	}
	const token = "test-only-pulse-setup-token-0000000000"
	task.AppKey = pulse.ServiceKey
	task.Config = json.RawMessage(`{"public_url":"https://pulse.example.com"}`)
	task.Secrets = json.RawMessage(`{"setup_token":"` + token + `"}`)
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
	if bytes.Contains(metadata, []byte(token)) || plan.options.Config.User != "65532:65532" {
		t.Fatal("secret leak or invalid user")
	}
	reader := tar.NewReader(bytes.NewReader(plan.files))
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, _ := io.ReadAll(reader)
		if bytes.Equal(contents, []byte(token)) {
			found = true
			if header.Mode != 0600 || header.Uid != 65532 {
				t.Fatal("token permissions")
			}
		}
	}
	if !found {
		t.Fatal("bootstrap token not staged")
	}
}

func TestPulseAuthenticationConfigRejectedBeforeDocker(t *testing.T) {
	task := packageTestTask(t)
	for _, app := range officialContractFixture(t).Apps {
		if app.ID == "pulse" {
			task.Manifest = app
		}
	}
	task.AppKey = pulse.ServiceKey
	task.Config = json.RawMessage(`{"public_url":"https://pulse.example.com"}`)
	task.AuthorizedCapabilities = task.Manifest.Runtime.RequiredCapabilities
	packageTaskDigest(t, &task)
	_, err := (ApplicationExecutor{DockerSocket: "not-a-docker-socket"}).Deploy(context.Background(), task)
	if err == nil || !strings.Contains(err.Error(), "setup_token") {
		t.Fatalf("missing secret reached Docker: %v", err)
	}
}
