package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/senulun"
)

func TestSenulunServiceAuthenticationSecretIsEncryptedAndRetainedOnUpgrade(t *testing.T) {
	store, service, _, deployment := senulunFixture(t)
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
	if _, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: service.ID, AppKey: senulunAppKey, Operation: "upgrade", Config: json.RawMessage(`{"public_url":"https://monitor.example.com"}`)}); err != nil {
		t.Fatal(err)
	}
	task := claimTask(t, store, service)
	config, secrets, err := senulun.DecodeServiceConfig(task.Config, task.Secrets)
	if err != nil || config.PublicURL != "https://monitor.example.com" || secrets.SetupToken != token || strings.Contains(string(task.Config), token) {
		t.Fatal("upgrade did not preserve the encrypted setup token separately from the new dashboard origin")
	}
}

func TestSenulunServiceRejectsInvalidAuthenticationChange(t *testing.T) {
	store, service, _, _ := senulunFixture(t)
	for _, config := range []string{
		`{"public_url":"http://senulun.example.com"}`,
		`{"public_url":"https://senulun.example.com/login"}`,
		`{"setup_token":""}`,
		`{"setup_token":"too-short"}`,
	} {
		if _, err := store.CreateDeployment(context.Background(), DeploymentRequest{AgentID: service.ID, AppKey: senulunAppKey, Operation: "configure", Config: json.RawMessage(config)}); err == nil {
			t.Fatal("invalid Senulun authentication configuration was accepted")
		}
	}
}

func TestSenulunUpgradeFillsMissingLegacyHostSettings(t *testing.T) {
	store, service, _, previous := senulunFixture(t)
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `UPDATE deployments SET app_version = '0.1.0-alpha.2', config_json = '{}', secret_id = NULL WHERE id = ?`, previous.ID); err != nil {
		t.Fatal(err)
	}
	addSenulunPrivateFixture(t, store, previous.ApplicationID, service.ID)
	created, err := store.CreateDeployment(ctx, DeploymentRequest{AgentID: service.ID, AppKey: senulunAppKey, Operation: "upgrade", Config: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if created.OneTimeCredentials == nil || len(created.OneTimeCredentials.SetupToken) < 32 || !created.OneTimeCredentialsAvailable {
		t.Fatal("generated Senulun setup token was not delivered once")
	}
	task := claimTask(t, store, service)
	config, secrets, err := senulun.DecodeServiceConfig(task.Config, task.Secrets)
	if err != nil || config.PublicURL != "https://senulun.private.example.com" || secrets.SetupToken != created.OneTimeCredentials.SetupToken {
		t.Fatal("legacy Senulun upgrade did not use its ready HTTPS entry and generated token")
	}
	if strings.Contains(string(task.Config), secrets.SetupToken) {
		t.Fatal("setup token leaked into public deployment configuration")
	}
}
