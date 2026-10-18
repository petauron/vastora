package center

import (
	"context"
	"database/sql/driver"
	"errors"
	"path/filepath"
	"testing"
)

// Request cancellation or a bad connection can make database/sql replace its
// sole connection. Pool size one does not make connection PRAGMAs persistent.
func replaceStoreConnection(t *testing.T, s *Store) {
	t.Helper()
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestStoreReplacementConnectionsEnforceForeignKeys(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	for i := 0; i < 3; i++ {
		replaceStoreConnection(t, s)
		var enabled int
		if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
			t.Fatalf("replacement %d foreign_keys=%d err=%v", i, enabled, err)
		}
		if _, err := s.db.Exec(`INSERT INTO task_events(id,task_id,agent_id,kind,event,created_at) VALUES('orphan','synthetic-task','missing-agent','test','queued','')`); err == nil {
			t.Fatal("replacement connection accepted orphan")
		}
	}
}

func TestMigrationVerificationRejectsDisabledForeignKeys(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	if _, err := s.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	if err := verifyMigratedSchema(context.Background(), s.db, centerSchemaVersion); err == nil {
		t.Fatal("migration verification accepted disabled enforcement")
	}
}

func TestStoreDatabaseURIHandlesRelativeAndReservedPathCharacters(t *testing.T) {
	t.Chdir(t.TempDir())
	dir := filepath.Join("data # with ?", "center")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES('uri-path-test','preserved')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replaceStoreConnection(t, s)
	var value string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key='uri-path-test'`).Scan(&value); err != nil || value != "preserved" {
		t.Fatalf("opened wrong database: %q %v", value, err)
	}
}
