package center

import (
	"testing"
	"time"
)

func TestLandingConnectionRequiresFreshMatchingHealth(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-10 * time.Second).Format(time.RFC3339Nano)
	old := now.Add(-time.Minute).Format(time.RFC3339Nano)
	for _, test := range []struct {
		name                     string
		enabled                  bool
		configuration            string
		revision, healthRevision uint64
		healthy                  bool
		checked, received, want  string
	}{
		{"healthy", true, "ready", 3, 3, true, fresh, fresh, "healthy"},
		{"configuration is not health", true, "ready", 3, 0, false, "", "", "unhealthy"},
		{"old revision", true, "ready", 3, 2, true, fresh, fresh, "unhealthy"},
		{"expired observation", true, "ready", 3, 3, true, old, fresh, "unhealthy"},
		{"expired heartbeat", true, "ready", 3, 3, true, fresh, old, "unhealthy"},
		{"failed check", true, "ready", 3, 3, false, fresh, fresh, "unhealthy"},
		{"pending", true, "applying", 3, 2, true, fresh, fresh, "pending"},
		{"disabled", false, "stopped", 3, 2, true, fresh, fresh, "disabled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := landingConnectionStatus(test.enabled, test.configuration, test.revision, test.healthRevision, test.healthy, test.checked, test.received, now); got != test.want {
				t.Fatalf("got %s want %s", got, test.want)
			}
		})
	}
}
