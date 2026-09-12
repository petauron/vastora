package landing

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"
)

type Health struct {
	Revision  uint64    `json:"revision"`
	Healthy   bool      `json:"healthy"`
	CheckedAt time.Time `json:"checkedAt"`
}

// ServerState is the complete native landing service intent. A nil plan
// disables it; its revision still fences delayed installation tasks.
type ServerState struct {
	NodeID   string      `json:"nodeId"`
	Revision uint64      `json:"revision"`
	Plan     *ServerPlan `json:"plan,omitempty"`
}

func (state ServerState) Validate() error {
	return (DesiredState{NodeID: state.NodeID, Revision: state.Revision, Server: state.Plan}).Validate()
}

// DesiredState is private task data, not a public runtime-health response.
type DesiredState struct {
	NodeID   string      `json:"nodeId"`
	Revision uint64      `json:"revision"`
	Server   *ServerPlan `json:"server,omitempty"`
	Proxy    *ProxyPlan  `json:"proxy,omitempty"`
	Clients  *ClientPlan `json:"clients,omitempty"`
}

type ClientPlan struct {
	ApplicationID string        `json:"applicationId"`
	Source        PeerIdentity  `json:"source"`
	Grants        []ClientGrant `json:"grants"`
	BlockedUsers  []ClientBlock `json:"blockedUsers,omitempty"`
	// Explicit authorization for the selected instance's cutover and runtime
	// fail-closed session termination. It is not cluster-wide restart authority.
	AllowSessionReset bool `json:"allowSessionReset"`
}

type ClientBlock struct {
	ParentID   string `json:"parentId"`
	InboundTag string `json:"inboundTag"`
	User       string `json:"user"`
	Identity   string `json:"identity"`
}

// An application ID or a reused tailnet address is not a machine identity.
// Deny-only cleanup deliberately remains possible after a machine is replaced.
func (plan ClientPlan) CheckSource(actual PeerIdentity) error {
	for _, grant := range plan.Grants {
		if grant.Enabled && (plan.Source.ID == "" || plan.Source.PublicKey == "" || !tailnetIPv4(plan.Source.Address) || actual != plan.Source) {
			return errors.New("landing: entry private identity changed; revoke and authorize the replacement")
		}
	}
	return nil
}

type PeerUse struct {
	Peer    PeerIdentity
	TCPOnly bool
	Active  bool
}

func (state DesiredState) ApplicationID() string {
	if state.Proxy != nil {
		return state.Proxy.ApplicationID
	}
	if state.Clients != nil {
		return state.Clients.ApplicationID
	}
	return ""
}

func (state DesiredState) Active() bool { return state.Proxy != nil || state.Clients != nil }

func (state DesiredState) Inbounds() []string {
	tags := []string{}
	if state.Proxy != nil {
		tags = append(tags, state.Proxy.InboundTags...)
	}
	if state.Clients != nil {
		for _, grant := range state.Clients.Grants {
			if grant.Enabled && !slices.Contains(tags, grant.InboundTag) {
				tags = append(tags, grant.InboundTag)
			}
		}
	}
	slices.Sort(tags)
	return tags
}

func (state DesiredState) PeerUses() []PeerUse {
	uses := map[string]PeerUse{}
	if state.Clients != nil {
		for _, grant := range state.Clients.Grants {
			previous := uses[grant.Peer.ID]
			uses[grant.Peer.ID] = PeerUse{Peer: grant.Peer, TCPOnly: true, Active: previous.Active || grant.Enabled}
		}
	}
	if state.Proxy != nil {
		uses[state.Proxy.Peer.ID] = PeerUse{Peer: state.Proxy.Peer, Active: true}
	}
	result := make([]PeerUse, 0, len(uses))
	for _, use := range uses {
		result = append(result, use)
	}
	slices.SortFunc(result, func(a, b PeerUse) int { return strings.Compare(a.Peer.ID, b.Peer.ID) })
	return result
}

func (state DesiredState) PrepareRoutes(raw json.RawMessage) (RouteChange, error) {
	if state.Clients != nil {
		change, err := PrepareClientRoutes(raw, state.Revision, state.Proxy, state.Clients.Grants)
		if err != nil {
			return change, err
		}
		var config map[string]any
		if json.Unmarshal(change.After, &config) != nil {
			return RouteChange{}, errors.New("landing: invalid client route output")
		}
		routing := config["routing"].(map[string]any)
		prefix := []any{}
		for _, block := range state.Clients.BlockedUsers {
			prefix = append(prefix, map[string]any{"type": "field", "user": []string{block.User}, "outboundTag": grantDenyTag})
		}
		routing["rules"] = append(prefix, routing["rules"].([]any)...)
		change.After, err = json.Marshal(config)
		return change, err
	}
	if state.Proxy != nil {
		return PrepareRouteChange(raw, state.Revision, state.Proxy.InboundTags, state.Proxy.Peer)
	}
	return RouteChange{}, errors.New("landing: disabled state has no routes")
}

func (state DesiredState) ReplaceRoutes(previous RouteChange, current json.RawMessage) (RouteChange, error) {
	if state.Revision <= previous.Revision {
		return RouteChange{}, errors.New("landing: stale replacement")
	}
	_, write, err := previous.NextWrite(current, true)
	if err != nil || write {
		return RouteChange{}, errors.New("landing: original route ownership changed")
	}
	change, err := state.PrepareRoutes(previous.Before)
	if err == nil {
		change.Replaces = slices.Clone(previous.After)
	}
	return change, err
}

type ProxyPlan struct {
	ApplicationID string       `json:"applicationId"`
	InboundTags   []string     `json:"inboundTags"`
	Peer          PeerIdentity `json:"peer"`
}

func (state DesiredState) Validate() error {
	if state.NodeID == "" || state.Revision == 0 || state.Revision > 1<<62 {
		return errors.New("landing: invalid desired state identity")
	}
	if state.Server != nil {
		if state.Server.Revision != state.Revision {
			return errors.New("landing: mismatched native server revision")
		}
		if err := state.Server.Validate(); err != nil {
			return err
		}
	}
	if state.Proxy != nil {
		proxy := state.Proxy
		if proxy.ApplicationID == "" || !tailnetIPv4(proxy.Peer.Address) || proxy.Peer.ID == "" || proxy.Peer.PublicKey == "" || len(proxy.InboundTags) == 0 || len(proxy.InboundTags) > 128 {
			return errors.New("landing: invalid proxy plan")
		}
		if state.Server != nil && state.Server.Address == proxy.Peer.Address {
			return errors.New("landing: a node cannot use itself as a remote landing peer")
		}
		seen := map[string]bool{}
		for _, tag := range proxy.InboundTags {
			if tag == "" || len(tag) > 128 || strings.TrimSpace(tag) != tag || seen[tag] {
				return errors.New("landing: invalid managed inbound selection")
			}
			seen[tag] = true
		}
	}
	if state.Clients != nil {
		if state.Clients.ApplicationID == "" || !state.Clients.AllowSessionReset || len(state.Clients.Grants)+len(state.Clients.BlockedUsers) == 0 || len(state.Clients.Grants) > 512 || len(state.Clients.BlockedUsers) > 512 || state.Server != nil || state.Proxy != nil && state.Proxy.ApplicationID != state.Clients.ApplicationID {
			return errors.New("landing: invalid client plan or missing session reset approval")
		}
		if err := state.Clients.CheckSource(state.Clients.Source); err != nil {
			return err
		}
		for _, block := range state.Clients.BlockedUsers {
			if !validGrantID(block.ParentID) || !validRouteUser(block.User) || !validIdentity(block.Identity) || block.InboundTag == "" || strings.TrimSpace(block.InboundTag) != block.InboundTag || len(block.InboundTag) > 128 {
				return errors.New("landing: invalid account block")
			}
		}
		peers := map[string]PeerIdentity{}
		if state.Proxy != nil {
			peers[state.Proxy.Peer.ID] = state.Proxy.Peer
		}
		seen := map[string]bool{}
		addresses := map[string]string{}
		if state.Proxy != nil {
			addresses[state.Proxy.Peer.Address] = state.Proxy.Peer.ID
		}
		for _, grant := range state.Clients.Grants {
			if err := grant.Validate(); err != nil {
				return err
			}
			if seen[grant.ID] {
				return errors.New("landing: duplicate client grant")
			}
			seen[grant.ID] = true
			if peer, ok := peers[grant.Peer.ID]; ok && peer != grant.Peer {
				return errors.New("landing: conflicting peer identity")
			}
			if id, ok := addresses[grant.Peer.Address]; ok && id != grant.Peer.ID {
				return errors.New("landing: shared peer address")
			}
			peers[grant.Peer.ID], addresses[grant.Peer.Address] = grant.Peer, grant.Peer.ID
		}
	}
	return nil
}
