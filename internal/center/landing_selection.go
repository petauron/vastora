package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

const landingSelectionKey = "meridian_landing_selection"

type LandingSelection struct {
	NodeIDs            []string          `json:"nodeIds"`
	LandingRegionCodes map[string]string `json:"landingRegionCodes,omitempty"`
	RetiringNodeIDs    []string          `json:"retiringNodeIds,omitempty"`
	Revision           uint64            `json:"revision"`
}

type LandingSelectionInput struct {
	NodeIDs            []string          `json:"nodeIds"`
	LandingRegionCodes map[string]string `json:"landingRegionCodes,omitempty"`
	Revision           uint64            `json:"revision"`
}

type LandingCandidate struct {
	NodeID string `json:"nodeId"`
	Name   string `json:"name"`
}

type LandingView struct {
	ControllerBlocked bool `json:"controllerBlocked"`
	LandingSelection
	Status               string               `json:"status"`
	EligibleEntries      int                  `json:"eligibleEntries"`
	ReadyCombinations    int                  `json:"readyCombinations"`
	FailedCombinations   int                  `json:"failedCombinations"`
	WithheldCombinations int                  `json:"withheldCombinations"`
	TasksPaused          bool                 `json:"tasksPaused"`
	BlockedNodeIDs       []string             `json:"blockedNodeIds"`
	Servers              []LandingServerView  `json:"servers"`
	Candidates           []LandingCandidate   `json:"candidates"`
	Proxies              []LandingProxyView   `json:"proxies"`
	Latencies            []LandingLatencyView `json:"latencies"`
}

type LandingServerView struct {
	EgressAddresses      []landing.EgressAddress `json:"egressAddresses"`
	EgressIP             string                  `json:"egressIp"`
	EgressRevision       uint64                  `json:"egressRevision"`
	EgressSupported      bool                    `json:"egressSupported"`
	EgressError          string                  `json:"egressError,omitempty"`
	NodeID               string                  `json:"nodeId"`
	Name                 string                  `json:"name"`
	Status               string                  `json:"status"`
	InUse                bool                    `json:"inUse"`
	EligibleEntries      int                     `json:"eligibleEntries"`
	ReadyCombinations    int                     `json:"readyCombinations"`
	FailedCombinations   int                     `json:"failedCombinations"`
	WithheldCombinations int                     `json:"withheldCombinations"`
}

type LandingProxyView struct {
	ApplicationID string                  `json:"applicationId"`
	LandingNodeID string                  `json:"landingNodeId"`
	Revision      uint64                  `json:"revision"`
	Enabled       bool                    `json:"enabled"`
	Status        string                  `json:"status"`
	Connection    string                  `json:"connection"`
	Applied       *LandingAppliedExit     `json:"applied,omitempty"`
	Peers         []LandingPeerHealthView `json:"peers"`
}

type LandingPeerHealthView struct {
	NodeID    string    `json:"nodeId"`
	Healthy   bool      `json:"healthy"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	CheckedAt time.Time `json:"checkedAt,omitempty"`
}

// Applied is the last acknowledged configuration, not a live health result.
// An empty LandingNodeID denotes the original routing, not necessarily direct.
type LandingAppliedExit struct {
	Revision      uint64 `json:"revision"`
	LandingNodeID string `json:"landingNodeId"`
}

func readLandingSelection(ctx context.Context, tx *sql.Tx) (LandingSelection, error) {
	var encoded string
	err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, landingSelectionKey).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return LandingSelection{NodeIDs: []string{}, LandingRegionCodes: map[string]string{}, RetiringNodeIDs: []string{}}, nil
	}
	if err != nil {
		return LandingSelection{}, err
	}
	var value LandingSelection
	if json.Unmarshal([]byte(encoded), &value) != nil || value.Revision == 0 || value.NodeIDs == nil {
		return value, errors.New("center: invalid landing selection")
	}
	if value.LandingRegionCodes == nil {
		value.LandingRegionCodes = map[string]string{}
	}
	if value.RetiringNodeIDs == nil {
		value.RetiringNodeIDs = []string{}
	}
	if err := validateLandingSelection(value); err != nil {
		return value, err
	}
	return value, nil
}

func validateLandingSelection(value LandingSelection) error {
	active := map[string]bool{}
	for _, nodeID := range value.NodeIDs {
		if strings.TrimSpace(nodeID) == "" || active[nodeID] {
			return errors.New("center: invalid landing selection")
		}
		active[nodeID] = true
	}
	retiring := map[string]bool{}
	for _, nodeID := range value.RetiringNodeIDs {
		if strings.TrimSpace(nodeID) == "" || active[nodeID] || retiring[nodeID] {
			return errors.New("center: invalid landing retirement")
		}
		retiring[nodeID] = true
	}
	for nodeID, raw := range value.LandingRegionCodes {
		code, ok := regionCode(raw)
		if (!active[nodeID] && !retiring[nodeID]) || !ok || code != raw {
			return errors.New("center: invalid landing region")
		}
	}
	return nil
}

func (s *Store) LandingSelection(ctx context.Context) (LandingSelection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LandingSelection{}, err
	}
	defer tx.Rollback()
	return readLandingSelection(ctx, tx)
}

func (s *Store) Landing(ctx context.Context) (LandingView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LandingView{}, err
	}
	defer tx.Rollback()
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		return LandingView{}, err
	}
	view := LandingView{LandingSelection: selection, Servers: []LandingServerView{}, Candidates: []LandingCandidate{}, Proxies: []LandingProxyView{}}
	view.TasksPaused, err = executionClaimsPaused(ctx, tx)
	if err != nil {
		return view, err
	}
	view.BlockedNodeIDs = []string{}
	blockedRows, err := tx.QueryContext(ctx, `SELECT DISTINCT agent_id FROM task_executions WHERE disposition='' AND state<>'succeeded' ORDER BY agent_id`)
	if err != nil {
		return view, err
	}
	for blockedRows.Next() {
		var id string
		if err := blockedRows.Scan(&id); err != nil {
			blockedRows.Close()
			return view, err
		}
		view.BlockedNodeIDs = append(view.BlockedNodeIDs, id)
	}
	err = blockedRows.Err()
	blockedRows.Close()
	if err != nil {
		return view, err
	}
	view.Latencies = s.landingLatencyViews(selection)
	serverIDs := append(slices.Clone(selection.NodeIDs), selection.RetiringNodeIDs...)
	slices.Sort(serverIDs)
	serverIDs = slices.Compact(serverIDs)
	for _, nodeID := range serverIDs {
		server := LandingServerView{NodeID: nodeID}
		var active bool
		var lastSeen string
		var egressAddressesJSON []byte
		if err := tx.QueryRowContext(ctx, `SELECT a.name,s.status,a.status='active' AND a.credential_revoked_at='',a.last_seen_at,
 EXISTS(SELECT 1 FROM json_each(s.desired_json,'$.plan.sources')),
 COALESCE(json_extract(s.desired_json,'$.plan.egressIp'),''),s.desired_revision,COALESCE(json_extract(a.capabilities_json,'$.landingEgressIP'),0)=1,s.last_error,a.landing_egress_addresses_json
 FROM landing_server_states s JOIN agents a ON a.id=s.node_id WHERE s.node_id=?`, nodeID).Scan(&server.Name, &server.Status, &active, &lastSeen, &server.InUse, &server.EgressIP, &server.EgressRevision, &server.EgressSupported, &server.EgressError, &egressAddressesJSON); err != nil {
			return view, err
		}
		if err := json.Unmarshal(egressAddressesJSON, &server.EgressAddresses); err != nil {
			return view, err
		}
		seen, _ := time.Parse(time.RFC3339Nano, lastSeen)
		if !active || s.now().Sub(seen) > 2*time.Minute {
			server.Status = "offline"
			server.EgressAddresses = []landing.EgressAddress{}
		}
		if slices.Contains(selection.RetiringNodeIDs, nodeID) {
			server.Status = "draining"
		}
		if err := s.populateLandingServerCounts(ctx, tx, selection, &server); err != nil {
			return view, err
		}
		server.InUse = server.InUse || server.ReadyCombinations+server.FailedCombinations+server.WithheldCombinations > 0
		view.Servers = append(view.Servers, server)
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,p.headscale_address FROM agents a JOIN agent_network_profiles p ON p.agent_id=a.id
 WHERE a.status='active' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed' AND a.last_seen_at>? ORDER BY a.name,a.id`, s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano))
	if err != nil {
		return view, err
	}
	defer rows.Close()
	for rows.Next() {
		var candidate LandingCandidate
		var address string
		if err := rows.Scan(&candidate.NodeID, &candidate.Name, &address); err != nil {
			return view, err
		}
		if (landing.ServerPlan{Revision: 1, Address: address}).Validate() == nil {
			view.Candidates = append(view.Candidates, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		return view, err
	}
	rows.Close()
	proxies, err := tx.QueryContext(ctx, `SELECT application_id,landing_node_id,desired_revision,json_extract(desired_json,'$.proxy') IS NOT NULL,status,health_revision,health_ok,health_checked_at,health_received_at,applied_revision,applied_landing_node_id,peer_health_json FROM landing_proxy_states ORDER BY application_id`)
	if err != nil {
		return view, err
	}
	defer proxies.Close()
	for proxies.Next() {
		var proxy LandingProxyView
		var healthRevision uint64
		var healthy bool
		var checked, received string
		var appliedRevision uint64
		var appliedNode sql.NullString
		var peerHealthJSON []byte
		if err := proxies.Scan(&proxy.ApplicationID, &proxy.LandingNodeID, &proxy.Revision, &proxy.Enabled, &proxy.Status, &healthRevision, &healthy, &checked, &received, &appliedRevision, &appliedNode, &peerHealthJSON); err != nil {
			return view, err
		}
		var peerHealth []landing.PeerHealth
		if json.Unmarshal(peerHealthJSON, &peerHealth) != nil {
			return view, errors.New("center: invalid landing peer health")
		}
		proxy.Peers = make([]LandingPeerHealthView, 0, len(peerHealth))
		for _, peer := range peerHealth {
			value := LandingPeerHealthView{NodeID: peer.Peer.ID, Healthy: peer.Healthy, State: peer.State, Reason: peer.Reason, CheckedAt: peer.CheckedAt}
			if value.CheckedAt.IsZero() || s.now().Sub(value.CheckedAt) > landingHealthFreshness || value.CheckedAt.After(s.now().Add(5*time.Second)) {
				value.Healthy = false
				value.State = "blocked"
				value.Reason = "runtime_stale"
			}
			proxy.Peers = append(proxy.Peers, value)
		}
		if appliedRevision > 0 && appliedNode.Valid {
			proxy.Applied = &LandingAppliedExit{Revision: appliedRevision, LandingNodeID: appliedNode.String}
		}
		proxy.Connection = landingConnectionStatus(proxy.Enabled, proxy.Status, proxy.Revision, healthRevision, healthy, checked, received, s.now().UTC())
		view.Proxies = append(view.Proxies, proxy)
	}
	if err := proxies.Err(); err != nil {
		return view, err
	}
	proxies.Close()
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_endpoints WHERE status<>'retired' AND vless_enabled=1`).Scan(&view.EligibleEntries); err != nil {
		return view, err
	}
	for _, server := range view.Servers {
		view.ReadyCombinations += server.ReadyCombinations
		view.FailedCombinations += server.FailedCombinations
		view.WithheldCombinations += server.WithheldCombinations
	}
	view.Status = "ready"
	if len(selection.RetiringNodeIDs) > 0 || view.WithheldCombinations > 0 || slices.ContainsFunc(view.Servers, func(server LandingServerView) bool { return server.Status != "ready" }) {
		view.Status = "applying"
	}
	if view.TasksPaused || view.ControllerBlocked || view.FailedCombinations > 0 {
		view.Status = "failed"
	}
	return view, nil
}

// Selection stores only managed node identity. Address resolution and task
// queueing share one transaction so a concurrent profile edit cannot redirect it.
func (s *Store) SelectLanding(ctx context.Context, input LandingSelection) error {
	if len(input.NodeIDs) > landing.MaxServers {
		return errors.New("center: too many landing servers")
	}
	input.NodeIDs = slices.Clone(input.NodeIDs)
	if input.NodeIDs == nil {
		input.NodeIDs = []string{}
	}
	slices.Sort(input.NodeIDs)
	for i, id := range input.NodeIDs {
		if strings.TrimSpace(id) == "" || id != strings.TrimSpace(id) || i > 0 && input.NodeIDs[i-1] == id {
			return errors.New("center: invalid landing server selection")
		}
	}
	for id, value := range input.LandingRegionCodes {
		code, ok := regionCode(value)
		if !ok || !slices.Contains(input.NodeIDs, id) {
			return errors.New("center: invalid landing region")
		}
		input.LandingRegionCodes[id] = code
	}
	if len(input.LandingRegionCodes) != len(input.NodeIDs) {
		return errors.New("center: every landing server requires a region")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if owns, err := meridianOwnsLegacyLanding(ctx, tx); err != nil {
		return err
	} else if owns {
		return errMeridianOwnsLegacyLanding
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errExecutionBlocked
	}
	current, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	if current.Revision != input.Revision {
		return errors.New("center: landing selection changed; refresh and retry")
	}
	if slices.Equal(current.NodeIDs, input.NodeIDs) && equalStringMap(current.LandingRegionCodes, input.LandingRegionCodes) {
		return nil
	}
	for _, nodeID := range input.NodeIDs {
		if slices.Contains(current.RetiringNodeIDs, nodeID) {
			return errors.New("center: wait for the landing server removal to finish before adding it again")
		}
	}
	checkNodes := append(slices.Clone(current.NodeIDs), input.NodeIDs...)
	if controller, controllerNode, controllerErr := runningGlobalThreeXUIController(ctx, tx); controllerErr == nil {
		checkNodes = append(checkNodes, controllerNode)
		inbounds, err := threeXUIClientInbounds(ctx, tx, controller)
		if err != nil {
			return err
		}
		for _, entry := range inbounds {
			if !entry.VLESSDisabled && entry.InboundTag != "" {
				checkNodes = append(checkNodes, entry.NodeID)
			}
		}
	}
	slices.Sort(checkNodes)
	for _, nodeID := range slices.Compact(checkNodes) {
		var blocked bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')`, nodeID).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return errExecutionBlocked
		}
	}
	input.RetiringNodeIDs = slices.Clone(current.RetiringNodeIDs)
	for _, nodeID := range input.NodeIDs {
		if slices.Contains(current.NodeIDs, nodeID) {
			continue
		}
		var address string
		if err := tx.QueryRowContext(ctx, `SELECT p.headscale_address FROM agents a JOIN agent_network_profiles p ON p.agent_id=a.id
 WHERE a.id=? AND a.status='active' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed' AND a.last_seen_at>?`, nodeID, s.now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano)).Scan(&address); err != nil {
			return errors.New("center: select an online managed private-network node")
		}
		plan := &landing.ServerPlan{Revision: 1, Address: address}
		if plan.Validate() != nil {
			return errors.New("center: selected node has no usable private address")
		}
		if err := s.queueLandingServer(ctx, tx, nodeID, plan); err != nil {
			return err
		}
	}
	for _, nodeID := range current.NodeIDs {
		if slices.Contains(input.NodeIDs, nodeID) {
			continue
		}
		if !slices.Contains(input.RetiringNodeIDs, nodeID) {
			input.RetiringNodeIDs = append(input.RetiringNodeIDs, nodeID)
		}
	}
	slices.Sort(input.RetiringNodeIDs)
	for _, nodeID := range input.RetiringNodeIDs {
		if code := current.LandingRegionCodes[nodeID]; code != "" {
			if input.LandingRegionCodes == nil {
				input.LandingRegionCodes = map[string]string{}
			}
			input.LandingRegionCodes[nodeID] = code
		}
	}
	for id := range input.LandingRegionCodes {
		if !slices.Contains(input.NodeIDs, id) && !slices.Contains(input.RetiringNodeIDs, id) {
			delete(input.LandingRegionCodes, id)
		}
	}
	input.Revision++
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, landingSelectionKey, string(encoded)); err != nil {
		return err
	}
	if err := s.reconcileGlobalLandingPool(ctx, tx, !equalStringMap(current.LandingRegionCodes, input.LandingRegionCodes)); err != nil {
		return err
	}
	return tx.Commit()
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func (s *Server) handleLanding(writer http.ResponseWriter, request *http.Request) {
	value, err := s.store.Landing(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, value)
}

func (s *Server) handleSelectLanding(writer http.ResponseWriter, request *http.Request) {
	var input LandingSelectionInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SelectMeridianLanding(request.Context(), LandingSelection{
		NodeIDs: input.NodeIDs, LandingRegionCodes: input.LandingRegionCodes, Revision: input.Revision,
	}); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	s.handleLanding(writer, request)
}
