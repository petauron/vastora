package center

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"time"
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

func (s *Store) landingLatencySnapshot(ctx context.Context) (LandingSelection, []LandingLatencyView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LandingSelection{}, nil, err
	}
	defer tx.Rollback()
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		return selection, nil, err
	}
	return selection, s.landingLatencyViews(selection), nil
}

func (s *Server) handleLandingLatencyEvents(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, errors.New("center: streaming is unavailable"))
		return
	}
	// Renew the request periodically so session revocation/expiry is rechecked
	// by requireAuth. EventSource reconnects with a complete current snapshot.
	ctx, cancel := context.WithTimeout(request.Context(), time.Minute)
	defer cancel()
	expiry := time.NewTicker(time.Second)
	defer expiry.Stop()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	var previous []LandingLatencyView
	var revision uint64
	initial := true
	for {
		changed := s.store.taskChanges.subscribe(landingLatencyWakeKey)
		selection, samples, err := s.store.landingLatencySnapshot(ctx)
		if err != nil {
			s.store.taskChanges.unsubscribe(landingLatencyWakeKey, changed)
			if initial {
				writeError(writer, http.StatusInternalServerError, err)
			}
			return
		}
		event := landingLatencyDelta(selection.Revision, previous, samples, initial || revision != selection.Revision)
		if event.Reset || len(event.Upserts) > 0 || len(event.Removed) > 0 {
			payload, err := json.Marshal(event)
			if err == nil {
				// Keep the deadline longer than the keepalive interval, including
				// HTTP/2 where expiry can close an otherwise idle stream.
				_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(30 * time.Second))
				_, err = fmt.Fprintf(writer, "data: %s\n\n", payload)
			}
			if err != nil {
				s.store.taskChanges.unsubscribe(landingLatencyWakeKey, changed)
				return
			}
			flusher.Flush()
		}
		previous, revision, initial = samples, selection.Revision, false
		select {
		case <-ctx.Done():
			s.store.taskChanges.unsubscribe(landingLatencyWakeKey, changed)
			return
		case <-changed:
		case <-expiry.C:
		case <-keepalive.C:
			_ = http.NewResponseController(writer).SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := fmt.Fprint(writer, ": keepalive\n\n"); err != nil {
				s.store.taskChanges.unsubscribe(landingLatencyWakeKey, changed)
				return
			}
			flusher.Flush()
		}
		s.store.taskChanges.unsubscribe(landingLatencyWakeKey, changed)
	}
}
