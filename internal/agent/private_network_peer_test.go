package agent

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
	"testing"
)

func TestPrivateNetworkPeerBeforeApplicationRestore(t *testing.T) {
	peer := landing.PeerIdentity{ID: "replacement", PublicKey: "nodekey:new", Address: "100.64.0.8"}
	// No application store or installation exists on the replacement yet.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/status" {
			t.Errorf("unexpected identity endpoint: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"BackendState":"Running","Self":{"ID":"replacement","PublicKey":"nodekey:new","TailscaleIPs":["100.64.0.8"]}}`))
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}}
	checker := &landing.LinkChecker{HTTPClient: &http.Client{Transport: transport}}
	defer checker.Close()
	store := &Store{linkChecker: checker}
	candidates := []networking.Candidate{{Kind: networking.KindHeadscale, Address: peer.Address}}
	got := store.observePrivateNetworkPeer(context.Background(), candidates, "managed")
	if got == nil || *got != peer {
		t.Fatal("fresh replacement did not report its network identity")
	}
	if store.observePrivateNetworkPeer(context.Background(), candidates, "external") != nil {
		t.Fatal("external network advertised managed identity")
	}
	if store.observePrivateNetworkPeer(context.Background(), []networking.Candidate{{Kind: networking.KindLAN, Address: peer.Address}}, "managed") != nil {
		t.Fatal("LAN candidate advertised private identity")
	}
}
