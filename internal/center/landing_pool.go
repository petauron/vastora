package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

// reconcileGlobalLandingPool derives every client combination from the one
// global selection. It never grants a client access to an entry the controller
// did not already authorize, and it never removes the original subscription
// node. Individual unavailable pairs are withheld without rolling back healthy
// pairs or the landing service itself.
func (s *Store) reconcileGlobalLandingPool(ctx context.Context, tx *sql.Tx, refreshReady bool) error {
	if owns, err := meridianOwnsLegacyLanding(ctx, tx); err != nil {
		return err
	} else if owns {
		return nil
	}
	selection, err := readLandingSelection(ctx, tx)
	if err != nil {
		return err
	}
	controller, _, err := runningGlobalThreeXUIController(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		return s.finalizeLandingPoolRetirements(ctx, tx, &selection)
	}
	if err != nil {
		return err
	}
	legacyRows, err := tx.QueryContext(ctx, `SELECT application_id,desired_revision,status FROM landing_proxy_states WHERE json_extract(desired_json,'$.proxy') IS NOT NULL ORDER BY application_id`)
	if err != nil {
		return err
	}
	type legacyProxy struct {
		applicationID string
		revision      uint64
		status        string
	}
	legacy := []legacyProxy{}
	for legacyRows.Next() {
		var value legacyProxy
		if err := legacyRows.Scan(&value.applicationID, &value.revision, &value.status); err != nil {
			legacyRows.Close()
			return err
		}
		legacy = append(legacy, value)
	}
	err = legacyRows.Err()
	legacyRows.Close()
	if err != nil {
		return err
	}
	if len(legacy) > 0 {
		for _, value := range legacy {
			if value.status == "pending" || value.status == "applying" {
				continue
			}
			if err := s.configureLandingProxy(ctx, tx, value.applicationID, LandingProxyInput{Revision: value.revision}); err != nil {
				return err
			}
		}
		return s.finalizeLandingPoolRetirements(ctx, tx, &selection)
	}
	inbounds, err := threeXUIClientInbounds(ctx, tx, controller)
	if err != nil {
		return err
	}
	clients, err := landingPoolClients(ctx, tx, controller)
	if err != nil {
		return err
	}
	desired := make(map[string]bool)
	for _, entry := range inbounds {
		if entry.VLESSDisabled || entry.InboundTag == "" {
			continue
		}
		for _, client := range clients {
			if !landingPoolClientEligible(client, entry.ID, s.now()) {
				continue
			}
			for _, target := range selection.NodeIDs {
				if target == entry.NodeID {
					continue
				}
				desired[landingPoolCombinationKey(client.ID, entry.ApplicationID, target)] = true
				var id string
				var revision uint64
				err := tx.QueryRowContext(ctx, `SELECT id FROM landing_client_grants WHERE parent_id=? AND application_id=? AND landing_node_id=?`, client.ID, entry.ApplicationID, target).Scan(&id)
				if err == nil {
					grant, err := readLandingGrant(ctx, tx, id)
					if err != nil {
						return err
					}
					if grant.Status == "prepared" {
						pairErr := withLandingPoolSavepoint(ctx, tx, func() error {
							return s.queueClientLandingRoutes(ctx, tx, grant.ApplicationID)
						})
						if pairErr != nil && !landingPoolPairUnavailable(pairErr) {
							return pairErr
						}
						continue
					}
					// Revoked rows can be retained briefly as landing-source cleanup
					// markers. Do not reuse their private identity; cleanup removes the
					// row before a later pass creates a fresh grant.
					if grant.Status == "revoked" || !refreshReady || grant.Status != "ready" {
						continue
					}
					revision = grant.Revision
				} else if !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				ready, err := s.landingServerReady(ctx, tx, target)
				if err != nil {
					return err
				}
				if !ready {
					continue
				}
				pairErr := withLandingPoolSavepoint(ctx, tx, func() error {
					_, err := s.configureClientLanding(ctx, tx, LandingClientGrantInput{
						ParentID: client.ID, ServiceID: entry.ServiceID, LandingNodeID: target,
						Mode: landing.FixedMode, Enabled: true, Revision: revision, ConfirmSessionReset: true,
					}, false)
					return err
				})
				if pairErr != nil && !landingPoolPairUnavailable(pairErr) {
					return pairErr
				}
			}
		}
	}

	rows, err := tx.QueryContext(ctx, `SELECT id FROM landing_client_grants WHERE status<>'revoked' ORDER BY id`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		grant, err := readLandingGrant(ctx, tx, id)
		if err != nil {
			return err
		}
		if desired[landingPoolCombinationKey(grant.ParentID, grant.ApplicationID, grant.LandingNodeID)] || grant.Status == "revoking" || grant.Status == "failed" || grant.Status == "paused" {
			continue
		}
		pairErr := withLandingPoolSavepoint(ctx, tx, func() error {
			_, err := s.revokeClientLandingTx(ctx, tx, LandingClientGrantInput{
				ParentID: grant.ParentID, ServiceID: grant.ServiceID,
				LandingNodeID: grant.LandingNodeID, Revision: grant.Revision,
			})
			return err
		})
		if pairErr != nil && !landingPoolPairUnavailable(pairErr) {
			return pairErr
		}
	}
	return s.finalizeLandingPoolRetirements(ctx, tx, &selection)
}

func landingPoolPairUnavailable(err error) bool {
	message := err.Error()
	for _, expected := range []string{
		"center: refresh the client list",
		"center: subscription controller changed",
		"center: choose an available VLESS entry",
		"center: update the selected Agents",
		"center: entry private identity unavailable",
		"center: landing server is not ready",
		"center: wait for the current",
		"center: landing route operation is still applying",
		"center: grant changed or is still applying",
		"center: grant identity changed",
		"center: client is disabled, expired or out of traffic",
	} {
		if strings.HasPrefix(message, expected) {
			return true
		}
	}
	return false
}

func (s *Store) resumeGlobalLandingPool(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.reconcileGlobalLandingPool(ctx, tx, false); err != nil {
		return err
	}
	return tx.Commit()
}

func landingPoolClients(ctx context.Context, tx *sql.Tx, controller string) ([]ThreeXUIClientView, error) {
	rows, err := tx.QueryContext(ctx, `SELECT metadata_json FROM three_x_ui_client_accounts WHERE controller_id=? AND email=json_extract(metadata_json,'$.email') ORDER BY id`, controller)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := []ThreeXUIClientView{}
	for rows.Next() {
		var raw []byte
		var client ThreeXUIClientView
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &client); err != nil {
			return nil, fmt.Errorf("center: invalid client inventory: %w", err)
		}
		clients = append(clients, client)
	}
	return clients, rows.Err()
}

func landingPoolClientEligible(client ThreeXUIClientView, inboundID int, now time.Time) bool {
	return client.Enabled && slices.Contains(client.InboundIDs, inboundID) &&
		(client.ExpiryTime <= 0 || client.ExpiryTime > now.UnixMilli()) &&
		(client.TotalBytes <= 0 || client.UsedBytes < client.TotalBytes)
}

func landingPoolCombinationKey(parentID, applicationID, landingNodeID string) string {
	return parentID + "\x00" + applicationID + "\x00" + landingNodeID
}

func (s *Store) landingServerReady(ctx context.Context, tx *sql.Tx, nodeID string) (bool, error) {
	var ready bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM landing_server_states s JOIN agents a ON a.id=s.node_id
		WHERE s.node_id=? AND s.status='ready' AND s.desired_revision=s.applied_revision
		AND a.status='active' AND a.credential_revoked_at='' AND a.tailscale_ownership='managed' AND a.last_seen_at>?)`,
		nodeID, s.now().UTC().Add(-landingHealthFreshness).Format(time.RFC3339Nano)).Scan(&ready)
	return ready, err
}

func withLandingPoolSavepoint(ctx context.Context, tx *sql.Tx, operation func() error) error {
	if _, err := tx.ExecContext(ctx, "SAVEPOINT landing_pool_pair"); err != nil {
		return err
	}
	if err := operation(); err != nil {
		_, _ = tx.ExecContext(ctx, "ROLLBACK TO landing_pool_pair")
		_, _ = tx.ExecContext(ctx, "RELEASE landing_pool_pair")
		return err
	}
	_, err := tx.ExecContext(ctx, "RELEASE landing_pool_pair")
	return err
}

func (s *Store) finalizeLandingPoolRetirements(ctx context.Context, tx *sql.Tx, selection *LandingSelection) error {
	remaining := slices.Clone(selection.RetiringNodeIDs)
	for _, nodeID := range selection.RetiringNodeIDs {
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM landing_client_grants WHERE landing_node_id=? AND status<>'revoked') +
			(SELECT COUNT(*) FROM landing_proxy_states WHERE landing_node_id=? AND json_extract(desired_json,'$.proxy') IS NOT NULL) +
			(SELECT COUNT(*) FROM landing_proxy_retirements WHERE landing_node_id=?)`, nodeID, nodeID, nodeID).Scan(&references); err != nil {
			return err
		}
		if references != 0 {
			continue
		}
		var status string
		var desiredJSON []byte
		err := tx.QueryRowContext(ctx, `SELECT status,desired_json FROM landing_server_states WHERE node_id=?`, nodeID).Scan(&status, &desiredJSON)
		if errors.Is(err, sql.ErrNoRows) {
			remaining = slices.DeleteFunc(remaining, func(id string) bool { return id == nodeID })
			continue
		}
		if err != nil {
			return err
		}
		var state landing.ServerState
		if json.Unmarshal(desiredJSON, &state) != nil || state.Validate() != nil {
			return errors.New("center: invalid landing service state during retirement")
		}
		if status == "stopped" {
			remaining = slices.DeleteFunc(remaining, func(id string) bool { return id == nodeID })
			continue
		}
		// With no remaining topology reference, a not-yet-applied or failed
		// empty-source plan can be superseded by the stop plan. An applying
		// task must finish first so its completion cannot race the stop.
		if (status == "pending" || status == "ready" || status == "failed") && state.Plan != nil && len(state.Plan.Sources) == 0 {
			if err := s.queueLandingServer(ctx, tx, nodeID, nil); err != nil {
				return err
			}
		}
	}
	if slices.Equal(remaining, selection.RetiringNodeIDs) {
		return nil
	}
	selection.RetiringNodeIDs = remaining
	for nodeID := range selection.LandingRegionCodes {
		if !slices.Contains(selection.NodeIDs, nodeID) && !slices.Contains(remaining, nodeID) {
			delete(selection.LandingRegionCodes, nodeID)
		}
	}
	encoded, _ := json.Marshal(selection)
	_, err := tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key=?`, encoded, landingSelectionKey)
	return err
}

func (s *Store) populateLandingServerCounts(ctx context.Context, tx *sql.Tx, selection LandingSelection, server *LandingServerView) error {
	controller, _, err := runningGlobalThreeXUIController(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	inbounds, err := threeXUIClientInbounds(ctx, tx, controller)
	if err != nil {
		return err
	}
	clients, err := landingPoolClients(ctx, tx, controller)
	if err != nil {
		return err
	}
	expected := 0
	for _, entry := range inbounds {
		if entry.VLESSDisabled || entry.InboundTag == "" || entry.NodeID == server.NodeID {
			continue
		}
		server.EligibleEntries++
		for _, client := range clients {
			if landingPoolClientEligible(client, entry.ID, s.now()) {
				expected++
			}
		}
	}
	if err := tx.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(grant.status='ready' AND proxy.status='ready' AND EXISTS(
		 SELECT 1 FROM json_each(proxy.peer_health_json) peer
		 WHERE json_extract(peer.value,'$.peer.id')=grant.landing_node_id AND json_extract(peer.value,'$.healthy')=1)
		 AND proxy.health_received_at>? ),0),
		COALESCE(SUM(grant.status IN ('failed','paused') OR proxy.status='failed'),0),
		COALESCE(SUM(grant.status NOT IN ('failed','paused','revoked') AND NOT (
		 grant.status='ready' AND proxy.status='ready' AND EXISTS(
		  SELECT 1 FROM json_each(proxy.peer_health_json) peer
		  WHERE json_extract(peer.value,'$.peer.id')=grant.landing_node_id AND json_extract(peer.value,'$.healthy')=1)
		  AND proxy.health_received_at>? )),0)
		FROM landing_client_grants grant
		LEFT JOIN landing_proxy_states proxy ON proxy.application_id=grant.application_id
		WHERE grant.landing_node_id=? AND grant.status<>'revoked'`,
		s.now().UTC().Add(-landingHealthFreshness).Format(time.RFC3339Nano),
		s.now().UTC().Add(-landingHealthFreshness).Format(time.RFC3339Nano), server.NodeID,
	).Scan(&server.ReadyCombinations, &server.FailedCombinations, &server.WithheldCombinations); err != nil {
		return err
	}
	accounted := server.ReadyCombinations + server.FailedCombinations + server.WithheldCombinations
	if slices.Contains(selection.NodeIDs, server.NodeID) && expected > accounted {
		server.WithheldCombinations += expected - accounted
	}
	return nil
}
