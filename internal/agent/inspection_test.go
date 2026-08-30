package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectConnectionIsReadOnlyAndExcludesCredential(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveConnection(context.Background(), testConnection(t, "agent-1", "edge", "https://center.example.com", "secret-credential")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(directory, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	summary, err := InspectConnection(directory)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(directory, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	if summary.AgentID != "agent-1" || summary.Name != "edge" || summary.CenterURL != "https://center.example.com" {
		t.Fatalf("connection summary = %#v", summary)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("read-only inspection changed the Agent database: before=%v after=%v", before, after)
	}
}

func TestInspectConnectionDoesNotCreateState(t *testing.T) {
	directory := t.TempDir()
	if _, err := InspectConnection(directory); err == nil {
		t.Fatal("missing Agent state was accepted")
	}
	if _, err := os.Stat(filepath.Join(directory, "agent.db")); !os.IsNotExist(err) {
		t.Fatalf("inspection created Agent state: %v", err)
	}
}
