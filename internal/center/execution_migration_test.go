package center

import (
	"context"
	"testing"
)

func TestExecutionFreshDatabaseDoesNotRequireCutover(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	if paused, err := executionClaimsPaused(context.Background(), store.db); err != nil || paused {
		t.Fatalf("fresh installation unexpectedly paused: %t %v", paused, err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_claim_control_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("fresh installation has migration history: %d %v", count, err)
	}
}
