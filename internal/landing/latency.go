package landing

import "time"

// Latency is observational only: it never authorizes business traffic.
type LatencyTarget struct {
	Revision uint64       `json:"revision"`
	Peer     PeerIdentity `json:"peer"`
}

type LatencyObservation struct {
	Target    LatencyTarget `json:"target"`
	State     string        `json:"state"`
	LatencyMS *float64      `json:"latencyMs,omitempty"`
	CheckedAt time.Time     `json:"checkedAt"`
}
