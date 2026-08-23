package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

type threeXUINodeView struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Status  string `json:"status"`
}

func applyThreeXUINodeCommand(ctx context.Context, store *Store, command ThreeXUINodeCommandTask) (ThreeXUINodeCommandResult, error) {
	baseURL, masterToken, err := threeXUIClientAPIConnection(ctx, store)
	if err != nil {
		return ThreeXUINodeCommandResult{}, err
	}
	if strings.TrimSpace(command.WorkerApplicationID) == "" {
		return ThreeXUINodeCommandResult{}, errors.New("agent: 3x-ui node identity is missing")
	}
	if command.Action == "remove" {
		return removeThreeXUINode(ctx, baseURL, masterToken, command.RemoteNodeID)
	}
	if command.Action != "reconcile" || net.ParseIP(command.Address) == nil || command.Port < 1024 || command.Port > 65535 || strings.TrimSpace(command.APIToken) == "" {
		return ThreeXUINodeCommandResult{}, errors.New("agent: invalid 3x-ui VLESS node configuration")
	}
	desiredName := threeXUINodeAPIName(command.WorkerApplicationID)
	nodes, err := listThreeXUINodes(ctx, baseURL, masterToken)
	if err != nil {
		return ThreeXUINodeCommandResult{}, err
	}
	existingID := command.RemoteNodeID
	for _, node := range nodes {
		if (command.RemoteNodeID > 0 && node.ID == command.RemoteNodeID) || node.Name == desiredName {
			existingID = node.ID
			break
		}
	}
	payload := map[string]any{
		"name": desiredName, "remark": strings.TrimSpace(command.Name), "scheme": "http",
		"address": command.Address, "port": command.Port, "basePath": "/", "apiToken": strings.TrimSpace(command.APIToken),
		"enable": true, "allowPrivateAddress": true, "tlsVerifyMode": "verify",
		"pinnedCertSha256": "", "inboundSyncMode": "all", "inboundTags": []string{}, "outboundTag": "",
	}
	endpoint := baseURL + "/panel/api/nodes/add"
	if existingID > 0 {
		endpoint = baseURL + "/panel/api/nodes/update/" + strconv.Itoa(existingID)
	}
	response, err := threeXUIAPI(ctx, http.MethodPost, endpoint, masterToken, "application/json", payload)
	if err != nil {
		return ThreeXUINodeCommandResult{}, fmt.Errorf("agent: connect VLESS node to 3x-ui controller: %w", err)
	}
	var node threeXUINodeView
	if existingID > 0 {
		payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/nodes/get/"+strconv.Itoa(existingID), masterToken, "", nil)
		if err != nil {
			return ThreeXUINodeCommandResult{}, fmt.Errorf("agent: verify 3x-ui VLESS node: %w", err)
		}
		response = payload
	}
	if json.Unmarshal(response, &node) != nil || node.ID < 1 || node.Name != desiredName || node.Address != command.Address || node.Port != command.Port {
		return ThreeXUINodeCommandResult{}, errors.New("agent: 3x-ui controller returned an invalid VLESS node")
	}
	return ThreeXUINodeCommandResult{RemoteNodeID: node.ID, Status: "ready"}, nil
}

func listThreeXUINodes(ctx context.Context, baseURL, token string) ([]threeXUINodeView, error) {
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/nodes/list", token, "", nil)
	if err != nil {
		return nil, fmt.Errorf("agent: list 3x-ui VLESS nodes: %w", err)
	}
	var nodes []threeXUINodeView
	if json.Unmarshal(payload, &nodes) != nil {
		return nil, errors.New("agent: 3x-ui controller returned invalid node data")
	}
	return nodes, nil
}

func removeThreeXUINode(ctx context.Context, baseURL, token string, nodeID int) (ThreeXUINodeCommandResult, error) {
	if nodeID < 1 {
		return ThreeXUINodeCommandResult{}, errors.New("agent: invalid 3x-ui VLESS node id")
	}
	payload, err := threeXUIAPI(ctx, http.MethodGet, baseURL+"/panel/api/inbounds/list", token, "", nil)
	if err != nil {
		return ThreeXUINodeCommandResult{}, fmt.Errorf("agent: list VLESS node inbounds before removal: %w", err)
	}
	var inbounds []struct {
		ID     int  `json:"id"`
		NodeID *int `json:"nodeId"`
	}
	if json.Unmarshal(payload, &inbounds) != nil {
		return ThreeXUINodeCommandResult{}, errors.New("agent: 3x-ui controller returned invalid inbound data")
	}
	for _, inbound := range inbounds {
		if inbound.NodeID == nil || *inbound.NodeID != nodeID {
			continue
		}
		if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/del/"+strconv.Itoa(inbound.ID), token, "application/json", map[string]any{}); err != nil {
			return ThreeXUINodeCommandResult{}, fmt.Errorf("agent: remove VLESS node inbound: %w", err)
		}
	}
	if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/nodes/del/"+strconv.Itoa(nodeID), token, "application/json", map[string]any{}); err != nil {
		if nodes, listErr := listThreeXUINodes(ctx, baseURL, token); listErr == nil {
			for _, node := range nodes {
				if node.ID == nodeID {
					return ThreeXUINodeCommandResult{}, fmt.Errorf("agent: remove VLESS node from 3x-ui controller: %w", err)
				}
			}
		} else {
			return ThreeXUINodeCommandResult{}, fmt.Errorf("agent: remove VLESS node from 3x-ui controller: %w", err)
		}
	}
	return ThreeXUINodeCommandResult{RemoteNodeID: nodeID, Status: "stopped"}, nil
}

func threeXUINodeAPIName(applicationID string) string {
	value := strings.NewReplacer("_", "-", "/", "-", ".", "-").Replace(strings.ToLower(applicationID))
	value = strings.Trim(value, "-")
	if len(value) > 32 {
		value = value[len(value)-32:]
	}
	return "vastora-" + value
}
