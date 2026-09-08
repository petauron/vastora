package landing

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type latencyTransport struct{ base string }

func (transport latencyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request.URL.Scheme = "http"
	request.URL.Host = strings.TrimPrefix(transport.base, "http://")
	return http.DefaultTransport.RoundTrip(request)
}

func TestLinkLatencyUsesDiscoRTTOnlyForDirectPeer(t *testing.T) {
	for _, relay := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/localapi/v0/ping" {
				seconds := 0.0285
				ping := pingResult{IP: "100.64.0.8", NodeIP: "100.64.0.8", Endpoint: "203.0.113.8:41641", LatencySeconds: &seconds}
				if relay {
					ping.DERPRegionID = 1
				}
				_ = json.NewEncoder(w).Encode(ping)
				return
			}
			_ = json.NewEncoder(w).Encode(localStatus{BackendState: "Running", Peer: map[string]*localPeer{"peer": {ID: "landing", PublicKey: "key", TailscaleIPs: []string{"100.64.0.8"}}}})
		}))
		checker := &LinkChecker{HTTPClient: &http.Client{Transport: latencyTransport{server.URL}}}
		result := checker.Check(context.Background(), PeerIdentity{ID: "landing", PublicKey: "key", Address: "100.64.0.8"})
		server.Close()
		if relay && result.LatencyMS != nil {
			t.Fatal("relay latency presented as direct")
		}
		if !relay && (result.State != "direct" || result.LatencyMS == nil || *result.LatencyMS != 28.5) {
			t.Fatalf("missing disco latency: %+v", result)
		}
	}
}

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
