package landing

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func clientGrantFixture(id string, mode PublishingMode) ClientGrant {
	parent := Identity("11111111-2222-4333-8444-555555555555")
	return ClientGrant{ID: id, ParentID: parent, BaseIdentity: parent, BaseUser: "phone", InboundTag: "business", FixedUser: FixedUser(id), FixedIdentity: Identity("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"), Peer: PeerIdentity{ID: "landing-a", PublicKey: "key-a", Address: "100.64.0.8"}, Mode: mode, Enabled: true}
}

func TestClientGrantModesIntersectWithoutEscalation(t *testing.T) {
	for _, allowed := range []PublishingMode{FixedMode, AdvancedMode, BothMode} {
		for _, published := range []PublishingMode{FixedMode, AdvancedMode, BothMode} {
			grant := clientGrantFixture("combination-a", allowed).Published(published)
			if err := grant.Validate(); err != nil {
				t.Fatal(err)
			}
			if grant.Enabled && grant.Mode.Fixed() != (allowed.Fixed() && published.Fixed()) || grant.Enabled && grant.Mode.Advanced() != (allowed.Advanced() && published.Advanced()) {
				t.Fatalf("mode expanded: %s / %s -> %+v", allowed, published, grant)
			}
			if grant.Enabled != ((allowed.Fixed() && published.Fixed()) || (allowed.Advanced() && published.Advanced())) {
				t.Fatal("disjoint modes remained enabled")
			}
		}
	}
}

func TestClientGrantRoutesKeepBaseAndScopeAdvancedException(t *testing.T) {
	grant := clientGrantFixture("combination-a", BothMode)
	forced := &ProxyPlan{ApplicationID: "entry", InboundTags: []string{"business"}, Peer: grant.Peer}
	for _, proxy := range []*ProxyPlan{nil, forced} {
		change, err := PrepareClientRoutes(json.RawMessage(routeFixture), 3, proxy, []ClientGrant{grant})
		if err != nil {
			t.Fatal(err)
		}
		config, _, err := decodeRouteSettings(change.After)
		if err != nil {
			t.Fatal(err)
		}
		rules := config["routing"].(map[string]any)["rules"].([]any)
		advanced, fixed, catchall := -1, -1, -1
		for i, value := range rules {
			rule := value.(map[string]any)
			tag, _ := rule["outboundTag"].(string)
			if strings.HasPrefix(tag, grantPrivateTag) {
				advanced = i
				if !reflect.DeepEqual(rule["user"], []any{grant.BaseUser}) || !reflect.DeepEqual(rule["inboundTag"], []any{"business"}) || !reflect.DeepEqual(rule["ip"], []any{"100.64.0.8/32"}) || rule["network"] != "tcp" || rule["port"] != "1080" {
					t.Fatal("advanced exception escaped its tuple")
				}
			}
			if tag == FixedOutbound(grant.ID) {
				fixed = i
				if !reflect.DeepEqual(rule["user"], []any{grant.FixedUser}) {
					t.Fatal("fixed route changed the base user")
				}
			}
			if tag == grantDenyTag && rule["inboundTag"] == nil && reflect.DeepEqual(rule["user"], []any{grant.FixedUser}) {
				catchall = i
			}
		}
		if advanced < 0 || fixed <= advanced || catchall <= fixed {
			t.Fatalf("unsafe route order: %d/%d/%d", advanced, fixed, catchall)
		}
		before, _, _ := decodeRouteSettings(change.Before)
		for _, key := range []string{"api", "stats", "policy"} {
			if !reflect.DeepEqual(before[key], config[key]) {
				t.Fatalf("changed unrelated %s", key)
			}
		}
	}
}

func TestRevokedClientRoutesNeverRestoreDirectForOldChild(t *testing.T) {
	grant := clientGrantFixture("combination-a", BothMode)
	first, err := PrepareClientRoutes(json.RawMessage(routeFixture), 3, nil, []ClientGrant{grant})
	if err != nil {
		t.Fatal(err)
	}
	grant.Enabled = false
	state := DesiredState{NodeID: "entry", Revision: 4, Clients: &ClientPlan{ApplicationID: "app", Source: PeerIdentity{ID: "entry", PublicKey: "entry-key", Address: "100.64.0.2"}, Grants: []ClientGrant{grant}, AllowSessionReset: true}}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, use := range state.PeerUses() {
		if use.Active {
			t.Fatal("revocation requires the offline peer")
		}
	}
	if len(state.Inbounds()) != 0 {
		t.Fatal("revocation requires a deleted inbound")
	}
	next, err := state.ReplaceRoutes(first, first.After)
	if err != nil {
		t.Fatal(err)
	}
	config, _, _ := decodeRouteSettings(next.After)
	for _, value := range config["outbounds"].([]any) {
		tag := value.(map[string]any)["tag"].(string)
		if tag == FixedOutbound(grant.ID) || strings.HasPrefix(tag, grantPrivateTag) {
			t.Fatal("revoked outbound remains enabled")
		}
	}
	if !strings.Contains(string(next.After), grant.FixedUser) || !strings.Contains(string(next.After), grantDenyTag) {
		t.Fatal("old child can fall through")
	}
	state.Revision = 3
	if _, err := state.ReplaceRoutes(first, first.After); err == nil {
		t.Fatal("accepted a stale replacement revision")
	}
}

func TestClientOnlyParentBlockHasValidRouteAndNoPeerDependency(t *testing.T) {
	grant := clientGrantFixture("combination-a", BothMode)
	state := DesiredState{NodeID: "entry", Revision: 1, Clients: &ClientPlan{ApplicationID: "app", AllowSessionReset: true, BlockedUsers: []ClientBlock{{ParentID: grant.ParentID, InboundTag: "removed", User: grant.BaseUser, Identity: grant.BaseIdentity}}}}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	if state.ApplicationID() != "app" || len(state.PeerUses()) != 0 {
		t.Fatal("incorrect block ownership")
	}
	change, err := state.PrepareRoutes(json.RawMessage(routeFixture))
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(change.After, &config); err != nil {
		t.Fatal(err)
	}
	rule := config["routing"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if rule["outboundTag"] != grantDenyTag || !reflect.DeepEqual(rule["user"], []any{grant.BaseUser}) {
		t.Fatal("account deny is not first")
	}
}

func TestClientSourceUnionPreservesForcedUDPInEitherOrder(t *testing.T) {
	tcp := []AuthorizedNode{{Address: "100.64.0.1", TCPOnly: true}, {Address: "100.64.0.2", TCPOnly: true}}
	forced := []AuthorizedNode{{Address: "100.64.0.1"}}
	one, err := MergeSources(tcp, forced)
	if err != nil {
		t.Fatal(err)
	}
	two, err := MergeSources(forced, tcp)
	if err != nil || !slices.Equal(one, two) {
		t.Fatal("source ordering changes authority")
	}
	if one[0].TCPOnly || !one[1].TCPOnly {
		t.Fatal("wrong protocol union")
	}
	remaining, err := MergeSources(tcp)
	if err != nil || !remaining[0].TCPOnly {
		t.Fatal("removing forced landing did not remove its UDP use")
	}
}
