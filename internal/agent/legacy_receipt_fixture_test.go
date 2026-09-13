package agent

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/secret"
)

// Seed historical bytes directly. Tests must not retain the removed production
// receipt writer/replay implementation merely to manufacture migration data.
func seedLegacyReceipt(t *testing.T, store *Store, task DeploymentTask, state string, completion json.RawMessage) {
	t.Helper()
	encoded, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	var sealed []byte
	if len(completion) > 0 {
		sealed, err = secret.Seal(store.key, completion, legacyTaskCompletionContext(task.ID))
		if err != nil {
			t.Fatal(err)
		}
	}
	generation := 0
	if task.Kind == "application.apply" {
		generation = platform.ApplicationRuntimeGeneration
	}
	now := store.now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`INSERT INTO task_receipts(task_id, task_kind, runtime_generation, attempt, task_hash, state, sealed_completion, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, task.ID, task.Kind, generation, task.Attempt, digest[:], state, sealed, now, now); err != nil {
		t.Fatal(err)
	}
}
