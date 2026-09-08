package agent

import (
	"context"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

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
