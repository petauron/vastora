package landing

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// AccessRule grants only the fixed native Dante ports between two managed
// private addresses. It cannot express arbitrary destinations, tags or ports.
type AccessRule struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

func NormalizeAccessRules(rules []AccessRule) ([]AccessRule, error) {
	if len(rules) > 128 {
		return nil, errors.New("landing: too many access rules")
	}
	result := slices.Clone(rules)
	for _, rule := range result {
		if !tailnetIPv4(rule.Source) || !tailnetIPv4(rule.Destination) || rule.Source == rule.Destination {
			return nil, errors.New("landing: invalid private access rule")
		}
	}
	slices.SortFunc(result, func(a, b AccessRule) int {
		if a.Source < b.Source {
			return -1
		}
		if a.Source > b.Source {
			return 1
		}
		if a.Destination < b.Destination {
			return -1
		}
		if a.Destination > b.Destination {
			return 1
		}
		return 0
	})
	return slices.Compact(result), nil
}

// ExtendHeadscalePolicy takes the deployer's fixed base policy, never an
// operator-supplied previous output. Regenerating prevents stale grants from
// accumulating and preserves all management grants without widening them.
func ExtendHeadscalePolicy(base []byte, rules []AccessRule) ([]byte, error) {
	rules, err := NormalizeAccessRules(rules)
	if err != nil {
		return nil, err
	}
	var policy map[string]json.RawMessage
	if json.Unmarshal(base, &policy) != nil || policy == nil {
		return nil, errors.New("landing: invalid base network policy")
	}
	var grants []json.RawMessage
	if json.Unmarshal(policy["grants"], &grants) != nil {
		return nil, errors.New("landing: base management grants are missing")
	}
	for _, rule := range rules {
		grant, err := json.Marshal(struct {
			Source      []string `json:"src"`
			Destination []string `json:"dst"`
			IP          []string `json:"ip"`
		}{
			[]string{rule.Source + "/32"}, []string{rule.Destination + "/32"}, []string{fmt.Sprintf("tcp:%d", SOCKSPort), fmt.Sprintf("udp:%d-%d", UDPRelayFirst, UDPRelayLast)},
		})
		if err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	policy["grants"], err = json.Marshal(grants)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(policy, "", "  ")
}
