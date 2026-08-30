package center

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestAgentEnrollmentOperationReplaysAcrossRestartAndRejectsAnotherOperation(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "replay-node", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	publicKey := testAgentPublicKey(t)
	operationID := "install-operation-replay-1"
	first, err := store.EnrollAgentOperation(context.Background(), enrollment.Token, operationID, "test", "linux", "amd64", publicKey)
	if err != nil {
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
	replayed, err := store.EnrollAgentOperation(context.Background(), enrollment.Token, operationID, "test", "linux", "amd64", publicKey)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, replayed) {
		t.Fatalf("replayed enrollment changed: first=%#v replayed=%#v", first, replayed)
	}
	if _, err := store.EnrollAgentOperation(context.Background(), enrollment.Token, "different-operation-2", "test", "linux", "amd64", publicKey); err == nil || !strings.Contains(err.Error(), "token is invalid") {
		t.Fatalf("another operation reused the token: %v", err)
	}
	if err := store.ValidateAgentEnrollment(context.Background(), enrollment.Token); err != nil {
		t.Fatalf("recovery bootstrap was unavailable before the first heartbeat: %v", err)
	}
	if err := store.RecordAgentHeartbeat(context.Background(), first.ID, first.Credential, NodeHeartbeat{
		Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ValidateAgentEnrollment(context.Background(), enrollment.Token); err != nil {
		t.Fatalf("recovery bootstrap was removed before host installation converged: %v", err)
	}
	var agents int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE name = 'replay-node'`).Scan(&agents); err != nil || agents != 1 {
		t.Fatalf("replay created %d Agents: %v", agents, err)
	}
}

func TestConcurrentAgentEnrollmentOperationConverges(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	enrollment, err := store.CreateAgentEnrollment(context.Background(), AgentEnrollmentSpec{SiteID: testSiteID(t, store), Name: "concurrent-node", CenterURL: "https://center.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	publicKey := testAgentPublicKey(t)
	results := make(chan AgentCredential, 2)
	errorsChannel := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			ready.Wait()
			result, err := store.EnrollAgentOperation(context.Background(), enrollment.Token, "concurrent-operation-1", "test", "linux", "arm64", publicKey)
			results <- result
			errorsChannel <- err
		}()
	}
	first, second := <-results, <-results
	for range 2 {
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
	}
	if first.ID == "" || !reflect.DeepEqual(first, second) {
		t.Fatalf("concurrent enrollment diverged: first=%#v second=%#v", first, second)
	}
}
