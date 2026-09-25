package center

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/nodediagnostics"
)

func (s *Store) StartMeridianLinkBandwidth(ctx context.Context, sourceID, landingID string) error {
	if sourceID == "" || landingID == "" || sourceID == landingID {
		return errors.New("meridian_link_invalid_pair")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if paused, err := executionClaimsPaused(ctx, tx); err != nil {
		return err
	} else if paused {
		return errors.New("meridian_link_tasks_paused")
	}
	var selected int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM settings selection JOIN json_each(selection.value,'$.nodeIds') node WHERE selection.key=? AND node.value=?)`, landingSelectionKey, landingID).Scan(&selected); err != nil {
		return err
	}
	if selected != 1 {
		return errors.New("meridian_link_landing_not_selected")
	}
	var installed int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM applications WHERE node_id=? AND app_key=? AND status='running')`, sourceID, meridianAppKey).Scan(&installed); err != nil {
		return err
	}
	if installed != 1 {
		return errors.New("meridian_link_source_required")
	}
	for _, id := range []string{sourceID, landingID} {
		var capsJSON, lastSeen string
		if err := tx.QueryRowContext(ctx, `SELECT capabilities_json,last_seen_at FROM agents WHERE id=? AND status='active' AND credential_revoked_at='' AND tailscale_ownership='managed'`, id).Scan(&capsJSON, &lastSeen); err != nil {
			return errors.New("meridian_link_node_unavailable")
		}
		seen, err := time.Parse(time.RFC3339Nano, lastSeen)
		if err != nil || !seen.After(s.now().Add(-agentConnectedMaxAge)) {
			return errors.New("meridian_link_node_offline")
		}
		var caps NodeCapabilities
		if json.Unmarshal([]byte(capsJSON), &caps) != nil || !caps.Docker || !caps.MeridianLinkBandwidth {
			return errors.New("meridian_link_docker_required")
		}
		var busy bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded') OR EXISTS(SELECT 1 FROM node_diagnostic_checks WHERE agent_id=? AND state IN ('pending','running'))`, id, id).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return errors.New("meridian_link_node_busy")
		}
	}
	var sourceJSON, landingJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT peer_json FROM agent_private_peer_capabilities WHERE node_id=?`, sourceID).Scan(&sourceJSON); err != nil {
		return errors.New("meridian_link_private_peer_unavailable")
	}
	if err := tx.QueryRowContext(ctx, `SELECT peer_json FROM landing_server_states WHERE node_id=? AND status='ready' AND desired_revision=applied_revision`, landingID).Scan(&landingJSON); err != nil {
		return errors.New("meridian_link_landing_not_ready")
	}
	var source, destination landing.PeerIdentity
	if json.Unmarshal(sourceJSON, &source) != nil || json.Unmarshal(landingJSON, &destination) != nil || source.ID == "" || source.PublicKey == "" || destination.ID == "" || destination.PublicKey == "" || source.ID == destination.ID || source.PublicKey == destination.PublicKey {
		return errors.New("meridian_link_private_peer_unavailable")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(40001))
	if err != nil {
		return err
	}
	link := nodediagnostics.LinkBandwidthTask{SourceNodeID: sourceID, LandingNodeID: landingID, SourceIP: source.Address, LandingIP: destination.Address, Port: 20000 + int(n.Int64())}
	if (nodediagnostics.Task{Link: &link}).ValidateLinkBandwidth(nodediagnostics.LinkBandwidthKind) != nil {
		return errors.New("meridian_link_private_peer_unavailable")
	}
	targetsJSON, err := json.Marshal(link)
	if err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	for _, side := range []struct{ id, kind string }{{landingID, nodediagnostics.LinkServerKind}, {sourceID, nodediagnostics.LinkBandwidthKind}} {
		token, err := randomToken(18)
		if err != nil {
			return err
		}
		id := "node-diagnostic-" + token
		_, err = tx.ExecContext(ctx, `INSERT INTO node_diagnostic_checks(agent_id,kind,id,bind_address,target_revision,targets_json,state,created_at,updated_at) VALUES(?,?,?,'',1,?,'pending',?,?)
 ON CONFLICT(agent_id,kind) DO UPDATE SET id=excluded.id,bind_address='',target_revision=1,targets_json=excluded.targets_json,state='pending',attempt=0,lease_expires_at='',error='',result_json='null',checked_at='',created_at=excluded.created_at,updated_at=excluded.updated_at`, side.id, side.kind, id, string(targetsJSON), now, now)
		if err != nil {
			return err
		}
		if err := s.recordTaskEvent(ctx, tx, id, side.id, side.kind, 1, "queued", ""); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) handleStartMeridianLinkBandwidth(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SourceNodeID  string `json:"sourceNodeId"`
		LandingNodeID string `json:"landingNodeId"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		writeError(w, http.StatusBadRequest, errors.New("meridian_link_invalid_pair"))
		return
	}
	if err := s.store.StartMeridianLinkBandwidth(r.Context(), input.SourceNodeID, input.LandingNodeID); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}
