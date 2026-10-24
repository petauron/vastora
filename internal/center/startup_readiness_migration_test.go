package center

import (
	"context"
	"testing"
)

func TestStartupReadinessMigrationDoesNotPromoteOldLease(t *testing.T) {
	s, id, _ := openMeridianRouteHealthReadFixture(t)
	ctx := context.Background()
	before, err := s.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(before.Routes) != 1 {
		t.Fatal("missing initial fixture", err)
	}
	var desired, applied int64
	if err := s.db.QueryRow(`SELECT desired_revision,applied_revision FROM meridian_route_grants WHERE id=?`, id).Scan(&desired, &applied); err != nil {
		t.Fatal(err)
	}
	sql, err := migrationFiles.ReadFile("migrations/00120_startup_readiness.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(string(sql)); err != nil {
		t.Fatal(err)
	}
	after, err := s.MeridianSubscription(ctx, sharedSnapshotTokenB)
	if err != nil || len(after.Routes) != 0 || len(after.Entries) != len(before.Entries) {
		t.Fatal("old snapshot bypassed startup acceptance", err)
	}
	var gotDesired, gotApplied int64
	if err := s.db.QueryRow(`SELECT desired_revision,applied_revision FROM meridian_route_grants WHERE id=?`, id).Scan(&gotDesired, &gotApplied); err != nil || gotDesired != desired || gotApplied != applied {
		t.Fatal("migration changed configuration", err)
	}
	for _, state := range []string{"revoked", "revoking"} {
		if _, err := s.db.Exec(`UPDATE meridian_route_grants SET status=?,last_error='preserve' WHERE id=?`, state, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(string(sql)); err != nil {
			t.Fatal(err)
		}
		var got, message string
		if err := s.db.QueryRow(`SELECT status,last_error FROM meridian_route_grants WHERE id=?`, id).Scan(&got, &message); err != nil || got != state || message != "preserve" {
			t.Fatal("revocation changed", err)
		}
	}
}
