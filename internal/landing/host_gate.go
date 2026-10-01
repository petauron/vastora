package landing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// The mark identifies only connections opened by the dedicated runtime GID
// to an authorized landing service. Agent probes have a different GID and
// remain independent of the lease they are checking. It is a conntrack mark,
// not a packet routing mark.
const hostGateConnectionMark = 0x56415354

func NewHostGate(peer PeerIdentity, gid uint32, revision uint64) (*TrafficGate, error) {
	if !tailnetIPv4(peer.Address) || peer.ID == "" || peer.PublicKey == "" || gid == 0 || gid == 65534 || revision == 0 {
		return nil, errors.New("landing: invalid host gate identity")
	}
	identity, _ := json.Marshal(struct {
		Peer     PeerIdentity
		GID      uint32
		Revision uint64
	}{peer, gid, revision})
	hash := sha256.Sum256(identity)
	return &TrafficGate{peer: peer, gid: gid, revision: revision,
		table:  "vastora_landing_host_" + hex.EncodeToString(hash[:12]),
		marker: "vastora-landing-host-v1:" + hex.EncodeToString(hash[:]), run: runNFT}, nil
}

func (g *TrafficGate) tablePrefix() string {
	if g.gid != 0 {
		return "vastora_landing_host_"
	}
	return "vastora_landing_"
}

func (g *TrafficGate) scopeChain() string {
	if g.gid != 0 {
		return "output"
	}
	return "forward"
}

func (g *TrafficGate) validOwnership(name, marker string) bool {
	prefix := "vastora-landing-v1:"
	if g.gid != 0 {
		prefix = "vastora-landing-host-v1:"
	}
	tableHash := strings.TrimPrefix(name, g.tablePrefix())
	markerHash := strings.TrimPrefix(marker, prefix)
	decoded, err := hex.DecodeString(markerHash)
	return strings.HasPrefix(name, g.tablePrefix()) && strings.HasPrefix(marker, prefix) &&
		len(tableHash) == 24 && len(decoded) == 32 && err == nil && strings.ToLower(markerHash) == markerHash && strings.HasPrefix(markerHash, tableHash)
}

func (g *TrafficGate) hostObjects() []map[string]nftObject {
	objects := []map[string]nftObject{
		{"table": {"family": "inet", "name": g.table, "comment": g.marker}},
		{"set": {"family": "inet", "table": g.table, "name": "allowed", "type": "ipv4_addr", "flags": []string{"timeout"}, "timeout": int(AllowLifetime.Seconds()), "size": 1}},
		{"chain": {"family": "inet", "table": g.table, "name": "output", "type": "filter", "hook": "output", "prio": 10, "policy": "accept"}},
		{"chain": {"family": "inet", "table": g.table, "name": "input", "type": "filter", "hook": "input", "prio": -10, "policy": "accept"}},
		{"chain": {"family": "inet", "table": g.table, "name": "outbound"}},
		{"chain": {"family": "inet", "table": g.table, "name": "inbound"}},
	}
	match := func(left, right any) any {
		return nftObject{"match": nftObject{"op": "==", "left": left, "right": right}}
	}
	payload := func(protocol, field string) any {
		return nftObject{"payload": nftObject{"protocol": protocol, "field": field}}
	}
	ctMark := nftObject{"ct": nftObject{"key": "mark"}}
	rule := func(chain string, expr ...any) {
		objects = append(objects, map[string]nftObject{"rule": {"family": "inet", "table": g.table, "chain": chain, "expr": expr}})
	}
	for _, protocol := range []string{"tcp", "udp"} {
		var port any = SOCKSPort
		if protocol == "udp" {
			port = nftObject{"range": []int{UDPRelayFirst, UDPRelayLast}}
		}
		rule("output", match(nftObject{"meta": nftObject{"key": "skgid"}}, g.gid), match(payload("ip", "daddr"), g.peer.Address), match(payload(protocol, "dport"), port), nftObject{"jump": nftObject{"target": "outbound"}})
		rule("input", match(ctMark, hostGateConnectionMark), match(payload("ip", "saddr"), g.peer.Address), match(payload(protocol, "sport"), port), nftObject{"jump": nftObject{"target": "inbound"}})
	}
	rule("outbound", nftObject{"mangle": nftObject{"key": ctMark, "value": hostGateConnectionMark}})
	rule("outbound", match(payload("ip", "daddr"), "@allowed"), nftObject{"return": nil})
	rule("outbound", nftObject{"drop": nil})
	rule("inbound", match(payload("ip", "saddr"), "@allowed"), nftObject{"return": nil})
	rule("inbound", nftObject{"drop": nil})
	return objects
}
