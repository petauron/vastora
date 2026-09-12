package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

type removedHeadscaleIdentity struct {
	Endpoint string `json:"endpoint"`
	ID       string `json:"id"`
	NodeKey  string `json:"nodeKey"`
	Address  string `json:"address"`
}

type removalHeadscaleNode struct {
	ID          string   `json:"id"`
	NodeKey     string   `json:"nodeKey"`
	IPAddresses []string `json:"ipAddresses"`
	ForcedTags  []string `json:"forcedTags"`
	ValidTags   []string `json:"validTags"`
	User        struct {
		Name string `json:"name"`
	} `json:"user"`
}

// Resolve only a confirmed managed private address, then persist the exact
// identity BEFORE deleting it. Never rediscover a new owner of that address on
// retry. An external Tailscale network is outside Vastora's authority.
func (s *Store) removeAgentPrivateIdentity(ctx context.Context, id string) error {
	var done bool
	var ownership, address string
	var encoded []byte
	if err := s.db.QueryRowContext(ctx, `SELECT r.headscale_done,r.headscale_identity_json,a.tailscale_ownership,COALESCE(p.headscale_address,'') FROM agent_removals r JOIN agents a ON a.id=r.agent_id LEFT JOIN agent_network_profiles p ON p.agent_id=a.id WHERE a.id=?`, id).Scan(&done, &encoded, &ownership, &address); err != nil {
		return err
	}
	if done {
		return nil
	}
	finish := func() error {
		_, err := s.db.ExecContext(ctx, `UPDATE agent_removals SET headscale_done=1 WHERE agent_id=?`, id)
		return err
	}
	if ownership != "managed" || address == "" {
		return finish()
	}
	var shared int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_network_profiles WHERE agent_id<>? AND headscale_address=?`, id, address).Scan(&shared); err != nil {
		return err
	}
	if shared > 0 {
		return errors.New("center: private node address has another owner")
	}
	client, err := s.headscale(ctx)
	if err != nil {
		return err
	}
	var identity removedHeadscaleIdentity
	if err = json.Unmarshal(encoded, &identity); err != nil {
		return err
	}
	if identity.Endpoint != "" && identity.Endpoint != client.baseURL {
		return errors.New("center: private network changed during node removal")
	}
	var response struct {
		Nodes []removalHeadscaleNode `json:"nodes"`
	}
	if err = client.do(ctx, http.MethodGet, "/api/v1/node", nil, nil, &response); err != nil {
		return err
	}
	if identity.ID == "" {
		var observedKey, observedAddress string
		err := s.db.QueryRowContext(ctx, `SELECT COALESCE(json_extract(peer_json,'$.publicKey'),''),COALESCE(json_extract(peer_json,'$.address'),'') FROM landing_client_capabilities WHERE node_id=?`, id).Scan(&observedKey, &observedAddress)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, node := range response.Nodes {
			if !slices.Contains(node.IPAddresses, address) {
				continue
			}
			if identity.ID != "" {
				return errors.New("center: private node identity is ambiguous")
			}
			if node.User.Name != "vastora" || (!slices.Contains(node.ForcedTags, "tag:vastora-agent") && !slices.Contains(node.ValidTags, "tag:vastora-agent")) || node.NodeKey == "" {
				return errors.New("center: private node is not owned by Vastora")
			}
			if observedKey != "" && (observedAddress != address || strings.TrimPrefix(node.NodeKey, "nodekey:") != strings.TrimPrefix(observedKey, "nodekey:")) {
				return errors.New("center: private node no longer matches its authenticated observation")
			}
			if _, err := strconv.ParseUint(node.ID, 10, 64); err != nil || node.ID == "0" {
				return errors.New("center: invalid private node identity")
			}
			identity = removedHeadscaleIdentity{Endpoint: client.baseURL, ID: node.ID, NodeKey: node.NodeKey, Address: address}
		}
		if identity.ID == "" {
			return finish()
		}
		encoded, err = json.Marshal(identity)
		if err != nil {
			return err
		}
		if _, err = s.db.ExecContext(ctx, `UPDATE agent_removals SET headscale_identity_json=? WHERE agent_id=?`, encoded, id); err != nil {
			return err
		}
	}
	found := false
	for _, node := range response.Nodes {
		if node.ID != identity.ID {
			continue
		}
		if node.NodeKey != identity.NodeKey || !slices.Contains(node.IPAddresses, identity.Address) {
			return errors.New("center: private node identity changed during removal")
		}
		found = true
	}
	if found {
		// A failed/lost response is retried by listing this SAME immutable ID.
		if err = client.do(ctx, http.MethodDelete, "/api/v1/node/"+identity.ID, nil, nil, nil); err != nil {
			return err
		}
	}
	return finish()
}

func (s *Store) removeAgentTunnel(ctx context.Context, id string) error {
	s.cloudflareTunnelMu.Lock()
	defer s.cloudflareTunnelMu.Unlock()
	var done bool
	if err := s.db.QueryRowContext(ctx, `SELECT tunnel_done FROM agent_removals WHERE agent_id=?`, id).Scan(&done); err != nil {
		return err
	}
	if done {
		return nil
	}
	var tunnelID, tokenID string
	err := s.db.QueryRowContext(ctx, `SELECT tunnel_id,token_secret_id FROM cloudflare_tunnels WHERE agent_id=?`, id).Scan(&tunnelID, &tokenID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	operation, exists, err := s.readCloudflareTunnelOperation(ctx, id)
	if err != nil {
		return err
	}
	if exists {
		if operation.Phase == "creating" && operation.TunnelID == "" {
			return errors.New("center: resolve the unfinished Tunnel creation before removal")
		}
		if tunnelID != "" && operation.TunnelID != "" && tunnelID != operation.TunnelID {
			return errors.New("center: Tunnel identity changed during removal")
		}
		if tunnelID == "" {
			tunnelID = operation.TunnelID
		}
	}
	if tunnelID != "" {
		var protected int
		if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM center_remote_access WHERE tunnel_id=?`, tunnelID).Scan(&protected); err != nil {
			return err
		}
		if protected > 0 {
			return errNodeRemovalShared
		}
		client, err := s.cloudflare(ctx)
		if err != nil {
			return err
		}
		if exists && operation.AccountID != client.accountID {
			return errors.New("center: Tunnel account changed during removal")
		}
		if tokenID != "" {
			token, err := s.getSecret(ctx, tokenID, "cloudflare-tunnel:"+id)
			if err != nil {
				return err
			}
			account, tunnel, _, ok := cloudflareTunnelTokenIdentity(string(token))
			if !ok || account != client.accountID || tunnel != tunnelID {
				return errors.New("center: Tunnel credentials no longer match their owner")
			}
		}
		if err = client.deleteTunnel(ctx, tunnelID); err != nil && !cloudflareResourceNotFound(err) {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE agent_removals SET tunnel_done=1 WHERE agent_id=?`, id)
	return err
}

func validHeadscaleNodePath(path string) bool {
	id, ok := strings.CutPrefix(path, "/api/v1/node/")
	if !ok || id == "" {
		return false
	}
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}
