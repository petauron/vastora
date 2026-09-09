package center

import (
	"context"
	"encoding/json"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

type landingLatencySample struct {
	Observation landing.LatencyObservation
	ReceivedAt  time.Time
}
type landingLatencyKey struct{ NodeID, LandingNodeID string }
type LandingLatencyView struct {
	NodeID        string   `json:"nodeId"`
	LandingNodeID string   `json:"landingNodeId"`
	State         string   `json:"state"`
	LatencyMS     *float64 `json:"latencyMs,omitempty"`
}

// Only configured managed peers are advertised, never caller-provided hosts.
// Measurements grant no business access and also run while a proxy is disabled.
func (s *Store) landingLatencyTargets(ctx context.Context, nodeID string) ([]landing.LatencyTarget, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server.node_id,json_extract(selection.value,'$.revision'),server.peer_json
 FROM settings selection JOIN json_each(selection.value,'$.nodeIds') selected
 JOIN landing_server_states server ON server.node_id=selected.value
 JOIN agents target ON target.id=server.node_id JOIN agents source ON source.id=?
 WHERE selection.key=? AND target.id<>source.id AND server.status='ready'
 AND target.status='active' AND target.credential_revoked_at='' AND target.tailscale_ownership='managed' AND target.last_seen_at>?
 AND source.status='active' AND source.credential_revoked_at=''
 AND EXISTS(SELECT 1 FROM applications WHERE node_id=source.id AND app_key='vastora-official/3x-ui' AND runtime='docker')
 ORDER BY server.node_id LIMIT ?`, nodeID, landingSelectionKey, s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano), landing.MaxServers)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []landing.LatencyTarget{}
	for rows.Next() {
		var target landing.LatencyTarget
		var peerJSON []byte
		if err := rows.Scan(&target.NodeID, &target.Revision, &peerJSON); err != nil {
			return nil, err
		}
		if json.Unmarshal(peerJSON, &target.Peer) != nil || target.Peer.ID == "" || target.Peer.PublicKey == "" || (landing.ServerPlan{Revision: 1, Address: target.Peer.Address}).Validate() != nil {
			continue
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *Store) recordLandingLatencies(nodeID string, targets []landing.LatencyTarget, observations []landing.LatencyObservation) {
	now := s.now().UTC()
	allowed := make(map[string]landing.LatencyTarget, len(targets))
	for _, target := range targets {
		allowed[target.NodeID] = target
	}
	s.landingLatencyMu.Lock()
	defer s.landingLatencyMu.Unlock()
	for key, sample := range s.landingLatencies {
		target, ok := allowed[key.LandingNodeID]
		if now.Sub(sample.ReceivedAt) > landingHealthFreshness || key.NodeID == nodeID && (!ok || target != sample.Observation.Target) {
			delete(s.landingLatencies, key)
		}
	}
	if s.landingLatencies == nil {
		s.landingLatencies = make(map[landingLatencyKey]landingLatencySample)
	}
	for _, value := range observations {
		target, ok := allowed[value.Target.NodeID]
		if !ok || value.Target != target || value.CheckedAt.Before(now.Add(-landingHealthFreshness)) || value.CheckedAt.After(now.Add(5*time.Second)) {
			continue
		}
		if value.State != "direct" {
			value.State, value.LatencyMS = "unavailable", nil
		} else if value.LatencyMS == nil || math.IsNaN(*value.LatencyMS) || math.IsInf(*value.LatencyMS, 0) || *value.LatencyMS <= 0 || *value.LatencyMS > landing.CheckTimeout.Seconds()*1000 {
			value.LatencyMS = nil
		}
		key := landingLatencyKey{nodeID, target.NodeID}
		if previous, ok := s.landingLatencies[key]; ok && previous.Observation.Target == target && !value.CheckedAt.After(previous.Observation.CheckedAt) {
			continue
		}
		s.landingLatencies[key] = landingLatencySample{Observation: value, ReceivedAt: now}
	}
}

func (s *Store) landingLatencyViews(selection LandingSelection) []LandingLatencyView {
	s.landingLatencyMu.Lock()
	defer s.landingLatencyMu.Unlock()
	views := []LandingLatencyView{}
	now := s.now().UTC()
	for key, sample := range s.landingLatencies {
		value := sample.Observation
		if value.Target.Revision != selection.Revision || !slices.Contains(selection.NodeIDs, key.LandingNodeID) || now.Sub(value.CheckedAt) > landingHealthFreshness || now.Sub(sample.ReceivedAt) > landingHealthFreshness {
			continue
		}
		views = append(views, LandingLatencyView{NodeID: key.NodeID, LandingNodeID: key.LandingNodeID, State: value.State, LatencyMS: value.LatencyMS})
	}
	slices.SortFunc(views, func(a, b LandingLatencyView) int {
		if order := strings.Compare(a.NodeID, b.NodeID); order != 0 {
			return order
		}
		return strings.Compare(a.LandingNodeID, b.LandingNodeID)
	})
	return views
}
