package center

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"sort"

	"github.com/petauron/vastora/internal/ipquality"
	"github.com/petauron/vastora/internal/landing"
)

// IPQualityTarget names one public exit that can be probed independently. The
// local bind address is resolved from authenticated Agent evidence, never input.
type IPQualityTarget struct {
	AgentID  string `json:"agentId"`
	Address  string `json:"address"`
	Family   string `json:"family"`
	Selected bool   `json:"selected"`
}

type ipQualityProbeTarget struct {
	IPQualityTarget
	bindAddress string
	native      bool
}

func ipQualityAddress(value string) string {
	if address := net.ParseIP(value); address != nil {
		return address.String()
	}
	return ""
}

func ipQualityFamily(address string) string {
	if ip := net.ParseIP(address); ip != nil && ip.To4() == nil {
		return "ipv6"
	}
	return "ipv4"
}

func ipQualityKey(agentID, address string) string {
	return agentID + "\x00" + ipQualityAddress(address)
}

func listIPQualityTargets(ctx context.Context, queryer networkQueryer, agentID string) ([]ipQualityProbeTarget, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT a.id,a.public_egress_address,a.public_egress_bind_address,a.landing_egress_addresses_json,
 COALESCE((SELECT json_extract(l.desired_json,'$.plan.egressIp') FROM landing_server_states l WHERE l.node_id=a.id AND l.status<>'stopped'),'')
 FROM agents a WHERE a.status='active' AND a.credential_revoked_at='' AND (?='' OR a.id=?) AND (
 EXISTS(SELECT 1 FROM services s JOIN applications app ON app.id=s.application_id WHERE app.node_id=a.id AND s.app_protocol IN ('vless/tcp/reality','meridian/entry') AND s.status<>'stopped')
 OR EXISTS(SELECT 1 FROM landing_server_states l WHERE l.node_id=a.id AND l.status<>'stopped')) ORDER BY a.id`, agentID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ipQualityProbeTarget{}
	for rows.Next() {
		var id, native, bind, raw, selected string
		if err := rows.Scan(&id, &native, &bind, &raw, &selected); err != nil {
			return nil, err
		}
		native, bind = ipQualityAddress(native), ipQualityAddress(bind)
		byAddress := map[string]ipQualityProbeTarget{}
		if (ipquality.Task{Address: native, BindAddress: bind}).Validate() == nil {
			byAddress[native] = ipQualityProbeTarget{IPQualityTarget: IPQualityTarget{AgentID: id, Address: native, Family: ipQualityFamily(native)}, bindAddress: bind, native: true}
		}
		var inventory []landing.EgressAddress
		if err := json.Unmarshal([]byte(raw), &inventory); err != nil {
			return nil, err
		}
		for _, candidate := range inventory {
			// Private local interfaces have no known public identity except the
			// separately observed native NAT mapping above.
			address, err := netip.ParseAddr(candidate.Address)
			if err != nil || !landing.PublicIP(address) || landing.ValidateEgressAddresses([]landing.EgressAddress{candidate}) != nil {
				continue
			}
			canonical := address.String()
			if _, exists := byAddress[canonical]; exists {
				continue
			}
			byAddress[canonical] = ipQualityProbeTarget{IPQualityTarget: IPQualityTarget{AgentID: id, Address: canonical, Family: ipQualityFamily(canonical)}, bindAddress: canonical}
		}
		if selected == "" {
			selected = native
		} else {
			selected = ipQualityAddress(selected)
			if selected == bind {
				selected = native
			}
		}
		for address, target := range byAddress {
			target.Selected = address == selected
			values = append(values, target)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].AgentID != values[j].AgentID {
			return values[i].AgentID < values[j].AgentID
		}
		if values[i].Family != values[j].Family {
			return values[i].Family < values[j].Family
		}
		return values[i].Address < values[j].Address
	})
	return values, nil
}

func publicIPQualityTargets(targets []ipQualityProbeTarget) []IPQualityTarget {
	values := make([]IPQualityTarget, 0, len(targets))
	for _, target := range targets {
		values = append(values, target.IPQualityTarget)
	}
	return values
}
