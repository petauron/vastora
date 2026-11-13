package agent

import (
	"math"
)

func legacyExcludedChildChanged(summary ThreeXUIClientView, detail threeXUIClientDetail, grant landingControllerGrant) bool {
	return summary.Email != grant.Task.Grant.FixedUser || verifyLandingChild(detail, grant) != nil
}

func legacyNativeOnlyUsage(account landingControllerAccount, identity string, childOwnerByIdentity map[string]string) (int64, int64, bool) {
	if account.ID != identity {
		return 0, 0, false
	}
	var baseline, used int64
	found := false
	seen := map[string]bool{}
	for _, member := range account.Members {
		if seen[member.ID] || (member.ID != identity && childOwnerByIdentity[member.ID] != identity) || member.Baseline < 0 || member.Observed < member.Baseline || member.Observed-member.Baseline > math.MaxInt64-used {
			return 0, 0, false
		}
		seen[member.ID] = true
		used += member.Observed - member.Baseline
		if member.ID == identity {
			baseline, found = member.Baseline, true
		}
	}
	if !found || used > math.MaxInt64-baseline {
		return 0, 0, false
	}
	// Preserve consumption by all retired or omitted child credentials in the
	// one imported native watermark. New Xray counters start at zero and the
	// Center watermark adds only their subsequent deltas.
	return baseline, baseline + used, true
}
