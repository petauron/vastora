package ipquality

import (
	"errors"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"
)

const AssessmentVersion = "meridian-v2"
const EvidenceMaxAge = 24 * time.Hour

type Preferences struct {
	RequiredServices []string `json:"requiredServices"`
	TargetRegion     string   `json:"targetRegion"`
}

func DefaultPreferences() Preferences {
	return Preferences{RequiredServices: []string{"ChatGPT", "Netflix", "DisneyPlus"}}
}

func (p Preferences) Validate() error {
	if len(p.RequiredServices) > len(services) || p.TargetRegion != "" && !regionPattern.MatchString(p.TargetRegion) {
		return errors.New("ipquality: invalid preferences")
	}
	seen := map[string]bool{}
	for _, name := range p.RequiredServices {
		if !contains(services, name) || seen[name] {
			return errors.New("ipquality: invalid preferences")
		}
		seen[name] = true
	}
	return nil
}

type Contribution struct {
	ID      string   `json:"id"`
	Weight  float64  `json:"weight"`
	Min     float64  `json:"min"`
	Max     float64  `json:"max"`
	Missing []string `json:"missing"`
}

type Assessment struct {
	Version         string           `json:"version"`
	Status          string           `json:"status"`
	Score           *int             `json:"score,omitempty"`
	Min             int              `json:"min"`
	Max             int              `json:"max"`
	Grade           string           `json:"grade"`
	IPType          string           `json:"ipType"`
	TypeCandidates  []string         `json:"typeCandidates"`
	TypeEvidence    []Classification `json:"typeEvidence"`
	Contributions   []Contribution   `json:"contributions"`
	Missing         []string         `json:"missing"`
	Advice          string           `json:"advice"`
	Reasons         []string         `json:"reasons"`
	RequiredFailed  []string         `json:"requiredFailed"`
	RequiredUnknown []string         `json:"requiredUnknown"`
	Preferences     Preferences      `json:"preferences"`
	ValidUntil      string           `json:"validUntil,omitempty"`
}

type typeRule struct {
	name        string
	points, cap float64
}

var typeRules = []typeRule{{"residential", 20, 100}, {"mobile", 17, 95}, {"business", 10, 89}, {"hosting", 5, 79}}
var scoreWeights = []struct {
	name   string
	weight float64
}{{"SCAMALYTICS", 10}, {"IPQS", 10}, {"AbuseIPDB", 5}}
var unlockWeights = []struct {
	name   string
	weight float64
}{{"ChatGPT", 15}, {"Netflix", 6}, {"DisneyPlus", 4}, {"Youtube", 2}, {"AmazonPrimeVideo", 2}, {"TikTok", 1}}

func usageType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "isp", "line isp", "residential", "residential isp":
		return "residential"
	case "mobile", "mobile isp":
		return "mobile"
	case "business", "commercial":
		return "business"
	case "hosting", "data center", "datacenter", "cdn":
		return "hosting"
	default:
		return ""
	}
}

func roundedScore(value float64) int { return int(math.Round(math.Max(1, math.Min(100, value)))) }

func freshAt(stamp string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, stamp)
	return err == nil && !t.After(now.Add(5*time.Second)) && now.Sub(t) <= EvidenceMaxAge
}

func (r Report) sourceFresh(source string, now time.Time) bool {
	for _, item := range r.Observations {
		if item.Source == source {
			return item.Status == "ok" && sameIP(item.Address, r.Address) && freshAt(item.CheckedAt, now)
		}
	}
	return false
}

func sameIP(a, b string) bool {
	ip := net.ParseIP(a)
	return ip != nil && ip.Equal(net.ParseIP(b))
}

// A result from the original check is not evidence for a different IP or a
// later check. Unknown provider values retain their entire possible range.
func Assess(report *Report, checkedAt string, changed bool, now time.Time, preferences Preferences) Assessment {
	a := Assessment{Version: AssessmentVersion, Status: "partial", Grade: "unknown", IPType: "unknown",
		TypeCandidates: []string{}, TypeEvidence: []Classification{}, Contributions: []Contribution{}, Missing: []string{},
		Advice: "recheck", Reasons: []string{}, RequiredFailed: []string{}, RequiredUnknown: []string{}, Preferences: preferences}
	r := Report{}
	if report != nil {
		r = *report
	}
	counts, seen := map[string]int{}, map[string]bool{}
	total := 0
	for _, evidence := range r.UsageTypes {
		// Reports stored before terminal formatting was stripped can still be
		// displayed. Keep their age/provenance checks unchanged for scoring.
		evidence.Value = normalizedValue(evidence.Value, 48)
		a.TypeEvidence = append(a.TypeEvidence, evidence)
		kind := usageType(evidence.Value)
		if kind == "" || seen[evidence.Source] || !r.sourceFresh(evidence.Source, now) {
			continue
		}
		seen[evidence.Source] = true
		counts[kind]++
		total++
	}
	for _, rule := range typeRules {
		if counts[rule.name] >= 2 && counts[rule.name]*3 >= total*2 {
			a.IPType = rule.name
		}
	}
	possible := []typeRule{}
	for _, rule := range typeRules {
		// A single vote cannot exclude other types. Conflicting multiple votes
		// constrain the interval to the types actually supported by evidence.
		if rule.name == a.IPType || a.IPType == "unknown" && (total < 2 || counts[rule.name] > 0) {
			possible = append(possible, rule)
			a.TypeCandidates = append(a.TypeCandidates, rule.name)
		}
	}
	typePart := Contribution{ID: "type", Weight: 20, Min: 20, Missing: []string{}}
	for _, rule := range possible {
		typePart.Min = math.Min(typePart.Min, rule.points)
		typePart.Max = math.Max(typePart.Max, rule.points)
	}
	if a.IPType == "unknown" {
		typePart.Missing = append(typePart.Missing, "type")
	}
	a.Contributions = append(a.Contributions, typePart)

	risk := Contribution{ID: "sources", Weight: 25, Missing: []string{}}
	for _, source := range scoreWeights {
		value, valid := 0.0, false
		for _, raw := range r.Scores {
			if raw.Source != source.name {
				continue
			}
			parsed, err := strconv.ParseFloat(raw.Value, 64)
			if err == nil && !math.IsNaN(parsed) && parsed >= 0 && parsed <= 100 && r.sourceFresh(source.name, now) {
				value, valid = source.weight*(1-parsed/100), true
			}
		}
		if valid {
			risk.Min += value
			risk.Max += value
		} else {
			risk.Max += source.weight
			risk.Missing = append(risk.Missing, source.name)
		}
	}
	a.Contributions = append(a.Contributions, risk)
	ipPure := Contribution{ID: "ippure", Weight: 25, Max: 25, Missing: []string{"IPPure"}}
	if p := r.IPPure; p != nil && p.Status == "ok" && p.Provider == IPPureProvider && p.RiskScore != nil && *p.RiskScore >= 0 && *p.RiskScore <= 100 && sameIP(p.Address, r.Address) && freshAt(p.CheckedAt, now) {
		ipPure.Min = 25 * (1 - *p.RiskScore/100)
		ipPure.Max, ipPure.Missing = ipPure.Min, []string{}
	}
	a.Contributions = append(a.Contributions, ipPure)
	unlock := Contribution{ID: "unlock", Weight: 30, Missing: []string{}}
	for _, item := range unlockWeights {
		switch serviceState(r, item.name, preferences.TargetRegion, now) {
		case "yes":
			unlock.Min += item.weight
			unlock.Max += item.weight
		case "unknown":
			unlock.Max += item.weight
			unlock.Missing = append(unlock.Missing, item.name)
		}
	}
	a.Contributions = append(a.Contributions, unlock)
	for _, name := range preferences.RequiredServices {
		switch serviceState(r, name, preferences.TargetRegion, now) {
		case "no":
			a.RequiredFailed = append(a.RequiredFailed, name)
		case "unknown":
			a.RequiredUnknown = append(a.RequiredUnknown, name)
		}
	}
	for _, part := range a.Contributions {
		a.Missing = append(a.Missing, part.Missing...)
	}
	// A selected zero-weight service (Reddit) still needs evidence for a
	// comparable result, even though it does not change the fixed formula.
	for _, name := range a.RequiredUnknown {
		if !slices.Contains(a.Missing, name) {
			a.Missing = append(a.Missing, name)
		}
	}
	lo, hi := 100.0, 0.0
	for _, rule := range possible {
		lo = math.Min(lo, math.Min(rule.cap, rule.points+risk.Min+ipPure.Min+unlock.Min))
		hi = math.Max(hi, math.Min(rule.cap, rule.points+risk.Max+ipPure.Max+unlock.Max))
	}
	a.Min, a.Max = roundedScore(lo), roundedScore(hi)
	var expiry time.Time
	if stamp, err := time.Parse(time.RFC3339Nano, checkedAt); err == nil {
		expiry = stamp.Add(EvidenceMaxAge)
	}
	// The earliest successful observation determines when this assessment must
	// be refreshed, not the later receipt time at the Center.
	for _, observation := range r.Observations {
		if observation.Status != "ok" {
			continue
		}
		if stamp, err := time.Parse(time.RFC3339Nano, observation.CheckedAt); err == nil {
			until := stamp.Add(EvidenceMaxAge)
			if expiry.IsZero() || until.Before(expiry) {
				expiry = until
			}
		}
	}
	if p := r.IPPure; p != nil && p.Status == "ok" {
		if stamp, err := time.Parse(time.RFC3339Nano, p.CheckedAt); err == nil {
			until := stamp.Add(EvidenceMaxAge)
			if expiry.IsZero() || until.Before(expiry) {
				expiry = until
			}
		}
	}
	if !expiry.IsZero() {
		a.ValidUntil = expiry.UTC().Format(time.RFC3339Nano)
	}
	if changed {
		a.Status = "ip_changed"
		a.Reasons = append(a.Reasons, "ip_changed")
		return a
	}
	if report != nil && (!freshAt(checkedAt, now) || !expiry.IsZero() && now.After(expiry)) {
		a.Status = "expired"
		a.Reasons = append(a.Reasons, "expired")
		return a
	}
	if len(a.Missing) == 0 {
		a.Status = "complete"
		score := a.Min
		a.Score = &score
		a.Grade = grade(score)
	} else if len(a.Missing) == 1 && a.Missing[0] == "IPQS" {
		// A missing IPQS response contributes its worst possible zero points.
		// Keep the evidence interval and missing source visible; this is a
		// conservative decision score, never an inferred IPQS risk value.
		a.Status = "conservative"
		score := a.Min
		a.Score = &score
		a.Grade = grade(score)
	}
	// A confirmed required-service failure is actionable even when another
	// provider is missing; missing-only evidence never asserts failure.
	if len(a.RequiredFailed) > 0 {
		a.Advice = "compare"
		a.Reasons = append(a.Reasons, "required_failed")
	} else if len(a.RequiredUnknown) > 0 || a.Score == nil {
		a.Reasons = append(a.Reasons, "incomplete")
	} else if *a.Score < 60 {
		a.Advice = "compare"
		a.Reasons = append(a.Reasons, "low_score")
	} else {
		a.Advice = "direct"
		a.Reasons = append(a.Reasons, "requirements_met")
	}
	return a
}

func serviceState(r Report, name, region string, now time.Time) string {
	if !r.sourceFresh("service:"+name, now) {
		return "unknown"
	}
	for _, item := range r.Services {
		if item.Name != name {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "no", "org", "originals only":
			return "no"
		case "yes":
			if region != "" && item.RegionCode == "" {
				return "unknown"
			}
			if region != "" && item.RegionCode != region {
				return "no"
			}
			return "yes"
		}
	}
	return "unknown"
}

func grade(score int) string {
	switch {
	case score >= 90:
		return "excellent"
	case score >= 80:
		return "premium"
	case score >= 60:
		return "good"
	case score >= 40:
		return "fair"
	default:
		return "poor"
	}
}

type Comparison struct {
	NodeID             string     `json:"nodeId"`
	Name               string     `json:"name"`
	Compatible         bool       `json:"compatible"`
	ConnectionVerified bool       `json:"connectionVerified"`
	Recommended        bool       `json:"recommended"`
	Reason             string     `json:"reason"`
	Delta              *int       `json:"delta,omitempty"`
	Assessment         Assessment `json:"assessment"`
	Services           []Service  `json:"services"`
}

func Compare(current, candidate Assessment, compatible bool) (bool, string, *int) {
	if !compatible {
		return false, "connection_unverified", nil
	}
	if current.Status == "ip_changed" || current.Status == "expired" || candidate.Status != "complete" && candidate.Status != "conservative" || candidate.Score == nil {
		return false, "recheck", nil
	}
	if current.Version != candidate.Version || !slices.Equal(current.Preferences.RequiredServices, candidate.Preferences.RequiredServices) || current.Preferences.TargetRegion != candidate.Preferences.TargetRegion {
		return false, "recheck", nil
	}
	var delta *int
	if current.Score != nil {
		// A candidate's minimum must beat the current maximum. This also
		// remains exact when both assessments are complete.
		value := candidate.Min - current.Max
		delta = &value
	}
	if len(candidate.RequiredFailed)+len(candidate.RequiredUnknown) > 0 {
		return false, "requirements_not_met", delta
	}
	if len(current.RequiredFailed) > 0 {
		return true, "unlocks_improved", delta
	}
	if len(current.RequiredUnknown) > 0 || current.Score == nil {
		return false, "recheck", delta
	}
	if delta != nil && *delta >= 10 {
		return true, "score_improved", delta
	}
	return false, "no_clear_improvement", delta
}
