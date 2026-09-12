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

// AllocateQuota divides ONE remaining budget between the parent and its
// children. Giving each identity the parent's full limit would multiply that
// limit. The caller journals this plan, confirms all decreases, then applies
// increases; a lost response cannot make both allocations spendable.
func AllocateQuota(total int64, enabled bool, members []QuotaMember) ([]QuotaLimit, int64, error) {
	if total < 0 || len(members) == 0 || len(members) > 513 {
		return nil, 0, errors.New("landing: invalid shared traffic plan")
	}
	ordered := slices.Clone(members)
	slices.SortFunc(ordered, func(a, b QuotaMember) int { return strings.Compare(a.ID, b.ID) })
	var used int64
	active := int64(0)
	for i, member := range ordered {
		if !validGrantID(member.ID) || i > 0 && member.ID == ordered[i-1].ID || member.Baseline < 0 || member.Observed < member.Baseline || member.Observed-member.Baseline > math.MaxInt64-used {
			return nil, 0, errors.New("landing: invalid shared traffic observation")
		}
		used += member.Observed - member.Baseline
		if member.Active {
			active++
		}
	}
	remaining := total - used
	if remaining < 0 {
		remaining = 0
	}
	share, remainder := int64(0), int64(0)
	if active > 0 {
		share, remainder = remaining/active, remaining%active
	}
	limits := make([]QuotaLimit, 0, len(ordered))
	for _, member := range ordered {
		limit := QuotaLimit{ID: member.ID, Total: member.Observed, Enabled: enabled && member.Active}
		if total == 0 {
			limit.Total = 0
		} else if member.Active {
			addition := share
			if remainder > 0 {
				addition++
				remainder--
			}
			if addition > math.MaxInt64-limit.Total {
				return nil, 0, errors.New("landing: shared traffic limit overflow")
			}
			limit.Total += addition
			limit.Enabled = limit.Enabled && addition > 0
		} else {
			limit.Enabled = false
		}
		limits = append(limits, limit)
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
