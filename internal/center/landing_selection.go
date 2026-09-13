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

const landingSelectionKey = "three_x_ui_landing_selection"

type LandingSelection struct {
	NodeIDs  []string `json:"nodeIds"`
	Revision uint64   `json:"revision"`
}

type LandingCandidate struct {
	NodeID string `json:"nodeId"`
	Name   string `json:"name"`
}

type LandingView struct {
	NodeExits []NodeExitPolicy `json:"nodeExits"`
	LandingSelection
	TasksPaused    bool                 `json:"tasksPaused"`
	BlockedNodeIDs []string             `json:"blockedNodeIds"`
	Servers        []LandingServerView  `json:"servers"`
	Candidates     []LandingCandidate   `json:"candidates"`
	Proxies        []LandingProxyView   `json:"proxies"`
	Latencies      []LandingLatencyView `json:"latencies"`
}

type LandingServerView struct {
	NodeID string `json:"nodeId"`
	Name   string `json:"name"`
	Status string `json:"status"`
	InUse  bool   `json:"inUse"`
}

type LandingProxyView struct {
	ApplicationID string              `json:"applicationId"`
	LandingNodeID string              `json:"landingNodeId"`
	Revision      uint64              `json:"revision"`
	Enabled       bool                `json:"enabled"`
	Status        string              `json:"status"`
	Connection    string              `json:"connection"`
	Applied       *LandingAppliedExit `json:"applied,omitempty"`
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
		return LandingSelection{NodeIDs: []string{}}, nil
	}
	if err != nil {
		return LandingSelection{}, err
	}
	var value LandingSelection
	if json.Unmarshal([]byte(encoded), &value) != nil || value.Revision == 0 || value.NodeIDs == nil {
		return value, errors.New("center: invalid landing selection")
	}
	return value, nil
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
	for _, nodeID := range selection.NodeIDs {
		server := LandingServerView{NodeID: nodeID}
		var active bool
		var lastSeen string
		if err := tx.QueryRowContext(ctx, `SELECT a.name,s.status,a.status='active' AND a.credential_revoked_at='',a.last_seen_at,
 EXISTS(SELECT 1 FROM json_each(s.desired_json,'$.plan.sources'))
 FROM landing_server_states s JOIN agents a ON a.id=s.node_id WHERE s.node_id=?`, nodeID).Scan(&server.Name, &server.Status, &active, &lastSeen, &server.InUse); err != nil {
			return view, err
		}
		seen, _ := time.Parse(time.RFC3339Nano, lastSeen)
		if !active || s.now().Sub(seen) > 2*time.Minute {
			server.Status = "offline"
		}
		var configured bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM settings p JOIN applications app ON p.key='node-exits:'||app.id JOIN json_each(p.value,'$.landingNodeIds') target WHERE target.value=?)`, nodeID).Scan(&configured); err != nil {
			return view, err
		}
		server.InUse = server.InUse || configured
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
	proxies, err := tx.QueryContext(ctx, `SELECT application_id,landing_node_id,desired_revision,json_extract(desired_json,'$.proxy') IS NOT NULL,status,health_revision,health_ok,health_checked_at,health_received_at,applied_revision,applied_landing_node_id FROM landing_proxy_states ORDER BY application_id`)
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
		if err := proxies.Scan(&proxy.ApplicationID, &proxy.LandingNodeID, &proxy.Revision, &proxy.Enabled, &proxy.Status, &healthRevision, &healthy, &checked, &received, &appliedRevision, &appliedNode); err != nil {
			return view, err
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
	view.NodeExits = []NodeExitPolicy{}
	controller, _, err := runningGlobalThreeXUIController(ctx, tx)
	if err == nil {
		inbounds, err := threeXUIClientInbounds(ctx, tx, controller)
		if err != nil {
			return view, err
		}
		for _, entry := range inbounds {
			p, err := readNodeExitPolicy(ctx, tx, entry.ApplicationID)
			if err != nil {
				return view, err
			}
			p.RequiresOwnExit = entry.HY2InboundID != 0
			if p.Revision > 0 {
				p.Status = "saved"
				var failed, pending bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM landing_client_grants WHERE application_id=? AND status IN ('failed','paused')) OR EXISTS(SELECT 1 FROM settings WHERE key=?), EXISTS(SELECT 1 FROM landing_client_grants WHERE application_id=? AND status NOT IN ('ready','revoked','failed','paused'))`, entry.ApplicationID, "node-exits-error:"+entry.ApplicationID, entry.ApplicationID).Scan(&failed, &pending); err != nil {
					return view, err
				}
				if failed {
					p.Status = "failed"
				} else if pending {
					p.Status = "applying"
				}
			}
			for _, proxy := range view.Proxies {
				if proxy.ApplicationID == entry.ApplicationID && p.Revision > 0 {
					if proxy.Status == "failed" {
						p.Status = "failed"
					} else if (proxy.Status == "pending" || proxy.Status == "applying") && p.Status != "failed" {
						p.Status = "applying"
					}
				}
			}
			view.NodeExits = append(view.NodeExits, p)
		}
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	if current.Revision != input.Revision {
		return errors.New("center: landing selection changed; refresh and retry")
	}
	if slices.Equal(current.NodeIDs, input.NodeIDs) {
		return nil
	}
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
		var configured bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM settings p JOIN applications app ON p.key='node-exits:'||app.id JOIN json_each(p.value,'$.landingNodeIds') target WHERE target.value=?)`, nodeID).Scan(&configured); err != nil {
			return err
		}
		if configured {
			return errors.New("center: deselect this landing server from node exits before removing it")
		}
		var encoded []byte
		if err := tx.QueryRowContext(ctx, `SELECT desired_json FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&encoded); err != nil {
			return err
		}
		var state landing.ServerState
		if json.Unmarshal(encoded, &state) != nil || state.Validate() != nil {
			return errors.New("center: invalid current landing configuration")
		}
		if state.Plan != nil && len(state.Plan.Sources) > 0 {
			return errors.New("center: move connected nodes before removing this landing server")
		}
		if err := s.queueLandingServer(ctx, tx, nodeID, nil); err != nil {
			return err
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
	return tx.Commit()
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
	var input LandingSelection
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SelectLanding(request.Context(), input); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	s.handleLanding(writer, request)
}
