package agent

import (
	"context"
	"testing"
)

func TestLandingControllerUninstallDoesNotTransferJournalOwnership(t *testing.T) {
	store, state, _ := landingSubscriptionTestState(t)
	ctx := context.Background()
	if err := store.saveLandingController(ctx, state); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveApplied(ctx, "unrelated-app"); err != nil {
		t.Fatal(err)
	}
	if current, err := store.landingController(ctx); err != nil || current == nil {
		t.Fatal("unrelated removal erased controller state", err)
	}
	if err := store.RemoveApplied(ctx, threeXUIKey); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM landing_controller_state`).Scan(&count); err != nil || count != 0 {
		t.Fatal("uninstalled controller kept credentials", err)
	}
}
