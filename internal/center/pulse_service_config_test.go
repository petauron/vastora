package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/pulse"
)

func TestPulseServiceAuthenticationSecretIsEncryptedAndRetainedOnUpgrade(t *testing.T) {
	store, service, _, deployment := pulseFixture(t)
	ctx := context.Background()
	const token = "test-only-pulse-setup-token-0000000000"
	var publicConfig, sealed []byte
	if err := store.db.QueryRow(`SELECT d.config_json,s.sealed FROM deployments d JOIN secrets s ON s.id = d.secret_id WHERE d.id = ?`, deployment.ID).Scan(&publicConfig, &sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicConfig), token) || strings.Contains(string(publicConfig), "setup_token") || strings.Contains(string(sealed), token) {
		t.Fatal("setup token leaked into public configuration or plaintext persistence")
	}
	// The fixture installs the current catalog version. Model an older installed
	// release so this exercises an upgrade rather than same-version rejection.
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET app_version = '0.1.0-alpha.1' WHERE id = ?`, deployment.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: service.ID, AppKey: pulseAppKey, Operation: "upgrade", Config: json.RawMessage(`{"public_url":"https://monitor.example.com"}`)}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, service)
	config, secrets, err := pulse.DecodeServiceConfig(task.Config, task.Secrets)
	if err != nil || config.PublicURL != "https://monitor.example.com" || secrets.SetupToken != token || strings.Contains(string(task.Config), token) {
		t.Fatal("upgrade did not preserve the encrypted setup token separately from the new dashboard origin")
	}
}

func TestPulseServiceRejectsInvalidAuthenticationChange(t *testing.T) {
	store, service, _, _ := pulseFixture(t)
	for _, config := range []string{
		`{"public_url":"http://pulse.example.com"}`,
		`{"public_url":"https://pulse.example.com/login"}`,
		`{"setup_token":""}`,
		`{"setup_token":"too-short"}`,
	} {
		if _, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: service.ID, AppKey: pulseAppKey, Operation: "configure", Config: json.RawMessage(config)}); err == nil {
			t.Fatal("invalid Pulse authentication configuration was accepted")
		}
	}
}
