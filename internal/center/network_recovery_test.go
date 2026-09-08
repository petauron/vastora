package center

import (
	"context"
	"testing"

	"github.com/petauron/vastora/internal/networking"
)

func TestHeartbeatRecoversOnlyTheConfirmedNetworkAddress(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	profile := networking.Profile{ServiceAddress: "100.64.0.93", HeadscaleAddress: "100.64.0.93", EnabledKinds: []string{networking.KindHeadscale}}
	node := enrollOrchestrationNode(t, store, "recover-private", NodeCapabilities{Docker: true}, []networking.Candidate{
		{Address: profile.ServiceAddress, Interface: "tailscale0", Kind: networking.KindHeadscale},
	}, profile)
	for _, address := range []string{"10.0.0.93", "100.64.0.94", profile.ServiceAddress} {
		iface := "tailscale0"
		if address == "10.0.0.93" {
			iface = "eth0"
		}
		if err := store.RecordAgentHeartbeat(ctx, node.ID, node.Credential, NodeHeartbeat{
			Version: "test", Roles: []string{"worker"}, Capabilities: NodeCapabilities{Docker: true},
			NetworkCandidates: []networking.Candidate{{Address: address, Interface: iface}},
		}); err != nil {
			t.Fatal(err)
		}
		active, err := networkProfile(ctx, store.db, node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if address != profile.ServiceAddress && active != nil {
			t.Fatal("a different address was adopted")
		}
		if address == profile.ServiceAddress && (active == nil || active.ServiceAddress != profile.ServiceAddress) {
			t.Fatal("confirmed private address did not recover")
		}
	}
}
