package landing

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostGateScopeAndIdentity(t *testing.T) {
	peer := fixtureGate(t).peer
	gate, err := NewHostGate(peer, 1001, 7)
	if err != nil {
		t.Fatal(err)
	}
	document := fixtureNFT(t, gate)
	if found, err := gate.validate(document); !found || err != nil {
		t.Fatalf("host policy rejected: %v", err)
	}
	if !gate.matchesTarget(document, gate.table) || !gate.validOwnership(gate.table, gate.marker) {
		t.Fatal("host policy ownership not matched")
	}
	data, _ := json.Marshal(gate.objects())
	if strings.Contains(string(data), `"hook":"forward"`) || !strings.Contains(string(data), `"key":"skgid"`) || !strings.Contains(string(data), `"key":"mark"`) {
		t.Fatal("host policy must scope output by GID and replies by owned conntrack mark")
	}
	other, _ := NewHostGate(peer, 1002, 7)
	if other.table == gate.table || other.matchesTarget(document, gate.table) {
		t.Fatal("another runtime GID shares the gate")
	}
	other, _ = NewHostGate(peer, 1001, 8)
	if other.table == gate.table {
		t.Fatal("new revision shares a lease")
	}
	for _, gid := range []uint32{0, 65534} {
		if _, err := NewHostGate(peer, gid, 7); err == nil {
			t.Fatal("accepted shared or privileged GID")
		}
	}
	// Changes to the return path must be rejected even if output still matches.
	for _, object := range document.Objects {
		if rule := object["rule"]; rule != nil && rule["chain"] == "inbound" {
			rule["expr"] = []any{map[string]any{"accept": nil}}
			break
		}
	}
	if _, err := gate.validate(document); err == nil {
		t.Fatal("accepted bypassed return-path fence")
	}
}
