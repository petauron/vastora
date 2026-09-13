package center

import (
	"context"
	"testing"
)

func TestNodeExitPolicyDefaultAndInvalidSelection(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := readNodeExitPolicy(ctx, tx, "entry")
	tx.Rollback()
	if err != nil || !policy.OwnExit || policy.Revision != 0 || len(policy.LandingNodeIDs) != 0 {
		t.Fatalf("unexpected default: %+v %v", policy, err)
	}
	for _, input := range []NodeExitInput{
		{OwnExit: false, ConfirmSessionReset: true},
		{OwnExit: true},
		{OwnExit: true, LandingNodeIDs: []string{"a", "a"}, ConfirmSessionReset: true},
	} {
		if err := store.ConfigureNodeExits(ctx, "entry", input); err == nil {
			t.Fatal("invalid policy accepted")
		}
	}
}
