package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"
)

const landingNativeSyncTimeout = 45 * time.Second

// 3x-ui v3.7.0 returns nodePending when the controller accepted a write but a
// target node is unavailable. Its background reconcile clears configDirty only
// after pushing the configuration, guarded by configDirtyAt. Read that contract;
// online status or a controller-side client read-back alone is not confirmation.
func writeLandingNativeChange(ctx context.Context, baseURL, token, endpoint string, payload any, inboundIDs []int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Resolve before the write, especially before detach removes attachments.
	nodeIDs, err := landingNativeNodeIDs(ctx, baseURL, token, inboundIDs)
	if err != nil {
		return err
	}
	result, err := threeXUIAPI(ctx, http.MethodPost, endpoint, token, "application/json", payload)
	if err != nil {
		return errors.New("agent: native account write was not confirmed; explicit recovery required")
	}
	pending, err := landingNativeWritePending(result)
	if err != nil || !pending {
		return err
	}
	return waitLandingNativeSync(ctx, baseURL, token, nodeIDs)
}

func landingNativeNodeIDs(ctx context.Context, baseURL, token string, inboundIDs []int) ([]int, error) {
	var nodeIDs []int
	scope := slices.Clone(inboundIDs)
	slices.Sort(scope)
	for _, inboundID := range slices.Compact(scope) {
		if inboundID <= 0 {
			return nil, errors.New("agent: invalid entry synchronization scope")
		}
		inbound, err := getThreeXUIInbound(ctx, baseURL, token, inboundID)
		if err != nil {
			return nil, errors.New("agent: cannot resolve entry synchronization scope")
		}
		if inbound.NodeID != nil {
			if *inbound.NodeID <= 0 {
				return nil, errors.New("agent: invalid entry synchronization node")
			}
			if !slices.Contains(nodeIDs, *inbound.NodeID) {
				nodeIDs = append(nodeIDs, *inbound.NodeID)
			}
		}
	}
	return nodeIDs, nil
}

func landingNativeWritePending(raw json.RawMessage) (bool, error) {
	// Native synchronous success has no obj; a deferred success has a boolean.
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var status struct {
		NodePending *bool `json:"nodePending"`
	}
	if json.Unmarshal(raw, &status) != nil || status.NodePending == nil {
		return false, errors.New("agent: invalid native account synchronization response")
	}
	return *status.NodePending, nil
}

func waitLandingNativeSync(ctx context.Context, baseURL, token string, nodeIDs []int) error {
	if len(nodeIDs) == 0 {
		return errors.New("agent: pending native write has no verifiable entry node")
	}
	waitCtx, cancel := context.WithTimeout(ctx, landingNativeSyncTimeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ready := true
		for _, nodeID := range nodeIDs {
			if err := waitCtx.Err(); err != nil {
				return fmt.Errorf("agent: entry synchronization remains unconfirmed: %w", err)
			}
			raw, err := threeXUIAPI(waitCtx, http.MethodGet, baseURL+"/panel/api/nodes/get/"+strconv.Itoa(nodeID), token, "", nil)
			if err != nil {
				if waitCtx.Err() != nil {
					return fmt.Errorf("agent: entry synchronization remains unconfirmed: %w", waitCtx.Err())
				}
				return errors.New("agent: cannot read entry synchronization state")
			}
			var node struct {
				ID          int    `json:"id"`
				Enable      *bool  `json:"enable"`
				Status      string `json:"status"`
				ConfigDirty *bool  `json:"configDirty"`
			}
			if json.Unmarshal(raw, &node) != nil || node.ID != nodeID || node.Enable == nil || node.ConfigDirty == nil || node.Status == "" {
				return errors.New("agent: invalid entry synchronization state")
			}
			if !*node.Enable {
				return errors.New("agent: entry synchronization is blocked by a disabled node")
			}
			ready = ready && node.Status == "online" && !*node.ConfigDirty
		}
		if err := waitCtx.Err(); err != nil {
			return fmt.Errorf("agent: entry synchronization remains unconfirmed: %w", err)
		}
		if ready {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("agent: entry synchronization remains unconfirmed: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
