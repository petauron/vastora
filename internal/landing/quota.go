package landing

import (
	"errors"
	"math"
	"slices"
	"strings"
)

// QuotaMember keeps native monotonically increasing counters. A monthly reset
// advances Baseline instead of resetting a live upstream counter. Retired
// identities stay in the ledger so revocation cannot refund consumed traffic.
type QuotaMember struct {
	ID       string `json:"id"`
	Baseline int64  `json:"baseline"`
	Observed int64  `json:"observed"`
	Active   bool   `json:"active"`
}

type QuotaLimit struct {
	ID      string `json:"id"`
	Total   int64  `json:"total"`
	Enabled bool   `json:"enabled"`
}

// AllocateQuota computes one authoritative parent ledger and projects only an
// on/off gate to its native identities. Active identities have no individual
// native cap: redistributing a shrinking remainder would rewrite Xray on every
// observation. The Agent disables all identities after the aggregate reaches
// the parent total, so enforcement can overshoot by at most one observation
// interval plus in-flight traffic instead of multiplying the plan per child.
func AllocateQuota(total int64, enabled bool, members []QuotaMember) ([]QuotaLimit, int64, error) {
	if total < 0 || len(members) == 0 || len(members) > 513 {
		return nil, 0, errors.New("landing: invalid shared traffic plan")
	}
	ordered := slices.Clone(members)
	slices.SortFunc(ordered, func(a, b QuotaMember) int { return strings.Compare(a.ID, b.ID) })
	var used int64
	for i, member := range ordered {
		if !validGrantID(member.ID) || i > 0 && member.ID == ordered[i-1].ID || member.Baseline < 0 || member.Observed < member.Baseline || member.Observed-member.Baseline > math.MaxInt64-used {
			return nil, 0, errors.New("landing: invalid shared traffic observation")
		}
		used += member.Observed - member.Baseline
	}
	accountEnabled := enabled && (total == 0 || used < total)
	limits := make([]QuotaLimit, 0, len(ordered))
	for _, member := range ordered {
		limits = append(limits, QuotaLimit{ID: member.ID, Total: 0, Enabled: accountEnabled && member.Active})
	}
	return limits, used, nil
}

func ObserveQuota(members []QuotaMember, counters map[string]int64) ([]QuotaMember, error) {
	result := slices.Clone(members)
	for i, member := range result {
		value, ok := counters[member.ID]
		if !ok || value < member.Observed {
			return nil, errors.New("landing: shared traffic counters changed or are incomplete")
		}
		result[i].Observed = value
	}
	return result, nil
}

func ResetQuota(members []QuotaMember) []QuotaMember {
	result := slices.Clone(members)
	for i := range result {
		result[i].Baseline = result[i].Observed
	}
	return result
}
