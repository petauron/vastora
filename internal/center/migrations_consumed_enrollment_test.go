package center

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestVersion107ConsumedEnrollmentReference(t *testing.T) {
	for _, consumed := range []bool{true, false} {
		t.Run(map[bool]string{true: "consumed", false: "unused"}[consumed], func(t *testing.T) {
			dir := t.TempDir()
			previous := legacyMigrationStore(t, dir, 106)
			if _, err := previous.db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			var used any
			if consumed {
				used = "2026-01-01T00:00:00Z"
			}
			_, err := previous.db.Exec(`INSERT INTO agent_enrollment_tokens(token_hash,site_id,name,center_url,roles_json,capabilities_json,bootstrap_secret_id,expires_at,used_at) VALUES(X'1234','site-v3','fixture','https://center.example.test','[]','{}','missing-bootstrap','2026-01-01T00:15:00Z',?)`, used)
			if err != nil {
				t.Fatal(err)
			}
			previous.Close()
			upgraded, err := Open(dir)
			if !consumed {
				if err == nil {
					upgraded.Close()
					t.Fatal("unused missing credential accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var ref sql.NullString
				if err = upgraded.db.QueryRow(`SELECT bootstrap_secret_id FROM agent_enrollment_tokens WHERE token_hash=X'1234'`).Scan(&ref); err != nil || ref.Valid {
					t.Fatalf("consumed reference not repaired: %v %v", ref, err)
				}
				upgraded.Close()
				reopened, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				reopened.Close()
			}
			backups, err := filepath.Glob(filepath.Join(dir, "migration-backups", fmt.Sprintf("center-v106-before-v%d-*.db", centerSchemaVersion)))
			if err != nil || len(backups) != 1 {
				t.Fatalf("missing backup: %v", err)
			}
		})
	}
}
