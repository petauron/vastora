package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/petauron/vastora/internal/nodeprotocol"
)

// Remove only the pinned local inbound. Inbound deletion detaches its client
// and host associations in 3x-ui; global clients and remote nodes are not deleted.
func removeThreeXUIRealityInbound(ctx context.Context, baseURL, token string, command RealityCommandTask) (RealityCommandResult, error) {
	if command.Action != "remove" || command.TargetNodeID != 0 || command.InboundID < 1 || command.InboundTag == "" || command.ServiceID == "" {
		return RealityCommandResult{}, errors.New("agent: invalid local REALITY removal parameters")
	}
	result := RealityCommandResult{Action: "remove", InboundID: command.InboundID, InboundTag: command.InboundTag}
	present, err := localRealityRemovalTarget(ctx, baseURL, token, command)
	if err != nil {
		return result, err
	}
	if command.RemoveHY2 {
		inbounds, err := listRealityInbounds(ctx, baseURL, token)
		if err != nil {
			return result, uncertainRealityMutation(err)
		}
		for _, inbound := range inbounds {
			if inbound.Tag != nodeprotocol.HY2Tag(command.InboundTag) || !threeXUIInboundMatchesNode(inbound, 0) {
				continue
			}
			if inbound.Protocol != "hysteria" {
				return result, errors.New("agent: refusing to remove a changed HY2 inbound")
			}
			if _, err := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/del/"+strconv.Itoa(inbound.ID), token, "", nil); err != nil {
				return result, uncertainRealityMutation(err)
			}
		}
		inbounds, err = listRealityInbounds(ctx, baseURL, token)
		if err != nil || inbounds == nil {
			return result, uncertainRealityMutation(errors.New("agent: HY2 removal could not be confirmed"))
		}
		for _, inbound := range inbounds {
			if inbound.Tag == nodeprotocol.HY2Tag(command.InboundTag) && threeXUIInboundMatchesNode(inbound, 0) {
				return result, uncertainRealityMutation(errors.New("agent: HY2 removal is incomplete"))
			}
		}
	}
	if !present {
		return result, nil
	}
	_, deleteErr := threeXUIAPI(ctx, http.MethodPost, baseURL+"/panel/api/inbounds/del/"+strconv.Itoa(command.InboundID), token, "", nil)
	// Even a successful response must be read back. A lost delete response may
	// have committed; retries retain the same id/tag and never delete a replacement.
	present, err = localRealityRemovalTarget(ctx, baseURL, token, command)
	if err != nil {
		return RealityCommandResult{}, uncertainRealityMutation(errors.Join(deleteErr, err))
	}
	if present {
		return RealityCommandResult{}, errors.Join(errors.New("agent: local REALITY inbound was not removed; retry"), deleteErr)
	}
	return result, nil
}

func localRealityRemovalTarget(ctx context.Context, baseURL, token string, command RealityCommandTask) (bool, error) {
	inbounds, err := listRealityInbounds(ctx, baseURL, token)
	if err != nil {
		return false, uncertainRealityMutation(err)
	}
	if inbounds == nil {
		return false, uncertainRealityMutation(errors.New("agent: 3x-ui did not return a complete inbound list"))
	}
	present := false
	for _, inbound := range inbounds {
		if inbound.ID != command.InboundID {
			if inbound.Tag == command.InboundTag && threeXUIInboundMatchesNode(inbound, 0) {
				return false, errors.New("agent: local REALITY inbound identity changed")
			}
			continue
		}
		var stream struct {
			Security string `json:"security"`
		}
		if inbound.Tag != command.InboundTag || !threeXUIInboundMatchesNode(inbound, 0) || inbound.Protocol != "vless" || json.Unmarshal(inbound.StreamSettings, &stream) != nil || stream.Security != "reality" {
			return false, errors.New("agent: refusing to remove a changed or remote inbound")
		}
		present = true
	}
	return present, nil
}
