package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingBlocksControllerMigrationWithHostBoundRoutes(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	peer := landing.PeerIdentity{ID: "peer", PublicKey: "key", Address: "100.64.0.8"}
	route, err := landing.PrepareRouteChange(json.RawMessage(`{"outbounds":[{"tag":"direct","protocol":"freedom"}],"routing":{"rules":[]}}`), 1, []string{"business"}, peer)
	if err != nil {
		t.Fatal(err)
	}
	state := landingRuntimeState{
		Desired:       landing.DesiredState{NodeID: "node-1", Revision: 1, Proxy: &landing.ProxyPlan{ApplicationID: "app-1", InboundTags: []string{"business"}, Peer: peer}},
		ApplicationID: "app-1", ContainerID: "container", Bridge: "br-0123456789ab", RestartPolicy: "no", Route: &route, Phase: "prepared",
	}
	if err := store.saveLandingRuntime(ctx, state); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"backup", "promote", "demote"} {
		_, err := (Client{}).applyThreeXUIControllerCommand(ctx, store, "migration", ThreeXUIControllerCommandTask{ApplicationID: "app-1", Action: action})
		if err == nil || !strings.Contains(err.Error(), "disable landing") {
			t.Fatalf("%s did not stop before controller access: %v", action, err)
		}
	}
	if err := store.checkLandingApplicationMutation(ctx, komariKey); err != nil {
		t.Fatal("landing blocked an unrelated application", err)
	}
}

func TestLandingProxyDisableAndRevisionFenceWithoutDocker(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.SaveConnection(ctx, testConnection(t, "node-1", "node", "https://center.example.com", "credential")); err != nil {
		t.Fatal(err)
	}
	if err := store.applyLandingProxy(ctx, landing.DesiredState{NodeID: "other", Revision: 1}); err == nil {
		t.Fatal("foreign node accepted")
	}
	disabled := landing.DesiredState{NodeID: "node-1", Revision: 2}
	if err := store.applyLandingProxy(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	if err := store.applyLandingProxy(ctx, disabled); err != nil {
		t.Fatal("disable retry failed", err)
	}
	if err := store.applyLandingProxy(ctx, landing.DesiredState{NodeID: "node-1", Revision: 1}); err == nil {
		t.Fatal("stale enable boundary accepted")
	}
	state, err := store.landingRuntime(ctx)
	if err != nil || state == nil || state.Applied == nil || state.Applied.Revision != 2 || state.Route != nil {
		t.Fatal("disable checkpoint not saved")
	}
	if err := store.checkLandingApplicationMutation(ctx, threeXUIKey); err != nil {
		t.Fatal("disabled landing blocked application", err)
	}
}
