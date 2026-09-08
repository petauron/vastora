package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingRuntimePersistsEncryptedAndRejectsRevisionRewrite(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SaveConnection(ctx, testConnection(t, "node-1", "node", "https://center.example.com", "credential")); err != nil {
		t.Fatal(err)
	}
	state := landingRuntimeState{Phase: "prepared", Desired: landing.DesiredState{
		NodeID: "node-1", Revision: 1, Proxy: &landing.ProxyPlan{
			ApplicationID: "app-1", InboundTags: []string{"business"},
			Peer: landing.PeerIdentity{ID: "peer", PublicKey: "peer-key", Address: "100.64.0.8"},
		},
	}}
	if err := store.saveLandingRuntime(ctx, state); err != nil {
		t.Fatal(err)
	}
	var sealed []byte
	if err := store.db.QueryRow(`SELECT sealed_state FROM landing_runtime_state`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"peer-key", "app-1", "business", "100.64.0.8"} {
		if strings.Contains(string(sealed), value) {
			t.Fatal("private landing plan leaked into storage")
		}
	}
	restored, err := store.landingRuntime(ctx)
	if err != nil || restored == nil || restored.Desired.Proxy.Peer != state.Desired.Proxy.Peer {
		t.Fatal("encrypted landing plan did not round trip")
	}
	state.Phase = "applied"
	state.Applied = &state.Desired
	if err := store.saveLandingRuntime(ctx, state); err != nil {
		t.Fatal(err)
	}
	state.Desired.Proxy.Peer.PublicKey = "changed-peer-key"
	if err := store.saveLandingRuntime(ctx, state); err == nil {
		t.Fatal("replaced peer identity without incrementing revision")
	}
	state.Desired.Revision = 2
	state.Applied = nil
	if err := store.saveLandingRuntime(ctx, state); err != nil {
		t.Fatal(err)
	}
	state.Desired.Revision = 1
	if err := store.saveLandingRuntime(ctx, state); err == nil {
		t.Fatal("accepted stale desired revision")
	}
	if _, err := store.db.Exec(`UPDATE landing_runtime_state SET sealed_state = ?`, []byte("corrupt")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.landingRuntime(ctx); err == nil {
		t.Fatal("accepted a corrupted checkpoint")
	}
}

func TestAgentSchemaV16AddsLandingStateForward(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DROP TABLE landing_runtime_state; PRAGMA user_version = 16`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version, rows int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != agentSchemaVersion {
		t.Fatal("landing forward migration did not advance schema")
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM landing_runtime_state`).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("migration fabricated an applied landing configuration")
	}
}
