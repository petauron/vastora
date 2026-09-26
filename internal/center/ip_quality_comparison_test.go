package center

import (
	"context"
	"testing"

	"github.com/petauron/vastora/internal/ipquality"
)

func TestIPQualityComparisonUsesSelectedEgressAndExplicitCurrentAddress(t *testing.T) {
	store, entry, egress := openMeridianLandingSourcesFixture(t)
	defer store.Close()
	ctx := context.Background()
	const v4 = "203.0.113.8"
	const v6 = "2001:4860:4860::8888"
	preferences := ipquality.DefaultPreferences()
	assessment := func(score int) ipquality.Assessment {
		return ipquality.Assessment{Version: ipquality.AssessmentVersion, Status: "complete", Score: &score, Min: score, Max: score, Preferences: preferences}
	}
	checks := []IPQualityView{
		{AgentID: entry, Address: v4, Assessment: assessment(60)},
		{AgentID: entry, Address: v6, Assessment: assessment(80)},
		{AgentID: egress, Address: v4, Assessment: assessment(99)},
		{AgentID: egress, Address: v6, Assessment: assessment(70)},
	}
	targets := []ipQualityProbeTarget{
		{IPQualityTarget: IPQualityTarget{AgentID: entry, Address: v4, Family: "ipv4", Selected: true}, native: true},
		{IPQualityTarget: IPQualityTarget{AgentID: entry, Address: v6, Family: "ipv6"}},
		{IPQualityTarget: IPQualityTarget{AgentID: egress, Address: v4, Family: "ipv4"}, native: true},
		{IPQualityTarget: IPQualityTarget{AgentID: egress, Address: v6, Family: "ipv6", Selected: true}},
	}
	for _, tc := range []struct {
		address string
		delta   int
	}{{"", 10}, {v6, -10}} {
		values, err := store.compareIPQuality(ctx, entry, tc.address, checks, targets, preferences)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, value := range values {
			if value.NodeID != egress {
				continue
			}
			found = true
			if value.Address != v6 || value.Family != "ipv6" || value.Assessment.Score == nil || *value.Assessment.Score != 70 || value.Delta == nil || *value.Delta != tc.delta {
				t.Fatalf("comparison mixed native and selected exits: %+v", value)
			}
		}
		if !found {
			t.Fatal("landing candidate missing")
		}
	}
	// An unmeasured selected IPv6 must not inherit the landing's IPv4 score.
	values, err := store.compareIPQuality(ctx, entry, "", checks[:3], targets, preferences)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		if value.NodeID == egress && (value.Assessment.Score != nil || value.Recommended || value.Address != v6) {
			t.Fatalf("unmeasured IPv6 borrowed IPv4: %+v", value)
		}
	}
}
