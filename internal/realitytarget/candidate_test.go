package realitytarget

import "testing"

func TestRankPrefersKnownSameASNThenStableLatency(t *testing.T) {
	values := []Candidate{
		{TargetHost: "unknown", Samples: 3, LatencyMillis: 1},
		{TargetHost: "same", NodeASN: 64500, TargetASN: 64500, Samples: 3, LatencyMillis: 70},
		{TargetHost: "different", NodeASN: 64500, TargetASN: 64501, Samples: 3, LatencyMillis: 3},
		{TargetHost: "transient", NodeASN: 64500, TargetASN: 64502, Samples: 1, LatencyMillis: 1},
	}
	Rank(values)
	for index, name := range []string{"same", "unknown", "different", "transient"} {
		if values[index].TargetHost != name {
			t.Fatalf("rank %d = %s, want %s", index, values[index].TargetHost, name)
		}
	}
}
