package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestInspectInstallOperationFindsPendingFreshInstallWithoutMigration(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if operation, exists, err := InspectInstallOperation(directory); err != nil || exists {
		t.Fatalf("empty installation state: operation=%#v exists=%v err=%v", operation, exists, err)
	}
	started, err := store.BeginEnrollmentOperation(context.Background(), "http://127.0.0.1:8080", "one-time-token", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	operation, exists, err := InspectInstallOperation(directory)
	if err != nil || !exists || operation.ReplaceExisting || operation.Phase != started.Phase {
		t.Fatalf("pending installation state: operation=%#v exists=%v err=%v", operation, exists, err)
	}
}

func TestEnrollmentOperationReusesIdentityAfterLostResponseAndRestart(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	operationID := ""
	publicKey := []byte(nil)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			OperationID string `json:"operationId"`
			PublicKey   []byte `json:"publicKey"`
		}
		if json.NewDecoder(request.Body).Decode(&input) != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		requests++
		if operationID == "" {
			operationID = input.OperationID
			publicKey = append([]byte(nil), input.PublicKey...)
		} else if operationID != input.OperationID || string(publicKey) != string(input.PublicKey) {
			writer.WriteHeader(http.StatusConflict)
			return
		}
		if requests == 1 {
			connection, _, err := writer.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(Enrollment{ID: "agent-1", Credential: "credential", Name: "node", Roles: []string{"worker"}, Capabilities: Capabilities{Docker: true}})
	}))
	defer server.Close()
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	client := Client{HTTPClient: server.Client()}
	if _, err := client.Enroll(context.Background(), store, server.URL, "one-time-token", ""); err == nil {
		t.Fatal("lost response was reported as successful")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	result, err := client.Enroll(context.Background(), store, server.URL, "one-time-token", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "agent-1" || requests != 2 || operationID == "" {
		t.Fatalf("enrollment did not resume: result=%#v requests=%d operation=%q", result, requests, operationID)
	}
}

func TestEnrollmentOperationRetriesLocalConnectionCommit(t *testing.T) {
	operationIDs := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var input struct {
			OperationID string `json:"operationId"`
		}
		_ = json.NewDecoder(request.Body).Decode(&input)
		operationIDs = append(operationIDs, input.OperationID)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(Enrollment{ID: "agent-1", Credential: "credential", Name: "node", Roles: []string{"worker"}, Capabilities: Capabilities{Docker: true}})
	}))
	defer server.Close()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`CREATE TRIGGER fail_connection_insert BEFORE INSERT ON control_plane_connection BEGIN SELECT RAISE(ABORT, 'injected connection failure'); END`); err != nil {
		t.Fatal(err)
	}
	client := Client{HTTPClient: server.Client()}
	if _, err := client.Enroll(context.Background(), store, server.URL, "one-time-token", ""); err == nil {
		t.Fatal("local connection failure was not reported")
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_connection_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Enroll(context.Background(), store, server.URL, "one-time-token", ""); err != nil {
		t.Fatal(err)
	}
	if len(operationIDs) != 2 || operationIDs[0] == "" || operationIDs[0] != operationIDs[1] {
		t.Fatalf("local retry changed operation IDs: %#v", operationIDs)
	}
}
