package center

import (
	"encoding/json"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func nativeLandingHealthy(raw []byte, received string, revision uint64, now time.Time) bool {
	var h landing.ServerHealth
	at, err := time.Parse(time.RFC3339Nano, received)
	return err == nil && !at.After(now.Add(5*time.Second)) && now.Sub(at) <= landing.ServerHealthFreshness && json.Unmarshal(raw, &h) == nil && h.Healthy && h.Fresh(now, revision)
}
