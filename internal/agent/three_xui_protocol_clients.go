package agent

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
)

// A node remains one selection in the client editor, while 3x-ui associates
// that selection with both protocol inbounds (including a disabled sibling).
func expandProtocolInboundIDs(inbounds []ThreeXUIClientInbound, ids []int) []int {
	result := append([]int(nil), ids...)
	for _, inbound := range inbounds {
		if inbound.HY2InboundID > 0 && containsInt(ids, inbound.ID) && !containsInt(result, inbound.HY2InboundID) {
			result = append(result, inbound.HY2InboundID)
		}
	}
	sort.Ints(result)
	return result
}

func collapseProtocolInboundIDs(inbounds []ThreeXUIClientInbound, ids []int) []int {
	result := make([]int, 0, len(ids))
	for _, id := range ids {
		for _, inbound := range inbounds {
			if inbound.HY2InboundID == id {
				id = inbound.ID
				break
			}
		}
		if !containsInt(result, id) {
			result = append(result, id)
		}
	}
	sort.Ints(result)
	return result
}

func hy2ClientLinkFromInbound(inbound threeXUIRealityInbound, hostname, email string) (string, error) {
	var settings struct {
		Clients []struct {
			Email string `json:"email"`
			Auth  string `json:"auth"`
		} `json:"clients"`
	}
	if inbound.Protocol != "hysteria" || !inbound.Enable || json.Unmarshal(inbound.Settings, &settings) != nil || !validThreeXUIShareHostname(hostname) {
		return "", errors.New("agent: HY2 client link is unavailable")
	}
	for _, client := range settings.Clients {
		if client.Email == email && client.Auth != "" {
			link := url.URL{Scheme: "hysteria2", User: url.User(client.Auth), Host: hostname + ":443", Fragment: inbound.Remark}
			query := url.Values{"sni": {hostname}, "alpn": {"h3"}}
			link.RawQuery = query.Encode()
			return link.String(), nil
		}
	}
	return "", errors.New("agent: client is not assigned to this HY2 node")
}
