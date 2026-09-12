package landing

import "testing"

func TestClientSourceIdentityCannotBeInherited(t *testing.T) {
	source := PeerIdentity{ID: "original", PublicKey: "old-key", Address: "100.64.0.2"}
	plan := ClientPlan{Source: source, Grants: []ClientGrant{clientGrantFixture("grant", BothMode)}}
	if err := plan.CheckSource(source); err != nil {
		t.Fatal(err)
	}
	for _, next := range []PeerIdentity{
		{ID: "replacement", PublicKey: "old-key", Address: source.Address},
		{ID: source.ID, PublicKey: "new-key", Address: source.Address},
		{ID: source.ID, PublicKey: source.PublicKey, Address: "100.64.0.3"},
		{},
	} {
		if err := plan.CheckSource(next); err == nil {
			t.Fatal("replacement inherited active authority")
		}
	}
	plan.Grants[0].Enabled = false
	if err := plan.CheckSource(PeerIdentity{}); err != nil {
		t.Fatal("cleanup requires old machine to be online", err)
	}
}
