package agent

import (
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLegacyRetiredChildChanged(t *testing.T) {
	const uuid = "57c7be07-b250-4d85-89cd-a010eb48e2d0"
	for _, tc := range []struct {
		name           string
		phase          string
		summaryEmail   string
		enabled        bool
		trafficEnabled bool
		childUUID      string
		childToken     string
		wantChanged    bool
	}{
		{name: "owned disabled child", phase: "revoked", summaryEmail: "old-child", childUUID: uuid, childToken: "child-token"},
		{name: "active grant", phase: "ready", summaryEmail: "old-child", childUUID: uuid, childToken: "child-token", wantChanged: true},
		{name: "different email", phase: "revoked", summaryEmail: "other-child", childUUID: uuid, childToken: "child-token", wantChanged: true},
		{name: "reenabled account", phase: "revoked", summaryEmail: "old-child", enabled: true, childUUID: uuid, childToken: "child-token", wantChanged: true},
		{name: "reenabled traffic", phase: "revoked", summaryEmail: "old-child", trafficEnabled: true, childUUID: uuid, childToken: "child-token", wantChanged: true},
		{name: "different credential", phase: "revoked", summaryEmail: "old-child", childUUID: "different", childToken: "child-token", wantChanged: true},
		{name: "different subscription", phase: "revoked", summaryEmail: "old-child", childUUID: uuid, childToken: "different", wantChanged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := landingControllerGrant{Phase: tc.phase, ChildSubscription: "child-token"}
			grant.Task.FixedUUID = uuid
			grant.Task.Grant = landing.ClientGrant{FixedUser: "old-child", FixedIdentity: landing.Identity(uuid)}
			summary := ThreeXUIClientView{Email: tc.summaryEmail, Enabled: tc.enabled, TrafficObserved: true, TrafficEnabled: tc.trafficEnabled}
			detail := threeXUIClientDetail{Client: map[string]json.RawMessage{
				"id":    json.RawMessage(`"` + tc.childUUID + `"`),
				"subId": json.RawMessage(`"` + tc.childToken + `"`),
				"email": json.RawMessage(`"old-child"`),
			}}
			if got := legacyRetiredChildChanged(summary, detail, grant); got != tc.wantChanged {
				t.Fatalf("legacyRetiredChildChanged() = %t, want %t", got, tc.wantChanged)
			}
		})
	}
}

func TestLegacyRouteGrantConverged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		phase     string
		taskPhase string
		want      bool
	}{
		{name: "confirmed activation", phase: "ready", taskPhase: "activate", want: true},
		{name: "prepared only", phase: "prepared", taskPhase: "prepare"},
		{name: "activation in progress", phase: "activating", taskPhase: "activate"},
		{name: "retired", phase: "revoked", taskPhase: "retire"},
		{name: "unwritten active phase", phase: "active", taskPhase: "activate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := landingControllerGrant{Phase: tc.phase}
			grant.Task.Phase = tc.taskPhase
			if got := legacyRouteGrantConverged(grant); got != tc.want {
				t.Fatalf("legacyRouteGrantConverged() = %t, want %t", got, tc.want)
			}
		})
	}
}
