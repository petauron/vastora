package landing

import (
	"reflect"
	"testing"
)

func TestSharedQuotaDoesNotMultiplyAcrossConcurrentIdentities(t *testing.T) {
	for count := 1; count <= 32; count++ {
		members := make([]QuotaMember, count)
		for i := range members {
			members[i] = QuotaMember{ID: Identity(string(rune('a' + i))), Observed: int64(i + 1), Active: true}
		}
		limits, used, err := AllocateQuota(10000, true, members)
		if err != nil {
			t.Fatal(err)
		}
		available := int64(0)
		for _, limit := range limits {
			for _, member := range members {
				if member.ID == limit.ID {
					available += limit.Total - member.Observed
				}
			}
		}
		if available+used != 10000 {
			t.Fatalf("%d members created %d total budget", count, available+used)
		}
	}
}

func TestSharedQuotaRetirementAndResetKeepOneLedger(t *testing.T) {
	members := []QuotaMember{{ID: "parent", Observed: 40, Active: true}, {ID: "retired-child", Observed: 30}}
	limits, used, err := AllocateQuota(100, true, members)
	if err != nil || used != 70 {
		t.Fatalf("lost usage: %d %v", used, err)
	}
	for _, limit := range limits {
		if limit.ID == "retired-child" && limit.Enabled {
			t.Fatal("retired child reenabled")
		}
	}
	if _, err := ObserveQuota(members, map[string]int64{"parent": 41}); err == nil {
		t.Fatal("ignored missing retired counter")
	}
	if _, err := ObserveQuota(members, map[string]int64{"parent": 39, "retired-child": 30}); err == nil {
		t.Fatal("accepted upstream counter reset")
	}
	reset := ResetQuota(members)
	_, used, err = AllocateQuota(100, true, reset)
	if err != nil || used != 0 || !reflect.DeepEqual(reset, ResetQuota(reset)) {
		t.Fatal("reset is not a baseline-only idempotent operation")
	}
	if members[0].Baseline != 0 {
		t.Fatal("reset mutated original journal")
	}
}

func TestSharedQuotaExpiryExhaustionAndUnlimited(t *testing.T) {
	for _, tc := range []struct {
		total   int64
		enabled bool
		want    bool
	}{{50, true, false}, {100, false, false}, {0, true, true}} {
		limits, _, err := AllocateQuota(tc.total, tc.enabled, []QuotaMember{{ID: "parent", Observed: 50, Active: true}, {ID: "child", Active: true}})
		if err != nil {
			t.Fatal(err)
		}
		for _, limit := range limits {
			if limit.Enabled != tc.want {
				t.Fatal("incorrect shared activation")
			}
			if tc.total == 0 && limit.Total != 0 {
				t.Fatal("unlimited plan acquired a synthetic quota")
			}
		}
	}
}
