package center

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestProductBrandMigrationPreservesIdentityAndCustomNames(t *testing.T) {
	for _, test := range []struct{ key, before, after string }{
		{"vastora-official/meridian", "Meridian", "Glivela"},
		{"vastora-official/pulse", "Pulse", "Senulun"},
		{"vastora-official/pulse", "Pulse 监控主机", "Senulun 监控主机"},
		{"vastora-official/pulse-agent", "Pulse Agent", "Senulun Agent"},
		{"vastora-official/pulse-agent", "Pulse 探针", "Senulun 探针"},
		{"vastora-official/meridian", "My Meridian", "My Meridian"},
		{"vastora-official/pulse", "Pulse Lab", "Pulse Lab"},
		{"vastora-official/pulse-agent", "Custom collector", "Custom collector"},
		{"private/pulse", "Pulse", "Pulse"},
	} {
		t.Run(test.key+"/"+test.before, func(t *testing.T) {
			directory := t.TempDir()
			legacy := legacyMigrationStore(t, directory, 109)
			if _, err := legacy.db.Exec(`UPDATE applications SET app_key=?,name=? WHERE id='application-v3'`, test.key, test.before); err != nil {
				t.Fatal(err)
			}
			const identity = `SELECT json_object('id',id,'key',app_key,'node',node_id,'site',site_id,'image',image,'status',status,'runtime',runtime,'generation',runtime_generation,'role',role,'created',created_at,'updated',updated_at) FROM applications WHERE id='application-v3'`
			var before string
			if err := legacy.db.QueryRow(identity).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := legacy.Close(); err != nil {
				t.Fatal(err)
			}
			migrated, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			var after, name string
			if err := migrated.db.QueryRow(identity).Scan(&after); err != nil || before != after {
				t.Fatalf("brand migration changed application identity or runtime state: %v", err)
			}
			if err := migrated.db.QueryRow(`SELECT name FROM applications WHERE id='application-v3'`).Scan(&name); err != nil || name != test.after {
				t.Fatalf("name=%q, want=%q, err=%v", name, test.after, err)
			}
			if err := migrated.Close(); err != nil {
				t.Fatal(err)
			}
			backups, err := filepath.Glob(filepath.Join(directory, "migration-backups", fmt.Sprintf("center-v109-before-v%d-*.db", centerSchemaVersion)))
			if err != nil || len(backups) != 1 {
				t.Fatalf("missing pre-brand migration backup: %v", err)
			}
			backup, err := sql.Open("sqlite", backups[0])
			if err != nil {
				t.Fatal(err)
			}
			defer backup.Close()
			if err := backup.QueryRow(`SELECT name FROM applications WHERE id='application-v3'`).Scan(&name); err != nil || name != test.before {
				t.Fatalf("backup did not preserve original name: %v", err)
			}
			// Once migrated, even an operator choosing an old title is respected.
			reopened, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.db.Exec(`UPDATE applications SET name=? WHERE id='application-v3'`, test.before); err != nil {
				t.Fatal(err)
			}
			reopened.Close()
			reopened, err = Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if err := reopened.db.QueryRow(`SELECT name FROM applications WHERE id='application-v3'`).Scan(&name); err != nil || name != test.before {
				t.Fatalf("brand migration repeated on startup: %v", err)
			}
		})
	}
}

func TestProductBrandMigrationFailureDoesNotAdvanceSchema(t *testing.T) {
	directory := t.TempDir()
	legacy := legacyMigrationStore(t, directory, 109)
	if _, err := legacy.db.Exec(`UPDATE applications SET app_key='vastora-official/meridian',name='Meridian' WHERE id='application-v3';
CREATE TRIGGER reject_brand_update BEFORE UPDATE OF name ON applications BEGIN SELECT RAISE(ABORT,'fixture migration failure'); END`); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	if opened, err := Open(directory); err == nil {
		opened.Close()
		t.Fatal("startup accepted a failed brand migration")
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "center.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if version, err := sqliteSchemaVersion(context.Background(), db); err != nil || version != 109 {
		t.Fatalf("failed migration advanced schema: %d %v", version, err)
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM applications WHERE id='application-v3'`).Scan(&name); err != nil || name != "Meridian" {
		t.Fatal("failed migration changed name", err)
	}
}
