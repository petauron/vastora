package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/senulun"
)

func TestSenulunServiceOwnsAdministratorSetup(t *testing.T) {
	store, service, _, deployment := senulunFixture(t)
	ctx := context.Background()
	if deployment.OneTimeCredentials != nil || deployment.OneTimeCredentialsAvailable {
		t.Fatal("Center generated credentials for Senulun's own administrator setup")
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
	config, err := senulun.DecodeServiceConfig(task.Config)
	if err != nil || config.PublicURL != "https://monitor.example.com" || string(task.Secrets) != `{}` {
		t.Fatal("upgrade must deliver only the application origin, without a bootstrap token")
	}
}

func TestSenulunServiceRejectsInvalidAuthenticationChange(t *testing.T) {
	store, service, _, _ := senulunFixture(t)
	for _, config := range []string{
		`{"public_url":"http://senulun.example.com"}`,
		`{"public_url":"https://senulun.example.com/login"}`,
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
	if created.OneTimeCredentials != nil || created.OneTimeCredentialsAvailable {
		t.Fatal("legacy origin completion generated an obsolete initialization token")
	}
	task := claimTask(t, store, service)
	config, err := senulun.DecodeServiceConfig(task.Config)
	if err != nil || config.PublicURL != "https://senulun.private.example.com" || string(task.Secrets) != `{}` {
		t.Fatal("legacy Senulun upgrade did not retain its ready HTTPS origin")
	}
	if strings.Contains(string(task.Config), "setup_token") {
		t.Fatal("obsolete setup token reached the current configuration")
	}
}
