package landing

import (
	"errors"
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
	return nil
}
