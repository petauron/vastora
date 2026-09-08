package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

type landingLatencySample struct {
	Observation landing.LatencyObservation
	ReceivedAt  time.Time
}

type LandingLatencyView struct {
	NodeID    string   `json:"nodeId"`
	State     string   `json:"state"`
	LatencyMS *float64 `json:"latencyMs,omitempty"`
}

// Targets come only from the selected, ready managed landing server. The
// authenticated caller cannot supply an arbitrary address to another Agent.
func (s *Store) landingLatencyTarget(ctx context.Context, nodeID string) (*landing.LatencyTarget, error) {
	var revision uint64
	var peerJSON []byte
	err := s.db.QueryRowContext(ctx, `SELECT json_extract(selection.value,'$.revision'),server.peer_json
 FROM settings selection
 JOIN landing_server_states server ON server.node_id=json_extract(selection.value,'$.nodeId')
 JOIN agents target ON target.id=server.node_id
 JOIN agents source ON source.id=?
 WHERE selection.key=? AND server.node_id<>source.id AND server.status='ready'
 AND target.status='active' AND target.credential_revoked_at=''
 AND source.status='active' AND source.credential_revoked_at='' AND source.tailscale_ownership='managed'
 AND EXISTS(SELECT 1 FROM applications WHERE node_id=source.id AND app_key='vastora-official/3x-ui' AND runtime='docker')`, nodeID, landingSelectionKey).Scan(&revision, &peerJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var peer landing.PeerIdentity
	if json.Unmarshal(peerJSON, &peer) != nil || peer.ID == "" || peer.PublicKey == "" || (landing.ServerPlan{Revision: 1, Address: peer.Address}).Validate() != nil {
		return nil, nil
	}
	return &landing.LatencyTarget{Revision: revision, Peer: peer}, nil
}

func (s *Store) recordLandingLatency(nodeID string, target *landing.LatencyTarget, observation *landing.LatencyObservation) {
	now := s.now().UTC()
	s.landingLatencyMu.Lock()
	defer s.landingLatencyMu.Unlock()
	for id, sample := range s.landingLatencies {
		if now.Sub(sample.ReceivedAt) > landingHealthFreshness {
			delete(s.landingLatencies, id)
		}
	}
	if target == nil || observation == nil || observation.Target != *target || observation.CheckedAt.Before(now.Add(-landingHealthFreshness)) || observation.CheckedAt.After(now.Add(5*time.Second)) {
		delete(s.landingLatencies, nodeID)
		return
	}
	value := *observation
	if value.State != "direct" {
		value.State, value.LatencyMS = "unavailable", nil
	} else if value.LatencyMS == nil || math.IsNaN(*value.LatencyMS) || math.IsInf(*value.LatencyMS, 0) || *value.LatencyMS <= 0 || *value.LatencyMS > landing.CheckTimeout.Seconds()*1000 {
		value.LatencyMS = nil
	}
	if previous, ok := s.landingLatencies[nodeID]; ok && previous.Observation.Target == *target && !value.CheckedAt.After(previous.Observation.CheckedAt) {
		return
	}
	if s.landingLatencies == nil {
		s.landingLatencies = make(map[string]landingLatencySample)
	}
	s.landingLatencies[nodeID] = landingLatencySample{Observation: value, ReceivedAt: now}
}

func (s *Store) landingLatencyViews(selection LandingSelection) []LandingLatencyView {
	s.landingLatencyMu.Lock()
	defer s.landingLatencyMu.Unlock()
	views := []LandingLatencyView{}
	now := s.now().UTC()
	for id, sample := range s.landingLatencies {
		value := sample.Observation
		if value.Target.Revision != selection.Revision || now.Sub(value.CheckedAt) > landingHealthFreshness || now.Sub(sample.ReceivedAt) > landingHealthFreshness {
			continue
		}
		views = append(views, LandingLatencyView{NodeID: id, State: value.State, LatencyMS: value.LatencyMS})
	}
	return views
}
