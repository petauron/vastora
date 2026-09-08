package landing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// ServerFirewall contains no passwords. UID must belong to the dedicated,
// non-login landing account, not root or another application. Inbound sources
// are exact node addresses on the actual tailscaled interface, not all tailnet
// members. The output chain runs after output DNAT, so post-resolution private
// addresses (including DNS rebinding) cannot bypass the destination restriction.
type ServerFirewall struct {
	Revision  uint64   `json:"revision"`
	Address   string   `json:"address"`
	Sources   []string `json:"sources"`
	Interface string   `json:"interface"`
	UID       uint32   `json:"uid"`
}

func (policy ServerFirewall) validate() error {
	if policy.Revision == 0 || !tailnetIPv4(policy.Address) || policy.UID == 0 || policy.UID == 65534 || !bridgeNamePattern.MatchString(policy.Interface) || len(policy.Sources) > 128 {
		return errors.New("landing: invalid native firewall identity")
	}
	seen := map[string]bool{}
	for _, source := range policy.Sources {
		if !tailnetIPv4(source) || source == policy.Address || seen[source] {
			return errors.New("landing: invalid native firewall source")
		}
		seen[source] = true
	}
	return nil
}

func (policy ServerFirewall) identity() (string, string) {
	policy.Sources = slices.Clone(policy.Sources)
	slices.Sort(policy.Sources)
	data, _ := json.Marshal(policy)
	digest := sha256.Sum256(data)
	return "vastora_landing_server_" + hex.EncodeToString(digest[:8]), "vastora-landing-server-v1:" + hex.EncodeToString(digest[:])
}

func (policy ServerFirewall) objects() []map[string]nftObject {
	table, marker := policy.identity()
	objects := []map[string]nftObject{
		{"table": {"family": "inet", "name": table, "comment": marker}},
		{"chain": {"family": "inet", "table": table, "name": "input", "type": "filter", "hook": "input", "prio": -10, "policy": "accept"}},
		{"chain": {"family": "inet", "table": table, "name": "output", "type": "filter", "hook": "output", "prio": 10, "policy": "accept"}},
		{"chain": {"family": "inet", "table": table, "name": "sources"}},
		{"chain": {"family": "inet", "table": table, "name": "destinations"}},
	}
	match := func(left, right any) any {
		return nftObject{"match": nftObject{"op": "==", "left": left, "right": right}}
	}
	payload := func(protocol, field string) any {
		return nftObject{"payload": nftObject{"protocol": protocol, "field": field}}
	}
	meta := func(key string) any { return nftObject{"meta": nftObject{"key": key}} }
	rule := func(chain string, expr ...any) {
		objects = append(objects, map[string]nftObject{"rule": {"family": "inet", "table": table, "chain": chain, "expr": expr}})
	}
	for _, protocol := range []string{"tcp", "udp"} {
		var port any = SOCKSPort
		if protocol == "udp" {
			port = nftObject{"range": []int{UDPRelayFirst, UDPRelayLast}}
		}
		rule("input", match(payload("ip", "daddr"), policy.Address), match(payload(protocol, "dport"), port), nftObject{"jump": nftObject{"target": "sources"}})
	}
	sources := slices.Clone(policy.Sources)
	slices.Sort(sources)
	for _, source := range sources {
		rule("sources", match(meta("iifname"), policy.Interface), match(payload("ip", "saddr"), source), nftObject{"return": nil})
	}
	rule("sources", nftObject{"drop": nil})
	rule("output", match(meta("skuid"), policy.UID), nftObject{"jump": nftObject{"target": "destinations"}})
	// Only replies to an existing authorized SOCKS conversation may return to
	// the private network. There is no general established/related bypass.
	for _, source := range sources {
		for _, protocol := range []string{"tcp", "udp"} {
			var port any = SOCKSPort
			if protocol == "udp" {
				port = nftObject{"range": []int{UDPRelayFirst, UDPRelayLast}}
			}
			rule("destinations", match(meta("oifname"), policy.Interface), match(payload("ip", "saddr"), policy.Address), match(payload("ip", "daddr"), source), match(payload(protocol, "sport"), port), match(nftObject{"ct": nftObject{"key": "direction"}}, "reply"), nftObject{"return": nil})
		}
	}
	for _, cidr := range blockedIPv4 {
		prefix := netip.MustParsePrefix(cidr)
		rule("destinations", match(payload("ip", "daddr"), nftObject{"prefix": nftObject{"addr": prefix.Addr().String(), "len": prefix.Bits()}}), nftObject{"drop": nil})
	}
	for _, portString := range strings.Split(managementPorts, ",") {
		port, _ := strconv.Atoi(portString)
		for _, protocol := range []string{"tcp", "udp"} {
			rule("destinations", match(payload(protocol, "dport"), port), nftObject{"drop": nil})
		}
	}
	for _, protocol := range []string{"tcp", "udp"} {
		rule("destinations", match(meta("nfproto"), "ipv4"), match(meta("l4proto"), protocol), nftObject{"return": nil})
	}
	rule("destinations", nftObject{"drop": nil}) // IPv6 and non-TCP/UDP fail closed.
	return objects
}

// Install adds a complete, atomic policy and confirms its read-back. A
// different revision gets a different table, permitting safe overlap during a
// stopped-service transition; removing old rules requires the exact old plan.
func (policy ServerFirewall) Install(ctx context.Context) error {
	return policy.install(ctx, runNFT)
}

// Remove is used only while the owned native service is stopped. A changed
// or foreign table is never deleted to make a configuration update succeed.
func (policy ServerFirewall) Remove(ctx context.Context) error {
	if err := policy.validate(); err != nil {
		return err
	}
	table, _ := policy.identity()
	data, err := runNFT(ctx, nil, "--json", "list", "ruleset")
	var document nftDocument
	if err != nil || len(data) > nftOutputLimit || json.Unmarshal(data, &document) != nil {
		return errors.New("landing: cannot inspect native firewall before removal")
	}
	found, err := validateNFTPolicy(document, table, policy.objects(), false)
	if err != nil || !found {
		return err
	}
	command, _ := json.Marshal(nftObject{"nftables": []any{nftObject{"delete": nftObject{"table": nftObject{"family": "inet", "name": table}}}}})
	_, removeErr := runNFT(ctx, command, "--json", "--file", "-")
	data, err = runNFT(ctx, nil, "--json", "list", "ruleset")
	if err != nil || len(data) > nftOutputLimit || json.Unmarshal(data, &document) != nil {
		return errors.New("landing: cannot confirm native firewall removal")
	}
	found, err = validateNFTPolicy(document, table, policy.objects(), false)
	if err != nil || found {
		return errors.Join(removeErr, err, errors.New("landing: native firewall removal was not confirmed"))
	}
	return nil
}

func (policy ServerFirewall) install(ctx context.Context, run func(context.Context, []byte, ...string) ([]byte, error)) error {
	if err := policy.validate(); err != nil {
		return err
	}
	table, _ := policy.identity()
	snapshot := func() (bool, error) {
		data, err := run(ctx, nil, "--json", "list", "ruleset")
		var document nftDocument
		if err != nil || len(data) > nftOutputLimit || json.Unmarshal(data, &document) != nil || document.Objects == nil {
			return false, errors.New("landing: native firewall state is unavailable")
		}
		return validateNFTPolicy(document, table, policy.objects(), false)
	}
	found, err := snapshot()
	if err != nil || found {
		return err
	}
	commands := make([]any, 0, len(policy.objects()))
	for _, object := range policy.objects() {
		operation := "create"
		if object["rule"] != nil {
			operation = "add"
		}
		commands = append(commands, nftObject{operation: object})
	}
	data, _ := json.Marshal(nftObject{"nftables": commands})
	_, applyErr := run(ctx, data, "--json", "--file", "-")
	if found, err := snapshot(); err != nil || !found {
		return errors.Join(applyErr, err, errors.New("landing: native firewall installation was not confirmed"))
	}
	return nil
}
