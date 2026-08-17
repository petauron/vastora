package agent

import (
	"context"
	"encoding/json"
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
	status, err := store.RecordApplied(context.Background(), AppliedInstallation{
		InstanceID: "cpa-main", AppKey: "official/cpa", Version: "1.0.0",
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
	if string(encoded) == "" || string(encoded) == `[{"instanceId":"cpa-main","appKey":"official/cpa","version":"1.0.0","configHash":"","appliedAt":""}]` {
		t.Fatal("unexpected status serialization")
	}
	secrets, err := store.ReadAppliedSecrets(context.Background(), "cpa-main")
	if err != nil {
		t.Fatal(err)
	}
	if string(secrets) != `{"apiKey":"not-in-status"}` {
		t.Fatalf("got %s", secrets)
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
	if _, err := store.RecordGatewayState(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := store.ClearGatewayState(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GatewayState(ctx); err == nil {
		t.Fatal("removed gateway retained last-known-good state")
	}
}
