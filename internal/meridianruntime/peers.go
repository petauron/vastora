package meridianruntime

import (
	"encoding/json"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
)

// Peer binds Center's egress Agent ID to the authenticated Tailscale identity.
// The two IDs belong to different namespaces and must never be interchanged.
// Meridian fixed routes are TCP-only; their projected Xray rules block UDP.
type Peer struct {
	EgressID string               `json:"egressId"`
	Identity landing.PeerIdentity `json:"identity"`
}

func (p Peer) Validate() error {
	validIdentityPart := func(value string, limit int) bool {
		return value != "" && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
	}
	address, err := netip.ParseAddr(p.Identity.Address)
	if !meridian.ValidIdentifier(p.EgressID) || !validIdentityPart(p.Identity.ID, 128) || !validIdentityPart(p.Identity.PublicKey, 512) ||
		err != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(address) || address.String() != p.Identity.Address {
		return errors.New("meridian runtime: invalid egress peer identity")
	}
	return nil
}

type PeerObservation struct {
	EgressID string                `json:"egressId"`
	Identity landing.PeerIdentity  `json:"identity"`
	Status   landing.MonitorStatus `json:"status"`
}

func (t Task) validatePeers() error {
	if len(t.Peers) == 0 && t.Source != nil || len(t.Peers) != 0 && (t.Source == nil || (Peer{EgressID: "entry-source", Identity: *t.Source}).Validate() != nil) {
		return errors.New("meridian runtime: invalid entry source identity")
	}
	if len(t.Peers) > 65536 {
		return errors.New("meridian runtime: too many egress peers")
	}
	egresses, identities, keys := map[string]bool{}, map[string]bool{}, map[string]bool{}
	addresses := make(map[string]bool, len(t.Peers))
	for _, peer := range t.Peers {
		if t.Source != nil && (peer.Identity.ID == t.Source.ID || peer.Identity.PublicKey == t.Source.PublicKey || peer.Identity.Address == t.Source.Address) {
			return errors.New("meridian runtime: egress is the entry source")
		}
		if peer.Validate() != nil || egresses[peer.EgressID] || identities[peer.Identity.ID] || keys[peer.Identity.PublicKey] {
			return errors.New("meridian runtime: invalid or duplicate egress peer")
		}
		if _, exists := addresses[peer.Identity.Address]; exists {
			return errors.New("meridian runtime: shared egress peer address")
		}
		egresses[peer.EgressID], identities[peer.Identity.ID], keys[peer.Identity.PublicKey] = true, true, true
		addresses[peer.Identity.Address] = false
	}
	// Bind the authenticated identities to the actual transport destinations,
	// not to user-controlled display names or generated outbound tag prefixes.
	var config struct {
		Outbounds []struct {
			Protocol string          `json:"protocol"`
			Settings json.RawMessage `json:"settings"`
		} `json:"outbounds"`
	}
	if json.Unmarshal(t.Desired.Config, &config) != nil {
		return errors.New("meridian runtime: invalid Xray outbound configuration")
	}
	for _, outbound := range config.Outbounds {
		if outbound.Protocol != "socks" {
			continue
		}
		var settings struct {
			Servers []struct {
				Address string `json:"address"`
				Port    int    `json:"port"`
			} `json:"servers"`
		}
		if json.Unmarshal(outbound.Settings, &settings) != nil || len(settings.Servers) == 0 {
			return errors.New("meridian runtime: invalid SOCKS egress destination")
		}
		for _, server := range settings.Servers {
			if _, exists := addresses[server.Address]; !exists || server.Port != landing.SOCKSPort {
				return errors.New("meridian runtime: SOCKS egress destination has no matching peer")
			}
			addresses[server.Address] = true
		}
	}
	for _, used := range addresses {
		if !used {
			return errors.New("meridian runtime: egress peer is absent from Xray configuration")
		}
	}
	return nil
}

// PeerHealth distinguishes an applied process from verified egress transport.
// The result must cover the exact task peer set and revision; a missing or
// foreign observation is a contract error. An authentic but stale or blocked
// observation only marks that peer unhealthy, without hiding healthy peers.
// Runtime receipt validation binds the whole result to the task's config SHA.
func (r Result) PeerHealth(task Task, now time.Time) (map[string]bool, error) {
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if err := r.Validate(task.Desired); err != nil {
		return nil, err
	}
	if now.IsZero() || len(r.Peers) != len(task.Peers) {
		return nil, errors.New("meridian runtime: missing or unexpected egress observations")
	}
	if (r.Source == nil) != (task.Source == nil) || r.Source != nil && *r.Source != *task.Source {
		return nil, errors.New("meridian runtime: entry observation does not match task")
	}
	expected := make(map[string]landing.PeerIdentity, len(task.Peers))
	for _, peer := range task.Peers {
		expected[peer.EgressID] = peer.Identity
	}
	health := make(map[string]bool, len(task.Peers))
	for _, observation := range r.Peers {
		identity, exists := expected[observation.EgressID]
		_, duplicate := health[observation.EgressID]
		if !exists || duplicate || identity != observation.Identity || observation.Status.Revision != task.Desired.Revision {
			return nil, errors.New("meridian runtime: egress observation does not match task")
		}
		status := observation.Status
		exit, err := netip.ParseAddr(status.ExitIP)
		health[observation.EgressID] = status.State == "healthy" && status.LinkState == "direct" && status.TCP &&
			!status.CheckedAt.IsZero() && !status.CheckedAt.After(now) && now.Sub(status.CheckedAt) <= landing.AllowLifetime &&
			status.AllowedUntil.After(now) && !status.AllowedUntil.After(status.CheckedAt.Add(landing.AllowLifetime)) &&
			err == nil && landing.PublicIP(exit)
	}
	return health, nil
}
