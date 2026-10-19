package center

import (
	"time"
)

const landingHealthFreshness = 45 * time.Second

func landingConnectionStatus(enabled bool, configuration string, revision, healthRevision uint64, healthy bool, checked, received string, now time.Time) string {
	if !enabled && configuration == "stopped" {
		return "disabled"
	}
	if configuration == "pending" || configuration == "applying" {
		return "pending"
	}
	if !enabled || configuration != "ready" || revision != healthRevision || !healthy {
		return "unhealthy"
	}
	checkedAt, err := time.Parse(time.RFC3339Nano, checked)
	if err != nil {
		return "unhealthy"
	}
	receivedAt, err := time.Parse(time.RFC3339Nano, received)
	if err != nil {
		return "unhealthy"
	}
	if now.Sub(checkedAt) > landingHealthFreshness || now.Sub(receivedAt) > landingHealthFreshness || checkedAt.After(now.Add(5*time.Second)) || receivedAt.After(now.Add(5*time.Second)) {
		return "unhealthy"
	}
	return "healthy"
}
