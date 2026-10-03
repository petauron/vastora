package agent

import (
	"context"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
	"testing"
)

func TestPrivateNetworkPeerBeforeApplicationRestore(t *testing.T) {
	peer := landing.PeerIdentity{ID: "replacement", PublicKey: "nodekey:new", Address: "100.64.0.8"}
	// No application store or installation exists on the replacement yet.
	store := &Store{linkChecker: &fakeMeridianLinkChecker{self: peer}}
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
