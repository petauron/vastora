package center

import (
	"context"
	"encoding/json"
	"github.com/petauron/vastora/internal/landing"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeLandingHealthRequiresFreshMatchingRuntimeEvidence(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name     string
		h        landing.ServerHealth
		received time.Time
		want     bool
	}{
		{"healthy", landing.ServerHealth{Revision: 2, Healthy: true, CheckedAt: now}, now, true},
		{"stopped", landing.ServerHealth{Revision: 2, CheckedAt: now}, now, false},
		{"old revision", landing.ServerHealth{Revision: 1, Healthy: true, CheckedAt: now}, now, false},
		{"old observation", landing.ServerHealth{Revision: 2, Healthy: true, CheckedAt: now.Add(-91 * time.Second)}, now, false},
		{"old receipt", landing.ServerHealth{Revision: 2, Healthy: true, CheckedAt: now}, now.Add(-91 * time.Second), false},
		{"future", landing.ServerHealth{Revision: 2, Healthy: true, CheckedAt: now.Add(time.Minute)}, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.h)
			if got := nativeLandingHealthy(raw, tc.received.Format(time.RFC3339Nano), 2, now); got != tc.want {
				t.Fatalf("healthy=%v want %v", got, tc.want)
			}
		})
	}
	if nativeLandingHealthy([]byte(`{}`), now.Format(time.RFC3339Nano), 2, now) {
		t.Fatal("missing evidence accepted")
	}
}

func TestVersion113LandingHealthMigrationBacksUpAndStartsUnknown(t *testing.T) {
	dir := t.TempDir()
	old := legacyMigrationStore(t, dir, 112)
	if err := old.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var version int
	if err = s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 113 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if _, err = s.db.ExecContext(context.Background(), `SELECT health_json,health_received_at FROM landing_server_states`); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "migration-backups", "*"))
	if err != nil || len(files) == 0 {
		t.Fatalf("missing pre-migration backup: %v", err)
	}
}
