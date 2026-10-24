package center

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/petauron/vastora/internal/networking"
)

// Reconcile only the node owning the Center host's actual overlay interface.
// Agent names, gateway capabilities and remotely reported addresses cannot
// confer the control-plane role. An unjoined Center has no node to tag yet.
func (s *Store) reconcileHeadscaleCenterRole(ctx context.Context, client headscaleClient) error {
	candidates, err := s.discoverNetworkCandidates(s.now().UTC())
	if err != nil {
		return fmt.Errorf("center: discover local private identity: %w", err)
	}
	local := map[string]bool{}
	for _, candidate := range candidates {
		if candidate.Kind == networking.KindHeadscale {
			local[candidate.Address] = true
		}
	}
	if len(local) == 0 {
		return nil
	}
	var response struct {
		Nodes []removalHeadscaleNode `json:"nodes"`
	}
	if err := client.do(ctx, http.MethodGet, "/api/v1/node", nil, nil, &response); err != nil {
		return err
	}
	var owner *removalHeadscaleNode
	for _, node := range response.Nodes {
		matches := false
		for _, address := range node.IPAddresses {
			matches = matches || local[address]
		}
		if !matches {
			continue
		}
		if owner != nil {
			return errors.New("center: local Headscale identity is ambiguous")
		}
		if node.NodeKey == "" || !validHeadscaleNodePath("/api/v1/node/"+node.ID) || !slices.Contains(node.Tags, "tag:vastora-agent") {
			return errors.New("center: local Headscale identity is not a managed node")
		}
		copy := node
		owner = &copy
	}
	if owner == nil {
		return errors.New("center: local Headscale identity was not found")
	}
	if slices.Contains(owner.Tags, "tag:vastora-center") {
		return nil
	}
	tags := append(slices.Clone(owner.Tags), "tag:vastora-center")
	if err := client.do(ctx, http.MethodPost, "/api/v1/node/"+owner.ID+"/tags", nil, map[string]any{"tags": tags}, nil); err != nil {
		return fmt.Errorf("center: assign control-plane role: %w", err)
	}
	response.Nodes = nil
	if err := client.do(ctx, http.MethodGet, "/api/v1/node", nil, nil, &response); err != nil {
		return err
	}
	for _, node := range response.Nodes {
		if node.ID != owner.ID {
			continue
		}
		if node.NodeKey != owner.NodeKey || !slices.Equal(node.IPAddresses, owner.IPAddresses) {
			return errors.New("center: private identity changed while assigning control-plane role")
		}
		for _, tag := range tags {
			if !slices.Contains(node.Tags, tag) {
				return errors.New("center: control-plane role was not retained")
			}
		}
		return nil
	}
	return errors.New("center: private identity disappeared while assigning control-plane role")
}
