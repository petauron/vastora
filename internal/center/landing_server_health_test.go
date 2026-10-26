package center

import (
	"encoding/json"
	"github.com/petauron/vastora/internal/landing"
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
