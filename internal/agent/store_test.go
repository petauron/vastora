package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/gateway"
)

func TestAppliedStatePersistsSecretsLocally(t *testing.T) {
	t.Parallel()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := packageTestTask(t)
	status, err := store.RecordApplied(context.Background(), AppliedInstallation{
		InstanceID: "komari-main", AppKey: task.AppKey, Version: task.Manifest.Version, Manifest: task.Manifest,
		Config: json.RawMessage(`{"timezone":"UTC"}`), Secrets: json.RawMessage(`{"apiKey":"not-in-status"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	states, err := store.ListApplied(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ConfigHash != status.ConfigHash {
		t.Fatalf("got %#v", states)
	}
	encoded, _ := json.Marshal(states)
	if string(encoded) == "" {
		t.Fatal("unexpected status serialization")
	}
	secrets, err := store.ReadAppliedSecrets(context.Background(), "komari-main")
	if err != nil {
		t.Fatal(err)
	}
	if string(secrets) != `{"apiKey":"not-in-status"}` {
		t.Fatalf("got %s", secrets)
	}
	var sealedState []byte
	if err := store.db.QueryRow(`SELECT sealed_state FROM applied_installations WHERE instance_id = ?`, "komari-main").Scan(&sealedState); err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{"UTC", "not-in-status", task.Manifest.ID} {
		if strings.Contains(string(sealedState), privateValue) {
			t.Fatalf("sealed application state leaked %q", privateValue)
		}
	}
	restorable, err := store.RestorableInstallations(context.Background())
	if err != nil || len(restorable) != 1 || restorable[0].Manifest.ID != task.Manifest.ID || string(restorable[0].Secrets) != `{"apiKey":"not-in-status"}` {
		t.Fatalf("restorable state = %#v, err=%v", restorable, err)
	}
}

func TestClearingGatewayStatePreventsStaleRestore(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	state := gateway.DesiredState{Revision: 1, Listeners: []gateway.Listener{{Kind: "headscale", Address: "100.64.0.2", HTTPPort: 80, HTTPSPort: 443}}, Routes: []gateway.Route{{
		ID: "route", Hostname: "cpa.apps.example.test", Protocol: "http", ListenerKind: "headscale",
		Upstreams: []gateway.Upstream{{Address: "100.64.0.10", Port: 3000}},
	}}}
	if _, err := store.RecordGatewayState(ctx, state, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearGatewayState(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GatewayState(ctx); err == nil {
		t.Fatal("removed gateway retained last-known-good state")
	}
}

func TestGatewayCertificatesAreEncryptedAtRest(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	state := gatewayState(1, 3000)
	state.Routes[0].TLSEnabled = true
	certificate := testGatewayCertificate(t, state.Routes[0].Hostname)
	if _, err := store.RecordGatewayState(ctx, state, []gateway.Certificate{certificate}); err != nil {
		t.Fatal(err)
	}
	var desired, sealed []byte
	if err := store.db.QueryRowContext(ctx, `SELECT desired_json, sealed_certificates FROM gateway_applied_state WHERE id = 1`).Scan(&desired, &sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(desired), "PRIVATE KEY") || strings.Contains(string(sealed), "PRIVATE KEY") {
		t.Fatal("gateway private key was stored in plaintext")
	}
	restored, err := store.GatewayState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Certificates) != 1 || restored.Certificates[0].PrivateKeyPEM != certificate.PrivateKeyPEM {
		t.Fatal("encrypted gateway certificate was not restored")
	}
}
