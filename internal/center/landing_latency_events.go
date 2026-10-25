package center

import (
	"reflect"
)

const landingLatencyWakeKey = "landing:latency"

type LandingLatencyPair struct {
	NodeID        string `json:"nodeId"`
	LandingNodeID string `json:"landingNodeId"`
}

type LandingLatencyEvent struct {
	Revision uint64               `json:"revision"`
	Reset    bool                 `json:"reset"`
	Upserts  []LandingLatencyView `json:"upserts"`
	Removed  []LandingLatencyPair `json:"removed"`
}

func landingLatencyDelta(revision uint64, previous, current []LandingLatencyView, reset bool) LandingLatencyEvent {
	event := LandingLatencyEvent{Revision: revision, Reset: reset, Upserts: []LandingLatencyView{}, Removed: []LandingLatencyPair{}}
	before := map[LandingLatencyPair]LandingLatencyView{}
	if !reset {
		for _, sample := range previous {
			before[LandingLatencyPair{sample.NodeID, sample.LandingNodeID}] = sample
		}
	}
	for _, sample := range current {
		pair := LandingLatencyPair{sample.NodeID, sample.LandingNodeID}
		if old, ok := before[pair]; !ok || !reflect.DeepEqual(old, sample) {
			event.Upserts = append(event.Upserts, sample)
		}
		delete(before, pair)
	}
	// Preserve snapshot order for deterministic events and tests.
	for _, sample := range previous {
		pair := LandingLatencyPair{sample.NodeID, sample.LandingNodeID}
		if _, removed := before[pair]; removed {
			event.Removed = append(event.Removed, pair)
		}
	}
	return event
}
