// Package realitytarget defines the bounded, reviewed REALITY suggestion pool.
// Inclusion is not a security verdict: every chosen address must pass the
// existing node-side certificate, transport and shared-provider checks.
package realitytarget

import "sort"

type Candidate struct {
	TargetHost       string `json:"targetHost"`
	TargetIP         string `json:"targetIp"`
	ServerName       string `json:"serverName"`
	NodeASN          int64  `json:"nodeAsn"`
	TargetASN        int64  `json:"targetAsn"`
	CDNProvider      string `json:"cdnProvider,omitempty"`
	TLS13            bool   `json:"tls13"`
	X25519           bool   `json:"x25519"`
	HTTP2            bool   `json:"http2"`
	CertificateValid bool   `json:"certificateValid"`
	LatencyMillis    int64  `json:"latencyMillis"`
	Samples          int    `json:"samples"`
}

// Sources and review policy are maintained in docs/reality-targets.md. Keep
// this pool small; never turn a suggestion request into an Internet scan.
func Hosts() []string {
	return []string{"www.bing.com", "www.google.com", "www.yahoo.com", "www.amazon.com"}
}

func Rank(values []Candidate) {
	sort.SliceStable(values, func(i, j int) bool {
		a, b := values[i], values[j]
		aSame, bSame := a.NodeASN > 0 && a.NodeASN == a.TargetASN, b.NodeASN > 0 && b.NodeASN == b.TargetASN
		if aSame != bSame {
			return aSame
		}
		if a.Samples != b.Samples {
			return a.Samples > b.Samples
		}
		if a.LatencyMillis != b.LatencyMillis {
			return a.LatencyMillis < b.LatencyMillis
		}
		if a.TargetHost != b.TargetHost {
			return a.TargetHost < b.TargetHost
		}
		return a.TargetIP < b.TargetIP
	})
}
