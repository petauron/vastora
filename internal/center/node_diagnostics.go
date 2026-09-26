package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/nodediagnostics"
)

const carrierTargetRevision = 1

const nodeDiagnosticsSchema80SQL = `CREATE TABLE node_diagnostic_checks (
 agent_id TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('node.network-quality','node.return-route','node.international-bandwidth')),
 id TEXT NOT NULL UNIQUE,
 bind_address TEXT NOT NULL,
 target_revision INTEGER NOT NULL,
 targets_json TEXT NOT NULL CHECK(json_valid(targets_json)),
 state TEXT NOT NULL CHECK(state IN ('pending','running','succeeded','failed')),
 attempt INTEGER NOT NULL DEFAULT 0,
 lease_expires_at TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 result_json TEXT NOT NULL DEFAULT 'null' CHECK(json_valid(result_json)),
 checked_at TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(agent_id,kind)
);`

var nodeDiagnosticsSchema96SQL = strings.Replace(nodeDiagnosticsSchema80SQL, "'node.international-bandwidth'))", "'node.international-bandwidth','node.host-profile','meridian.link-bandwidth','meridian.link-bandwidth-server'))", 1)

var nodeDiagnosticsSchemaSQL = strings.Replace(nodeDiagnosticsSchema96SQL, "PRIMARY KEY(agent_id,kind)", "pair_key TEXT NOT NULL DEFAULT '',\n PRIMARY KEY(agent_id,kind,pair_key)", 1)

type NodeDiagnosticView struct {
	AgentID        string                                    `json:"agentId"`
	Kind           string                                    `json:"kind"`
	ID             string                                    `json:"id"`
	State          string                                    `json:"state"`
	Error          string                                    `json:"error,omitempty"`
	TargetRevision int64                                     `json:"targetRevision"`
	Network        []nodediagnostics.NetworkMeasurement      `json:"network,omitempty"`
	Routes         []nodediagnostics.Route                   `json:"routes,omitempty"`
	Bandwidth      []nodediagnostics.BandwidthMeasurement    `json:"bandwidth,omitempty"`
	Host           *nodediagnostics.HostProfile              `json:"host,omitempty"`
	Link           *nodediagnostics.LinkBandwidthMeasurement `json:"link,omitempty"`
	LandingNodeID  string                                    `json:"landingNodeId,omitempty"`
	CheckedAt      string                                    `json:"checkedAt,omitempty"`
	UpdatedAt      string                                    `json:"updatedAt"`
}

func (s *Store) ListNodeDiagnostics(ctx context.Context) ([]NodeDiagnosticView, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id,kind,id,state,error,target_revision,targets_json,result_json,checked_at,updated_at,lease_expires_at FROM node_diagnostic_checks ORDER BY agent_id,kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []NodeDiagnosticView{}
	linkTargets := map[string]nodediagnostics.LinkBandwidthTask{}
	for rows.Next() {
		var value NodeDiagnosticView
		var raw, targetsJSON, lease string
		if err := rows.Scan(&value.AgentID, &value.Kind, &value.ID, &value.State, &value.Error, &value.TargetRevision, &targetsJSON, &raw, &value.CheckedAt, &value.UpdatedAt, &lease); err != nil {
			return nil, err
		}
		if value.Kind == nodediagnostics.LinkBandwidthKind || value.Kind == nodediagnostics.LinkServerKind {
			var link nodediagnostics.LinkBandwidthTask
			if json.Unmarshal([]byte(targetsJSON), &link) != nil {
				return nil, errors.New("center: invalid stored Meridian link target")
			}
			if (nodediagnostics.Task{Link: &link}).ValidateLinkBandwidth(value.Kind) != nil {
				return nil, errors.New("center: invalid stored Meridian link target")
			}
			linkTargets[value.ID] = link
			if value.Kind == nodediagnostics.LinkBandwidthKind {
				value.LandingNodeID = link.LandingNodeID
			}
		}
		if raw != "null" {
			var result nodediagnostics.Result
			if json.Unmarshal([]byte(raw), &result) != nil || result.Validate(value.Kind) != nil {
				return nil, errors.New("center: invalid stored node diagnostic")
			}
			value.Network, value.Routes, value.Bandwidth, value.Host, value.Link = result.Network, result.Routes, result.Bandwidth, result.Host, result.Link
		}
		if value.State == "running" {
			expires, err := time.Parse(time.RFC3339Nano, lease)
			if err != nil || !expires.After(s.now()) {
				value.State, value.Error = "failed", "interrupted"
			}
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range values {
		value := &values[index]
		if value.Kind != nodediagnostics.LinkBandwidthKind {
			continue
		}
		target := linkTargets[value.ID]
		var peer *NodeDiagnosticView
		for other := range values {
			if values[other].AgentID == target.LandingNodeID && values[other].Kind == nodediagnostics.LinkServerKind && linkTargets[values[other].ID] == target {
				peer = &values[other]
				break
			}
		}
		if peer == nil || peer.State == "failed" || peer.Error != "" {
			value.State, value.Error, value.Link = "failed", "peer_unavailable", nil
		} else if value.State == "succeeded" && peer.State != "succeeded" {
			value.State, value.Link = "running", nil
		}
	}
	return values, nil
}

func (s *Store) StartNodeDiagnostic(ctx context.Context, agentID, kind string) error {
	if kind != nodediagnostics.NetworkKind && kind != nodediagnostics.ReturnRouteKind && kind != nodediagnostics.BandwidthKind && kind != nodediagnostics.HostProfileKind {
		return errors.New("node_diagnostics_invalid_kind")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var capabilitiesJSON, lastSeen string
	if err := tx.QueryRowContext(ctx, `SELECT capabilities_json,last_seen_at FROM agents WHERE id=? AND status='active' AND credential_revoked_at=''`, agentID).Scan(&capabilitiesJSON, &lastSeen); err != nil {
		return errors.New("node_diagnostics_node_unavailable")
	}
	seen, err := time.Parse(time.RFC3339Nano, lastSeen)
	if err != nil || !seen.After(s.now().Add(-agentConnectedMaxAge)) {
		return errors.New("node_diagnostics_node_offline")
	}
	var capabilities NodeCapabilities
	if json.Unmarshal([]byte(capabilitiesJSON), &capabilities) != nil || kind == nodediagnostics.NetworkKind && !capabilities.NetworkDiagnostics || kind == nodediagnostics.ReturnRouteKind && !capabilities.ReturnRoute || kind == nodediagnostics.BandwidthKind && !capabilities.BandwidthDiagnostics || kind == nodediagnostics.HostProfileKind && !capabilities.HostProfile {
		return errors.New("node_diagnostics_agent_upgrade_required")
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errors.New("node_diagnostics_tasks_paused")
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded') OR EXISTS(SELECT 1 FROM node_diagnostic_checks WHERE agent_id=? AND kind=? AND state IN ('pending','running'))`, agentID, agentID, kind).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return errors.New("node_diagnostics_node_busy")
	}
	input := nodediagnostics.Task{}
	if kind != nodediagnostics.HostProfileKind {
		egress, err := agentPublicEgress(ctx, tx, agentID)
		if err != nil {
			return err
		}
		if egress == nil {
			return errors.New("node_diagnostics_address_unavailable")
		}
		input.BindAddress = egress.BindAddress
	}
	var targetsJSON []byte
	if kind == nodediagnostics.HostProfileKind {
		targetsJSON = []byte("[]")
	} else if kind == nodediagnostics.BandwidthKind {
		input.BandwidthTargets = append([]nodediagnostics.BandwidthTarget(nil), nodediagnostics.BandwidthTargets...)
		targetsJSON, err = json.Marshal(input.BandwidthTargets)
	} else {
		input.Targets = append([]nodediagnostics.Target(nil), nodediagnostics.CarrierTargets...)
		targetsJSON, err = json.Marshal(input.Targets)
	}
	if err != nil || kind == nodediagnostics.BandwidthKind && input.ValidateBandwidth() != nil || kind == nodediagnostics.HostProfileKind && input.ValidateHostProfile() != nil || kind != nodediagnostics.BandwidthKind && kind != nodediagnostics.HostProfileKind && input.Validate() != nil {
		return errors.New("node_diagnostics_address_unavailable")
	}
	id, err := randomToken(18)
	if err != nil {
		return err
	}
	id = "node-diagnostic-" + id
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO node_diagnostic_checks(agent_id,kind,id,bind_address,target_revision,targets_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,'pending',?,?)
 ON CONFLICT(agent_id,kind,pair_key) DO UPDATE SET id=excluded.id,bind_address=excluded.bind_address,target_revision=excluded.target_revision,targets_json=excluded.targets_json,state='pending',attempt=0,lease_expires_at='',error='',created_at=excluded.created_at,updated_at=excluded.updated_at`, agentID, kind, id, input.BindAddress, carrierTargetRevision, string(targetsJSON), now, now)
	if err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, kind, carrierTargetRevision, "queued", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) claimNodeDiagnostic(ctx context.Context, tx *sql.Tx, agentID string) (*AgentTask, error) {
	var id, kind, bindAddress, targetsJSON string
	var attempt int64
	err := tx.QueryRowContext(ctx, `SELECT q.id,q.kind,q.bind_address,q.targets_json,q.attempt FROM node_diagnostic_checks q JOIN agents a ON a.id=q.agent_id
 WHERE q.agent_id=? AND q.state='pending' AND ((q.kind='node.network-quality' AND json_extract(a.capabilities_json,'$.networkDiagnostics')=1) OR (q.kind='node.return-route' AND json_extract(a.capabilities_json,'$.returnRoute')=1) OR (q.kind='node.international-bandwidth' AND json_extract(a.capabilities_json,'$.bandwidthDiagnostics')=1) OR (q.kind='node.host-profile' AND json_extract(a.capabilities_json,'$.hostProfile')=1) OR (q.kind IN ('meridian.link-bandwidth','meridian.link-bandwidth-server') AND json_extract(a.capabilities_json,'$.meridianLinkBandwidth')=1))
 ORDER BY q.created_at,q.kind LIMIT 1`, agentID).Scan(&id, &kind, &bindAddress, &targetsJSON, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	input := nodediagnostics.Task{BindAddress: bindAddress}
	if kind == nodediagnostics.LinkBandwidthKind || kind == nodediagnostics.LinkServerKind {
		input.Link = &nodediagnostics.LinkBandwidthTask{}
		err = json.Unmarshal([]byte(targetsJSON), input.Link)
	} else if kind == nodediagnostics.HostProfileKind {
		if targetsJSON != "[]" {
			return nil, errors.New("center: invalid host profile targets")
		}
	} else if kind == nodediagnostics.BandwidthKind {
		err = json.Unmarshal([]byte(targetsJSON), &input.BandwidthTargets)
	} else {
		err = json.Unmarshal([]byte(targetsJSON), &input.Targets)
	}
	if err != nil {
		return nil, errors.New("center: invalid stored diagnostic targets")
	}
	if kind == nodediagnostics.LinkBandwidthKind || kind == nodediagnostics.LinkServerKind {
		err = input.ValidateLinkBandwidth(kind)
	} else if kind == nodediagnostics.HostProfileKind {
		err = input.ValidateHostProfile()
	} else if kind == nodediagnostics.BandwidthKind {
		err = input.ValidateBandwidth()
	} else {
		err = input.Validate()
	}
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE node_diagnostic_checks SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND agent_id=? AND state='pending' AND attempt=?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), id, agentID, attempt)
	if err != nil {
		return nil, err
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return nil, errStaleTaskLease
	}
	task := &AgentTask{ID: id, Kind: kind, Attempt: attempt + 1, Revision: carrierTargetRevision, NodeDiagnostics: &input}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, kind, carrierTargetRevision, "claimed", ""); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *Store) completeNodeDiagnostic(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, raw json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var kind, state, lease, targetsJSON string
	if attempt <= 0 || tx.QueryRowContext(ctx, `SELECT kind,state,lease_expires_at,targets_json FROM node_diagnostic_checks WHERE id=? AND agent_id=? AND attempt=?`, id, agentID, attempt).Scan(&kind, &state, &lease, &targetsJSON) != nil {
		return errStaleTaskLease
	}
	target, diagnosticError := "succeeded", ""
	var envelope struct {
		NodeDiagnostics *nodediagnostics.Result `json:"nodeDiagnostics"`
	}
	if succeeded {
		if len(raw) > nodediagnostics.MaxResultBytes || json.Unmarshal(raw, &envelope) != nil || envelope.NodeDiagnostics == nil || envelope.NodeDiagnostics.Validate(kind) != nil {
			return errors.New("center: invalid node diagnostic result")
		}
		if kind == nodediagnostics.LinkBandwidthKind && envelope.NodeDiagnostics.Link != nil {
			var target nodediagnostics.LinkBandwidthTask
			if json.Unmarshal([]byte(targetsJSON), &target) != nil || envelope.NodeDiagnostics.Link.SourceNodeID != agentID || envelope.NodeDiagnostics.Link.LandingNodeID != target.LandingNodeID {
				return errors.New("center: Meridian link result does not match its task")
			}
		}
		diagnosticError = envelope.NodeDiagnostics.Error
	} else {
		target, diagnosticError = "failed", "interrupted"
	}
	if state == target {
		return commit(tx)
	}
	expires, err := time.Parse(time.RFC3339Nano, lease)
	if state != "running" || err != nil || !expires.After(s.now()) {
		return errStaleTaskLease
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	resultJSON := "null"
	checkedAt := ""
	if succeeded && envelope.NodeDiagnostics.Error == "" {
		encoded, err := json.Marshal(envelope.NodeDiagnostics)
		if err != nil {
			return err
		}
		resultJSON, checkedAt = string(encoded), now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE node_diagnostic_checks SET state=?,error=?,result_json=CASE WHEN ?='null' THEN result_json ELSE ? END,checked_at=CASE WHEN ?='' THEN checked_at ELSE ? END,lease_expires_at='',updated_at=? WHERE id=?`, target, diagnosticError, resultJSON, resultJSON, checkedAt, checkedAt, now, id); err != nil {
		return err
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, kind, carrierTargetRevision, target, diagnosticError); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Server) handleListNodeDiagnostics(writer http.ResponseWriter, request *http.Request) {
	values, err := s.store.ListNodeDiagnostics(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, errors.New("node_diagnostics_read_failed"))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"checks": values})
}

func (s *Server) handleStartNodeDiagnostic(writer http.ResponseWriter, request *http.Request) {
	kind := strings.TrimSpace(request.PathValue("kind"))
	if err := s.store.StartNodeDiagnostic(request.Context(), request.PathValue("id"), kind); err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]bool{"queued": true})
}
