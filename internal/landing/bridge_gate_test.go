package landing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func fixtureGate(t *testing.T) *BridgeGate {
	t.Helper()
	gate, err := NewBridgeGate(PeerIdentity{ID: "node-landing", PublicKey: "nodekey:expected", Address: "100.64.0.8"}, "br-owned", 7)
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

func fixtureNFT(t *testing.T, gate *BridgeGate) nftDocument {
	t.Helper()
	data, err := json.Marshal(nftDocument{Objects: gate.objects()})
	if err != nil {
		t.Fatal(err)
	}
	var result nftDocument
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBridgeGateRejectsPolicyDriftAndFlowOffload(t *testing.T) {
	gate := fixtureGate(t)
	for _, test := range []struct {
		name   string
		change func(*nftDocument)
	}{
		{"unowned table", func(d *nftDocument) { d.Objects[0]["table"]["comment"] = "someone-else" }},
		{"dormant table", func(d *nftDocument) { d.Objects[0]["table"]["flags"] = []any{"dormant"} }},
		{"no expiry", func(d *nftDocument) { delete(d.Objects[1]["set"], "timeout") }},
		{"long expiry", func(d *nftDocument) { d.Objects[1]["set"]["timeout"] = float64(3600) }},
		{"wrong hook", func(d *nftDocument) { d.Objects[2]["chain"]["hook"] = "output" }},
		{"wrong priority", func(d *nftDocument) { d.Objects[2]["chain"]["prio"] = float64(300) }},
		{"no drop", func(d *nftDocument) { d.Objects = d.Objects[:len(d.Objects)-1] }},
		{"changed drop", func(d *nftDocument) {
			d.Objects[len(d.Objects)-1]["rule"]["expr"] = []any{map[string]any{"accept": nil}}
		}},
		{"external offload", func(d *nftDocument) {
			d.Objects = append(d.Objects, map[string]nftObject{"flowtable": {"family": "inet", "table": "unrelated", "name": "fast"}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := fixtureNFT(t, gate)
			if found, err := gate.validate(document); err != nil || !found {
				t.Fatalf("fixture rejected: %v", err)
			}
			test.change(&document)
			if _, err := gate.validate(document); err == nil {
				t.Fatal("changed firewall policy accepted")
			}
		})
	}
}

func TestBridgeGateLeaseCannotBePermanentOrReused(t *testing.T) {
	gate := fixtureGate(t)
	for _, test := range []struct {
		name    string
		element any
		want    bool
	}{
		{"live", map[string]any{"elem": map[string]any{"val": gate.peer.Address, "timeout": float64(10), "expires": float64(9)}}, true},
		{"plain permanent", gate.peer.Address, false},
		{"no expiration", map[string]any{"elem": map[string]any{"val": gate.peer.Address, "timeout": float64(10)}}, false},
		{"expired", map[string]any{"elem": map[string]any{"val": gate.peer.Address, "timeout": float64(10), "expires": float64(0)}}, false},
		{"wrong target", map[string]any{"elem": map[string]any{"val": "100.64.0.9", "timeout": float64(10), "expires": float64(9)}}, false},
		{"excess lifetime", map[string]any{"elem": map[string]any{"val": gate.peer.Address, "timeout": float64(3600), "expires": float64(3599)}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := fixtureNFT(t, gate)
			document.Objects[1]["set"]["elem"] = []any{test.element}
			if got := gate.hasLiveLease(document, time.Now().Add(12*time.Second)); got != test.want {
				t.Fatalf("lease=%v want=%v", got, test.want)
			}
		})
	}
	newRevision, err := NewBridgeGate(gate.peer, gate.bridge, 8)
	if err != nil || newRevision.table == gate.table {
		t.Fatal("new revision reused old permission")
	}
	peer := gate.peer
	peer.PublicKey = "nodekey:replaced"
	newPeer, err := NewBridgeGate(peer, gate.bridge, 7)
	if err != nil || newPeer.table == gate.table {
		t.Fatal("replacement identity reused old permission")
	}
}

func TestBridgeGateInstallClosesExistingPermissionWithoutReplacingRules(t *testing.T) {
	gate := fixtureGate(t)
	document := fixtureNFT(t, gate)
	document.Objects[1]["set"]["elem"] = []any{gate.peer.Address}
	writes := 0
	gate.run = func(_ context.Context, input []byte, _ ...string) ([]byte, error) {
		if input != nil {
			writes++
			if !strings.Contains(string(input), `"flush":{"set":`) || bytes.Contains(input, []byte(`"table":{`)) {
				t.Fatalf("unexpected rule replacement: %s", input)
			}
			delete(document.Objects[1]["set"], "elem")
			return nil, errors.New("lost response")
		}
		return json.Marshal(document)
	}
	if err := gate.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if writes != 1 {
		t.Fatalf("writes=%d", writes)
	}
}

func TestBridgeGateCommandsNeverContainGlobalOrSelfRefreshingRules(t *testing.T) {
	gate := fixtureGate(t)
	data, _ := json.Marshal(gate.objects())
	for _, forbidden := range []string{`"established"`, `"flow"`, `"update"`, `"hook":"output"`, `"hook":"input"`, `"hook":"prerouting"`} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("unsafe scope: %s", forbidden)
		}
	}
	for _, name := range []string{"", "br-bad\";", "br-*", "0123456789abcdef"} {
		if _, err := NewBridgeGate(gate.peer, name, 1); err == nil {
			t.Fatalf("accepted bridge %q", name)
		}
	}
}
