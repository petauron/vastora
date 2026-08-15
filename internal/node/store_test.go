package node

import (
	"context"
	"encoding/json"
	"testing"
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
