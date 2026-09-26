package center

import (
	"context"
	"slices"
	"strings"

	"github.com/petauron/vastora/internal/ipquality"
)

// This read only combines existing diagnostic and connection evidence. It does
// not create grants, authorize private identities, or start a network probe.
func (s *Store) compareIPQuality(ctx context.Context, nodeID, address string, checks []IPQualityView, targets []ipQualityProbeTarget, preferences ipquality.Preferences) ([]ipquality.Comparison, error) {
	view, err := s.Landing(ctx)
	if err != nil {
		return nil, err
	}
	endpoints, err := s.listMeridianEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	grants, err := s.listMeridianRouteGrants(ctx)
	if err != nil {
		return nil, err
	}
	byAddress := map[string]IPQualityView{}
	for _, check := range checks {
		byAddress[ipQualityKey(check.AgentID, check.Address)] = check
	}
	selected := map[string]ipQualityProbeTarget{}
	for _, target := range targets {
		if target.Selected {
			selected[target.AgentID] = target
		}
		if target.AgentID == nodeID && target.native && address == "" {
			address = target.Address
		}
	}
	current, found := byAddress[ipQualityKey(nodeID, address)]
	if !found {
		current.Assessment = ipquality.Assess(nil, "", false, s.now(), preferences)
	}
	entryIDs := map[string]bool{}
	meridianSource := false
	for _, endpoint := range endpoints {
		if endpoint.NodeID == nodeID && endpoint.VLESS {
			meridianSource = true
		}
		if endpoint.NodeID == nodeID && endpoint.VLESS && endpoint.RuntimeHealthy && endpoint.Status == "ready" && endpoint.DesiredRevision == endpoint.AppliedRevision {
			entryIDs[endpoint.ID] = true
		}
	}
	compatible := map[string]bool{}
	for _, grant := range grants {
		if entryIDs[grant.EndpointID] && grant.RuntimeHealthy {
			compatible[grant.EgressNodeID] = true
		}
	}
	// Other supported VLESS entries use the existing per-peer landing health
	// projection, which already expires stale observations on read.
	apps, err := s.db.QueryContext(ctx, `SELECT DISTINCT app.id FROM applications app JOIN services service ON service.application_id=app.id
		WHERE app.node_id=? AND app.status<>'stopped' AND service.status<>'stopped'
		AND service.app_protocol IN ('vless/tcp/reality','meridian/entry')`, nodeID)
	if err != nil {
		return nil, err
	}
	applicationIDs := map[string]bool{}
	for apps.Next() {
		var id string
		if err := apps.Scan(&id); err != nil {
			apps.Close()
			return nil, err
		}
		applicationIDs[id] = true
	}
	err = apps.Err()
	apps.Close()
	if err != nil {
		return nil, err
	}
	sourceEligible := len(entryIDs) > 0 || !meridianSource && len(applicationIDs) > 0
	for _, proxy := range view.Proxies {
		if !applicationIDs[proxy.ApplicationID] || !proxy.Enabled || proxy.Status != "ready" || proxy.Connection != "healthy" {
			continue
		}
		for _, peer := range proxy.Peers {
			if peer.Healthy && peer.State == "healthy" {
				compatible[peer.NodeID] = true
			}
		}
	}
	ready := map[string]bool{}
	settled, err := s.db.QueryContext(ctx, `SELECT node_id FROM landing_server_states WHERE status='ready' AND desired_revision=applied_revision`)
	if err != nil {
		return nil, err
	}
	for settled.Next() {
		var id string
		if err := settled.Scan(&id); err != nil {
			settled.Close()
			return nil, err
		}
		ready[id] = true
	}
	err = settled.Err()
	settled.Close()
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	online := map[string]bool{}
	for _, candidate := range view.Candidates {
		names[candidate.NodeID] = candidate.Name
		online[candidate.NodeID] = true
	}
	for _, server := range view.Servers {
		names[server.NodeID] = server.Name
		ready[server.NodeID] = ready[server.NodeID] && server.Status == "ready"
	}
	values := []ipquality.Comparison{}
	for id, name := range names {
		if id == nodeID {
			continue
		}
		target := selected[id]
		check, exists := byAddress[ipQualityKey(id, target.Address)]
		assessment := check.Assessment
		if !exists {
			assessment = ipquality.Assess(nil, "", false, s.now(), preferences)
		}
		// Configuration eligibility is available before a route exists. The
		// route still needs its own authenticated runtime health observation.
		allowed := sourceEligible && online[nodeID] && online[id] && ready[id] && !view.TasksPaused && !view.ControllerBlocked && !slices.Contains(view.BlockedNodeIDs, id) && !slices.Contains(view.BlockedNodeIDs, nodeID)
		recommended, reason, delta := ipquality.Compare(current.Assessment, assessment, allowed)
		// Score improvement is useful even while connection verification is pending.
		if delta == nil && current.Assessment.Score != nil && assessment.Score != nil {
			difference := assessment.Min - current.Assessment.Max
			delta = &difference
		}
		services := []ipquality.Service{}
		if check.Report != nil {
			services = check.Report.Services
		}
		values = append(values, ipquality.Comparison{NodeID: id, Address: target.Address, Family: target.Family, Name: name, Compatible: allowed, ConnectionVerified: compatible[id], Recommended: recommended, Reason: reason, Delta: delta, Assessment: assessment, Services: services})
	}
	slices.SortStableFunc(values, func(a, b ipquality.Comparison) int {
		rank := func(status string) int {
			switch status {
			case "complete":
				return 0
			case "conservative":
				return 1
			default:
				return 2
			}
		}
		if aRank, bRank := rank(a.Assessment.Status), rank(b.Assessment.Status); aRank != bRank {
			return aRank - bRank
		}
		if (a.Assessment.Score != nil) != (b.Assessment.Score != nil) {
			if a.Assessment.Score != nil {
				return -1
			}
			return 1
		}
		if a.Assessment.Score != nil && b.Assessment.Score != nil && *a.Assessment.Score != *b.Assessment.Score {
			return *b.Assessment.Score - *a.Assessment.Score
		}
		if order := strings.Compare(a.Name, b.Name); order != 0 {
			return order
		}
		return strings.Compare(a.NodeID, b.NodeID)
	})
	return values, nil
}
