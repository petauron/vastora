package agent

import (
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLandingChildOwnershipDoesNotDependOnStaleAttachments(t *testing.T) {
	identity := "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	user := landing.FixedUser("grant-a")
	client := func(value string) json.RawMessage {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	child := threeXUIClientDetail{Client: map[string]json.RawMessage{
		"id": client(identity), "email": client(user), "subId": client("private-child-token"),
	}, InboundIDs: []int{9, 10}}
	grant := landingControllerGrant{Task: landing.ControllerTask{Grant: landing.ClientGrant{FixedIdentity: landing.Identity(identity), FixedUser: user}, InboundID: 9}, ChildSubscription: "private-child-token"}
	if err := verifyLandingChild(child, grant); err != nil {
		t.Fatalf("owned child with a stale attachment could not be repaired: %v", err)
	}
	child.Client["subId"] = client("different-token")
	if err := verifyLandingChild(child, grant); err == nil {
		t.Fatal("foreign child identity was accepted")
	}
}
