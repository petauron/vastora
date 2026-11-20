package agent

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/petauron/vastora/internal/landing"
)

func TestLegacyExcludedChildChanged(t *testing.T) {
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
		{name: "retired child", phase: "revoked", summaryEmail: "old-child", childUUID: uuid, childToken: "child-token"},
		{name: "prepared child", phase: "prepared", summaryEmail: "old-child", childUUID: uuid, childToken: "child-token"},
		{name: "active child", phase: "ready", summaryEmail: "old-child", enabled: true, trafficEnabled: true, childUUID: uuid, childToken: "child-token"},
		{name: "different email", phase: "revoked", summaryEmail: "other-child", childUUID: uuid, childToken: "child-token", wantChanged: true},
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
			if got := legacyExcludedChildChanged(summary, detail, grant); got != tc.wantChanged {
				t.Fatalf("legacyExcludedChildChanged() = %t, want %t", got, tc.wantChanged)
			}
		})
	}
}

func TestLegacyNativeOnlyUsagePreservesSharedConsumption(t *testing.T) {
	account := landingControllerAccount{ID: "native", Members: []landing.QuotaMember{
		{ID: "native", Baseline: 100, Observed: 130},
		{ID: "child", Baseline: 20, Observed: 60},
		{ID: "retired-child", Baseline: 0, Observed: 5},
	}}
	baseline, observed, ok := legacyNativeOnlyUsage(account, "native", map[string]string{"child": "native", "retired-child": "native"})
	if !ok || baseline != 100 || observed != 175 {
		t.Fatalf("legacyNativeOnlyUsage() = (%d, %d, %t), want (100, 175, true)", baseline, observed, ok)
	}
}

func TestLegacyNativeOnlyUsageRejectsUnownedOrInvalidConsumption(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account landingControllerAccount
		owners  map[string]string
	}{
		{name: "missing native", account: landingControllerAccount{ID: "native", Members: []landing.QuotaMember{{ID: "child", Observed: 1}}}, owners: map[string]string{"child": "native"}},
		{name: "unknown child", account: landingControllerAccount{ID: "native", Members: []landing.QuotaMember{{ID: "native"}, {ID: "child", Observed: 1}}}},
		{name: "other account child", account: landingControllerAccount{ID: "native", Members: []landing.QuotaMember{{ID: "native"}, {ID: "child", Observed: 1}}}, owners: map[string]string{"child": "other"}},
		{name: "duplicate member", account: landingControllerAccount{ID: "native", Members: []landing.QuotaMember{{ID: "native"}, {ID: "native"}}}},
		{name: "decreasing counter", account: landingControllerAccount{ID: "native", Members: []landing.QuotaMember{{ID: "native", Baseline: 2, Observed: 1}}}},
		{name: "overflow", account: landingControllerAccount{ID: "native", Members: []landing.QuotaMember{{ID: "native", Baseline: 1, Observed: math.MaxInt64}, {ID: "child", Observed: 2}}}, owners: map[string]string{"child": "native"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, ok := legacyNativeOnlyUsage(tc.account, "native", tc.owners); ok {
				t.Fatal("legacyNativeOnlyUsage() accepted invalid usage")
			}
		})
	}
}
