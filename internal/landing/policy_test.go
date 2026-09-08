package landing

import (
	"encoding/json"
	"testing"
)

func TestLandingPolicyRetainsManagementAndRestrictsPorts(t *testing.T) {
	base := []byte(`{"tagOwners":{"tag:vastora-center":["vastora@"]},"grants":[{"src":["tag:vastora-agent"],"dst":["tag:vastora-center"],"ip":["443"]}]}`)
	rule := AccessRule{Source: "100.64.0.2", Destination: "100.64.0.3"}
	encoded, err := ExtendHeadscalePolicy(base, []AccessRule{rule, rule})
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Grants []struct {
			Source      []string `json:"src"`
			Destination []string `json:"dst"`
			IP          []string `json:"ip"`
		} `json:"grants"`
		Owners map[string][]string `json:"tagOwners"`
	}
	if json.Unmarshal(encoded, &policy) != nil || len(policy.Grants) != 2 || len(policy.Owners) != 1 {
		t.Fatal("base policy changed or duplicate grant retained")
	}
	grant := policy.Grants[1]
	if grant.Source[0] != "100.64.0.2/32" || grant.Destination[0] != "100.64.0.3/32" || len(grant.IP) != 2 || grant.IP[0] != "tcp:1080" || grant.IP[1] != "udp:1081-1208" {
		t.Fatal("overbroad grant")
	}
	for _, bad := range []AccessRule{{Source: "*", Destination: rule.Destination}, {Source: "10.0.0.1", Destination: rule.Destination}, {Source: rule.Source, Destination: rule.Source}} {
		if _, err := ExtendHeadscalePolicy(base, []AccessRule{bad}); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
}
