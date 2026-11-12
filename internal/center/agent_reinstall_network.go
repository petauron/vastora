package center

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
)

// Capture the authenticated old-machine observation while reserving recovery.
// Once an immutable controller ID is resolved, it is saved before withdrawal.
// Neither a matching address nor a shared ownership tag establishes identity.
type reinstallPrivateIdentity struct {
	Ownership string `json:"ownership"`
	Endpoint  string `json:"endpoint"`
	ID        string `json:"id"`
	NodeKey   string `json:"nodeKey"`
	Address   string `json:"address"`
}

func captureReinstallPrivateIdentity(ctx context.Context, tx *sql.Tx, agentID string, network AgentReinstallNetwork) ([]byte, error) {
	// A fresh, explicitly reviewed grant may replace an unused grant. Preserve
	// its original observation even if a later heartbeat changed live metadata.
	var saved []byte
	err := tx.QueryRowContext(ctx, `SELECT private_identity_json FROM agent_reinstall_operations WHERE agent_id=? AND state='awaiting_enrollment'`, agentID).Scan(&saved)
	if err == nil {
		return saved, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	identity := reinstallPrivateIdentity{Ownership: network.Ownership, Address: network.PrivateAddress}
	var observedAddress string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(json_extract(peer_json,'$.publicKey'),''),COALESCE(json_extract(peer_json,'$.address'),'') FROM agent_private_peer_capabilities WHERE node_id=?`, agentID).Scan(&identity.NodeKey, &observedAddress)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if identity.Address == "" {
		identity.Address = observedAddress
	} else if observedAddress != identity.Address {
		// A mismatching observation cannot authorize deletion by address.
		identity.NodeKey = ""
	}
	err = tx.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind='headscale' AND status='configured'`).Scan(&identity.Endpoint)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return json.Marshal(identity)
}

func (s *Store) isolateReinstallPrivateIdentity(ctx context.Context, agentID, operationID string) error {
	var encoded []byte
	var state, isolation string
	if err := s.db.QueryRowContext(ctx, `SELECT state,private_isolation,private_identity_json FROM agent_reinstall_operations WHERE id=? AND agent_id=?`, operationID, agentID).Scan(&state, &isolation, &encoded); err != nil {
		return err
	}
	if state != "preparing" || isolation != "pending" {
		return errors.New("center: private identity recovery requires a new explicit authorization")
	}
	var identity reinstallPrivateIdentity
	if err := json.Unmarshal(encoded, &identity); err != nil {
		return errors.New("center: saved private identity evidence is invalid")
	}
	if identity.Ownership == "" && identity.Address == "" && identity.NodeKey == "" {
		return s.finishReinstallPrivateIsolation(ctx, operationID, "not_required")
	}
	if identity.Ownership != "managed" {
		return errors.New("center: previous private network is externally managed; verify its isolation before recovery")
	}
	if identity.Address == "" || strings.TrimPrefix(identity.NodeKey, "nodekey:") == "" || identity.Endpoint == "" {
		return errors.New("center: authenticated previous private identity evidence is missing")
	}
	var shared bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_network_profiles WHERE agent_id<>? AND headscale_address=?)`, agentID, identity.Address).Scan(&shared); err != nil {
		return err
	}
	if shared {
		return errors.New("center: previous private address is assigned to another node")
	}
	client, err := s.headscale(ctx)
	if err != nil {
		return errors.New("center: private controller is unavailable; recovery remains paused")
	}
	if identity.Endpoint != client.baseURL {
		return errors.New("center: private controller changed after recovery authorization")
	}
	var response struct {
		Nodes []removalHeadscaleNode `json:"nodes"`
	}
	if err = client.do(ctx, http.MethodGet, "/api/v1/node", nil, nil, &response); err != nil || response.Nodes == nil {
		return errors.New("center: private identity inspection failed; recovery remains paused")
	}
	found := false
	for _, node := range response.Nodes {
		keyMatches := strings.TrimPrefix(node.NodeKey, "nodekey:") == strings.TrimPrefix(identity.NodeKey, "nodekey:")
		addressMatches := slices.Contains(node.IPAddresses, identity.Address)
		if !keyMatches && !addressMatches && node.ID != identity.ID {
			continue
		}
		if !keyMatches || !addressMatches || !slices.Contains(node.Tags, "tag:vastora-agent") ||
			!validHeadscaleNodePath("/api/v1/node/"+node.ID) || (identity.ID != "" && node.ID != identity.ID) || found {
			return errors.New("center: previous private identity or address ownership changed; recovery remains paused")
		}
		identity.ID = node.ID
		found = true
	}
	if found {
		encoded, err = json.Marshal(identity)
		if err != nil {
			return err
		}
		result, err := s.db.ExecContext(ctx, `UPDATE agent_reinstall_operations SET private_identity_json=? WHERE id=? AND agent_id=? AND state='preparing' AND private_isolation='pending'`, encoded, operationID, agentID)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errors.New("center: recovery command authorization changed")
		}
		if err = client.do(ctx, http.MethodDelete, "/api/v1/node/"+identity.ID, nil, nil, nil); err != nil {
			return errors.New("center: private identity withdrawal was not confirmed; inspect the saved identity before continuing")
		}
		// A successful delete response alone is insufficient to issue a command.
		// Confirm disappearance; do not repeat DELETE on a failed verification.
		response.Nodes = nil
		if err = client.do(ctx, http.MethodGet, "/api/v1/node", nil, nil, &response); err != nil || response.Nodes == nil {
			return errors.New("center: private identity withdrawal could not be verified; recovery remains paused")
		}
		for _, node := range response.Nodes {
			if node.ID == identity.ID || strings.TrimPrefix(node.NodeKey, "nodekey:") == strings.TrimPrefix(identity.NodeKey, "nodekey:") || slices.Contains(node.IPAddresses, identity.Address) {
				return errors.New("center: previous private identity or address is still present; recovery remains paused")
			}
		}
	}
	return s.finishReinstallPrivateIsolation(ctx, operationID, "withdrawn")
}

func (s *Store) finishReinstallPrivateIsolation(ctx context.Context, operationID, state string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE agent_reinstall_operations SET private_isolation=? WHERE id=? AND state='preparing' AND private_isolation='pending'`, state, operationID)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errors.New("center: recovery command authorization changed")
	}
	return nil
}

// An administrator may supply missing old-machine evidence, never replace a
// different saved key. The immutable tuple is rechecked by normal isolation.
func (s *Store) reviewReinstallPreviousPeer(ctx context.Context, agentID, adminID string, input AgentReinstallContinueInput) error {
	key := strings.TrimPrefix(strings.TrimSpace(input.PreviousPeerKey), "nodekey:")
	decoded, err := hex.DecodeString(key)
	if !input.ConfirmPreviousPeer || len(decoded) != 32 || err != nil || !validHeadscaleNodePath("/api/v1/node/"+input.PreviousPeerID) {
		return errors.New("center: explicitly review the previous private peer ID and public key")
	}
	var encoded []byte
	err = s.db.QueryRowContext(ctx, `SELECT private_identity_json FROM agent_reinstall_operations
 WHERE id=? AND agent_id=? AND authorized_by=? AND attempt=? AND state IN ('failed','preparing') AND private_isolation='pending'
 AND EXISTS(SELECT 1 FROM admins WHERE id=?) AND EXISTS(SELECT 1 FROM agents WHERE id=? AND credential_revoked_at<>'')`, input.OperationID, agentID, adminID, input.ExpectedAttempt, adminID, agentID).Scan(&encoded)
	if err != nil {
		return errors.New("center: recovery progress changed; refresh before reviewing the previous private peer")
	}
	var identity reinstallPrivateIdentity
	if err = json.Unmarshal(encoded, &identity); err != nil {
		return err
	}
	if identity.Ownership != "managed" || identity.Endpoint == "" || identity.Address == "" ||
		(identity.ID != "" && identity.ID != input.PreviousPeerID) ||
		(identity.NodeKey != "" && strings.TrimPrefix(identity.NodeKey, "nodekey:") != key) {
		return errors.New("center: reviewed private peer cannot replace saved recovery evidence")
	}
	client, err := s.headscale(ctx)
	if err != nil {
		return err
	}
	if client.baseURL != identity.Endpoint {
		return errors.New("center: private controller changed after recovery authorization")
	}
	var response struct {
		Nodes []removalHeadscaleNode `json:"nodes"`
	}
	if err = client.do(ctx, http.MethodGet, "/api/v1/node", nil, nil, &response); err != nil || response.Nodes == nil {
		return errors.New("center: private identity inspection failed; recovery remains paused")
	}
	found := false
	for _, node := range response.Nodes {
		keyMatches := strings.TrimPrefix(node.NodeKey, "nodekey:") == key
		addressMatches := slices.Contains(node.IPAddresses, identity.Address)
		if node.ID != input.PreviousPeerID && !keyMatches && !addressMatches {
			continue
		}
		if found || node.ID != input.PreviousPeerID || !keyMatches || !addressMatches || !slices.Contains(node.Tags, "tag:vastora-agent") {
			return errors.New("center: reviewed previous private identity is ambiguous or changed")
		}
		found = true
	}
	if !found {
		return errors.New("center: reviewed previous private peer is not present on the saved controller")
	}
	identity.ID, identity.NodeKey = input.PreviousPeerID, "nodekey:"+key
	reviewed, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var shared bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_network_profiles WHERE agent_id<>? AND headscale_address=?)`, agentID, identity.Address).Scan(&shared); err != nil {
		return err
	}
	if shared {
		return errors.New("center: previous private address is assigned to another node")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_reinstall_operations SET private_identity_json=?
 WHERE id=? AND agent_id=? AND authorized_by=? AND attempt=? AND private_identity_json=? AND state IN ('failed','preparing') AND private_isolation='pending'`, reviewed, input.OperationID, agentID, adminID, input.ExpectedAttempt, encoded)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errors.New("center: recovery evidence changed during private peer review")
	}
	if err = s.recordTaskEvent(ctx, tx, input.OperationID, agentID, "agent.reinstall.private-identity", int64(input.ExpectedAttempt), "succeeded", "Authorizing administrator reviewed the exact previous controller peer ID, public key and saved address; withdrawal remains required"); err != nil {
		return err
	}
	return tx.Commit()
}
