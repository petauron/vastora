package landing

import "testing"

func TestDiscoPathClassificationNeverUsesHistoricalStatus(t *testing.T) {
	for _, test := range []struct {
		name string
		ping pingResult
		want string
	}{
		{"direct", pingResult{IP: "100.64.0.8", NodeIP: "100.64.0.8", Endpoint: "203.0.113.8:41641"}, "direct"},
		{"DERP despite endpoint", pingResult{IP: "100.64.0.8", NodeIP: "100.64.0.8", Endpoint: "203.0.113.8:41641", DERPRegionID: 998}, "derp"},
		{"peer relay despite endpoint", pingResult{IP: "100.64.0.8", NodeIP: "100.64.0.8", Endpoint: "203.0.113.8:41641", PeerRelay: "203.0.113.9:41641:vni:1"}, "peer_relay"},
		{"no transport proof", pingResult{IP: "100.64.0.8", NodeIP: "100.64.0.8"}, "unknown"},
		{"subnet router", pingResult{IP: "100.64.0.8", NodeIP: "100.64.0.9", Endpoint: "203.0.113.9:41641"}, "unknown"},
		{"local target", pingResult{IP: "100.64.0.8", NodeIP: "100.64.0.8", Endpoint: "203.0.113.8:41641", IsLocalIP: true}, "unknown"},
		{"error with old endpoint", pingResult{IP: "100.64.0.8", NodeIP: "100.64.0.8", Endpoint: "203.0.113.8:41641", Err: "timeout"}, "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _ := classifyPing(test.ping, "100.64.0.8")
			if got != test.want {
				t.Fatalf("state=%s want=%s", got, test.want)
			}
		})
	}
}

func TestPeerIdentityMustMatchExactNodeAndIP(t *testing.T) {
	expected := PeerIdentity{ID: "landing", PublicKey: "nodekey:expected", Address: "100.64.0.8"}
	peer := &localPeer{ID: expected.ID, PublicKey: expected.PublicKey, TailscaleIPs: []string{expected.Address}}
	status := localStatus{Peer: map[string]*localPeer{"peer": peer}}
	if matchPeer(status, expected) != peer {
		t.Fatal("current peer was rejected")
	}
	peer.PublicKey = "nodekey:replacement"
	if matchPeer(status, expected) != nil {
		t.Fatal("replacement identity reused prior proof")
	}
	peer.PublicKey = expected.PublicKey
	status.Peer["duplicate"] = peer
	if matchPeer(status, expected) != nil {
		t.Fatal("ambiguous IP assignment accepted")
	}
}
