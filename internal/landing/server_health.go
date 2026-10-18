package landing

import "time"

// ServerHealth is runtime evidence, independent of the configuration receipt.
// It proves the owned native listener/firewall, not end-to-end Internet access.
type ServerHealth struct {
	Revision  uint64    `json:"revision"`
	Healthy   bool      `json:"healthy"`
	CheckedAt time.Time `json:"checkedAt"`
}

const ServerHealthFreshness = 90 * time.Second

func (h ServerHealth) Fresh(now time.Time, revision uint64) bool {
	return h.Revision == revision && revision > 0 && !h.CheckedAt.IsZero() && !h.CheckedAt.After(now.Add(5*time.Second)) && !h.CheckedAt.Before(now.Add(-ServerHealthFreshness))
}
