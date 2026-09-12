package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingReplacementJournalRetainsRecoverableGateOwnership(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SaveConnection(ctx, testConnection(t, "node-1", "node", "https://center.example.com", "credential")); err != nil {
		t.Fatal(err)
	}
	previous := landing.DesiredState{NodeID: "node-1", Revision: 1, Proxy: &landing.ProxyPlan{ApplicationID: "app-1", InboundTags: []string{"business"}, Peer: landing.PeerIdentity{ID: "peer-a", PublicKey: "key-a", Address: "100.64.0.8"}}}
	next := landing.DesiredState{NodeID: "node-1", Revision: 2, Proxy: &landing.ProxyPlan{ApplicationID: "app-1", InboundTags: []string{"business"}, Peer: landing.PeerIdentity{ID: "peer-b", PublicKey: "key-b", Address: "100.64.0.9"}}}
	raw := json.RawMessage(`{"outbounds":[{"tag":"direct","protocol":"freedom"}],"routing":{"rules":[]}}`)
	first, err := landing.PrepareRouteChange(raw, 1, previous.Proxy.InboundTags, previous.Proxy.Peer)
	if err != nil {
		t.Fatal(err)
	}
	change, err := landing.PrepareRouteReplacement(first, first.After, 2, next.Proxy.InboundTags, next.Proxy.Peer)
	if err != nil {
		t.Fatal(err)
	}
	state := landingRuntimeState{Desired: next, Applied: &previous, Retiring: &previous, ApplicationID: "app-1", ContainerID: "owned-container", Bridge: "br-owned", RestartPolicy: "unless-stopped", Route: &change, Phase: "prepared"}
	if err := store.saveLandingRuntime(ctx, state); err != nil {
		t.Fatal(err)
	}
	restored, err := store.landingRuntime(ctx)
	if err != nil || restored == nil || restored.Retiring == nil || restored.Retiring.Proxy.Peer != previous.Proxy.Peer {
		t.Fatal("crash recovery lost previous gate ownership")
	}
	// Explicit restoration during an interrupted switch still knows both gates.
	state.Desired = landing.DesiredState{NodeID: "node-1", Revision: 3}
	state.Applied = &next
	state.Phase = "restoring"
	if err := store.saveLandingRuntime(ctx, state); err != nil {
		t.Fatal(err)
	}
	invalid := state
	invalid.Retiring = &next
	if err := invalid.validate(); err == nil {
		t.Fatal("current gate accepted as the retiring gate")
	}
}

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
	if _, err := store.db.Exec(`DROP TABLE landing_controller_state; DROP TABLE landing_runtime_state; PRAGMA user_version = 16`); err != nil {
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
