package landing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"os/exec"
	"reflect"
	"regexp"
	"sync"
	"time"
)

const (
	SOCKSPort      = 1080
	nftTimeout     = 2 * time.Second
	nftOutputLimit = 16 << 20
)

// BridgeGate fences the managed Docker bridge's traffic to one landing SOCKS
// listener, in both directions. Host-local Agent probes and management ports do
// not traverse these rules. Native/host-network proxy instances are unsupported.
// Install must be called by a boot prerequisite before Docker can restore a
// saved landing route. This type does not install that prerequisite itself.
type BridgeGate struct {
	mu       sync.Mutex
	peer     PeerIdentity
	bridge   string
	revision uint64
	table    string
	marker   string
	run      func(context.Context, []byte, ...string) ([]byte, error)
}

var bridgeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`)

func NewBridgeGate(peer PeerIdentity, bridge string, revision uint64) (*BridgeGate, error) {
	address, err := netip.ParseAddr(peer.Address)
	if err != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(address) || address.String() != peer.Address || peer.ID == "" || peer.PublicKey == "" || !bridgeNamePattern.MatchString(bridge) || revision == 0 {
		return nil, errors.New("landing: invalid bridge gate identity")
	}
	identity, _ := json.Marshal(struct {
		Peer     PeerIdentity
		Bridge   string
		Revision uint64
	}{peer, bridge, revision})
	hash := sha256.Sum256(identity)
	return &BridgeGate{peer: peer, bridge: bridge, revision: revision,
		table:  "vastora_landing_" + hex.EncodeToString(hash[:12]),
		marker: "vastora-landing-v1:" + hex.EncodeToString(hash[:]), run: runNFT}, nil
}

type nftObject = map[string]any
type nftDocument struct {
	Objects []map[string]nftObject `json:"nftables"`
}

// No packet-path update statement is emitted: traffic cannot refresh its own
// permission. A persistent kernel set expires even if the Agent is killed.
func (g *BridgeGate) objects() []map[string]nftObject {
	objects := []map[string]nftObject{
		{"table": {"family": "inet", "name": g.table, "comment": g.marker}},
		{"set": {"family": "inet", "table": g.table, "name": "allowed", "type": "ipv4_addr", "flags": []string{"timeout"}, "timeout": int(AllowLifetime / time.Second), "size": 1}},
		{"chain": {"family": "inet", "table": g.table, "name": "forward", "type": "filter", "hook": "forward", "prio": -300, "policy": "accept"}},
		{"chain": {"family": "inet", "table": g.table, "name": "outbound"}},
		{"chain": {"family": "inet", "table": g.table, "name": "inbound"}},
	}
	match := func(left, right any) any {
		return nftObject{"match": nftObject{"op": "==", "left": left, "right": right}}
	}
	payload := func(protocol, field string) any {
		return nftObject{"payload": nftObject{"protocol": protocol, "field": field}}
	}
	meta := func(key string) any { return nftObject{"meta": nftObject{"key": key}} }
	rule := func(chain string, expr ...any) {
		objects = append(objects, map[string]nftObject{"rule": {"family": "inet", "table": g.table, "chain": chain, "expr": expr}})
	}
	// Separate TCP and UDP rules avoid ambiguous transport-header dependencies.
	for _, protocol := range []string{"tcp", "udp"} {
		rule("forward", match(meta("nfproto"), "ipv4"), match(meta("iifname"), g.bridge), match(payload("ip", "daddr"), g.peer.Address), match(meta("l4proto"), protocol), match(payload(protocol, "dport"), SOCKSPort), nftObject{"jump": nftObject{"target": "outbound"}})
		rule("forward", match(meta("nfproto"), "ipv4"), match(meta("oifname"), g.bridge), match(payload("ip", "saddr"), g.peer.Address), match(meta("l4proto"), protocol), match(payload(protocol, "sport"), SOCKSPort), nftObject{"jump": nftObject{"target": "inbound"}})
	}
	rule("outbound", match(meta("nfproto"), "ipv4"), match(payload("ip", "daddr"), "@allowed"), nftObject{"return": nil})
	rule("outbound", nftObject{"drop": nil})
	rule("inbound", match(meta("nfproto"), "ipv4"), match(payload("ip", "saddr"), "@allowed"), nftObject{"return": nil})
	rule("inbound", nftObject{"drop": nil})
	return objects
}

// Install is idempotent and always starts closed. It never replaces a table
// with different ownership or policy. A changed revision needs its own gate;
// the route switcher must keep the old gate until old connections are stopped.
func (g *BridgeGate) Install(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	document, err := g.snapshot(ctx)
	if err != nil {
		return err
	}
	found, err := g.validate(document)
	if err != nil {
		return err
	}
	if !found {
		commands := make([]any, 0, len(g.objects()))
		for _, object := range g.objects() {
			commands = append(commands, nftObject{"create": object})
		}
		// Rules support add, not create. Table creation still fails on collision
		// and the complete transaction is atomic.
		for i, object := range g.objects() {
			if _, ok := object["rule"]; ok {
				commands[i] = nftObject{"add": object}
			}
		}
		applyErr := g.apply(ctx, commands)
		document, err = g.snapshot(ctx)
		if err != nil {
			return errors.Join(applyErr, err)
		}
		found, err = g.validate(document)
		if err != nil || !found {
			return errors.Join(applyErr, err, errors.New("landing: gate installation was not confirmed"))
		}
	}
	return g.block(ctx)
}

func (g *BridgeGate) Block(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.block(ctx)
}

func (g *BridgeGate) block(ctx context.Context) error {
	document, err := g.snapshot(ctx)
	if err != nil {
		return err
	}
	if found, err := g.validate(document); err != nil || !found {
		return errors.Join(err, errors.New("landing: gate policy is unavailable"))
	}
	applyErr := g.apply(ctx, []any{g.flushAllowed()})
	document, err = g.snapshot(ctx)
	if err != nil {
		return errors.Join(applyErr, err)
	}
	if found, err := g.validate(document); err != nil || !found || len(g.elements(document)) != 0 {
		return errors.Join(applyErr, err, errors.New("landing: business blocking was not confirmed"))
	}
	return nil // Includes a lost command response followed by confirmed closure.
}

// Renew is internal to Monitor: callers cannot open the gate based on a UI
// flag, a historical ping, or a health result for an earlier revision.
func (g *BridgeGate) renew(ctx context.Context, until time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	document, err := g.snapshot(ctx)
	if err != nil {
		return err
	}
	if found, err := g.validate(document); err != nil || !found {
		return errors.Join(err, errors.New("landing: gate policy is unavailable"))
	}
	remaining := time.Until(until)
	if remaining <= nftTimeout+time.Second || remaining > AllowLifetime {
		return errors.New("landing: gate proof expired")
	}
	// libnftables JSON timeouts are seconds. Reserve the command deadline so
	// a delayed apply cannot extend the original proof's validity.
	seconds := int((remaining - nftTimeout) / time.Second)
	element := nftObject{"family": "inet", "table": g.table, "name": "allowed", "elem": []any{nftObject{"elem": nftObject{"val": g.peer.Address, "timeout": seconds}}}}
	applyErr := g.apply(ctx, []any{g.flushAllowed(), nftObject{"add": nftObject{"element": element}}})
	document, err = g.snapshot(ctx)
	if err != nil {
		return errors.Join(applyErr, err)
	}
	if found, err := g.validate(document); err != nil || !found || !g.hasLiveLease(document, until) {
		return errors.Join(applyErr, err, errors.New("landing: gate permission was not confirmed"))
	}
	return nil
}

func (g *BridgeGate) flushAllowed() any {
	return nftObject{"flush": nftObject{"set": nftObject{"family": "inet", "table": g.table, "name": "allowed"}}}
}

func (g *BridgeGate) apply(ctx context.Context, commands []any) error {
	data, err := json.Marshal(nftObject{"nftables": commands})
	if err != nil {
		return errors.New("landing: invalid firewall transaction")
	}
	_, err = g.run(ctx, data, "--json", "--file", "-")
	return err
}

func (g *BridgeGate) snapshot(ctx context.Context) (nftDocument, error) {
	data, err := g.run(ctx, nil, "--json", "list", "ruleset")
	var document nftDocument
	if err != nil {
		return document, err
	}
	// A empty/missing root is not proof that a policy exists.
	if len(data) > nftOutputLimit || json.Unmarshal(data, &document) != nil || document.Objects == nil {
		return document, errors.New("landing: invalid firewall state")
	}
	return document, nil
}

func (g *BridgeGate) validate(document nftDocument) (bool, error) {
	wantedBytes, _ := json.Marshal(g.objects())
	var wanted []map[string]nftObject
	_ = json.Unmarshal(wantedBytes, &wanted) // Normalize JSON numbers and arrays.
	actual := map[string][]nftObject{}
	found := false
	for _, object := range document.Objects {
		for kind, value := range object {
			// Flow offload can bypass forward filtering, including established
			// connections. Do not change a host's unrelated offload policy.
			if kind == "flowtable" {
				return false, errors.New("landing: flow offload is unsupported")
			}
			if kind == "metainfo" {
				continue
			}
			if kind == "table" {
				if value["family"] != "inet" || value["name"] != g.table {
					continue
				}
				found = true
			} else if value["family"] != "inet" || value["table"] != g.table {
				continue
			}
			copy := nftObject{}
			for key, entry := range value {
				if key == "handle" {
					continue
				}
				if kind == "set" && (key == "elem" || key == "use" || key == "policy") {
					continue
				}
				copy[key] = entry
			}
			actual[kind] = append(actual[kind], copy)
		}
	}
	if !found {
		if len(actual) != 0 {
			return false, errors.New("landing: incomplete firewall ownership")
		}
		return false, nil
	}
	expected := map[string][]nftObject{}
	for _, object := range wanted {
		for kind, value := range object {
			expected[kind] = append(expected[kind], value)
		}
	}
	// Keep rule order exact within each chain; nft prints chain declarations
	// alongside their rules, rather than in the original transaction order.
	groupRules := func(values []nftObject) map[string][]nftObject {
		groups := map[string][]nftObject{}
		for _, value := range values {
			chain, _ := value["chain"].(string)
			groups[chain] = append(groups[chain], value)
		}
		return groups
	}
	if !reflect.DeepEqual(groupRules(actual["rule"]), groupRules(expected["rule"])) {
		return false, errors.New("landing: firewall rules changed")
	}
	delete(actual, "rule")
	delete(expected, "rule")
	if !reflect.DeepEqual(actual, expected) {
		return false, errors.New("landing: firewall ownership or policy changed")
	}
	return true, nil
}

func (g *BridgeGate) elements(document nftDocument) []any {
	for _, object := range document.Objects {
		if set := object["set"]; set != nil && set["family"] == "inet" && set["table"] == g.table && set["name"] == "allowed" {
			if set["elem"] == nil {
				return nil
			}
			if elements, ok := set["elem"].([]any); ok {
				return elements
			}
			return []any{set["elem"]}
		}
	}
	return nil
}

func (g *BridgeGate) hasLiveLease(document nftDocument, until time.Time) bool {
	elements := g.elements(document)
	if len(elements) != 1 || !time.Now().Before(until) {
		return false
	}
	wrapper, ok := elements[0].(map[string]any)
	if !ok {
		return false
	}
	element, ok := wrapper["elem"].(map[string]any)
	if !ok || element["val"] != g.peer.Address {
		return false
	}
	timeout, _ := element["timeout"].(float64)
	expires, _ := element["expires"].(float64)
	return timeout > 0 && timeout <= AllowLifetime.Seconds() && expires > 0 && expires <= timeout && expires <= time.Until(until).Seconds()
}

type boundedNFTOutput struct{ buffer bytes.Buffer }

func (b *boundedNFTOutput) Write(data []byte) (int, error) {
	if len(data) > nftOutputLimit-b.buffer.Len() {
		return 0, errors.New("landing: firewall response exceeds limit")
	}
	return b.buffer.Write(data)
}

func runNFT(ctx context.Context, input []byte, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, nftTimeout)
	defer cancel()
	// No shell expansion, environment-provided binary path or sensitive stderr.
	command := exec.CommandContext(ctx, "/usr/sbin/nft", arguments...)
	command.Stdin = bytes.NewReader(input)
	var output boundedNFTOutput
	command.Stdout = &output
	command.WaitDelay = time.Second
	if err := command.Run(); err != nil {
		return nil, errors.New("landing: firewall operation failed")
	}
	return output.buffer.Bytes(), nil
}
