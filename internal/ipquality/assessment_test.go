package ipquality

import (
	"slices"
	"testing"
	"time"
)

var assessmentTime = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

func assessmentReport(kind string) Report {
	zero := 0.0
	r := Report{Address: "203.0.113.8", Version: "test", UsageTypes: []Classification{{"IPinfo", kind}, {"ipregistry", kind}},
		Scores: []Score{{"SCAMALYTICS", "0"}, {"IPQS", "0"}, {"AbuseIPDB", "0"}},
		IPPure: &IPPureResult{Provider: IPPureProvider, Status: "ok", Address: "203.0.113.8", RiskScore: &zero, CheckedAt: assessmentTime.Format(time.RFC3339Nano)}}
	for _, name := range services {
		r.Services = append(r.Services, Service{Name: name, Status: "Yes", RegionCode: "US", Type: "Native"})
	}
	r.RecordObservations(assessmentTime)
	return r
}

func assessFixture(r Report) Assessment {
	return Assess(&r, assessmentTime.Format(time.RFC3339Nano), false, assessmentTime, DefaultPreferences())
}

func TestAssessmentTypeCaps(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want int
	}{{"ISP", 100}, {"Mobile ISP", 95}, {"Business", 89}, {"Hosting", 79}} {
		t.Run(tc.kind, func(t *testing.T) {
			r := assessmentReport(tc.kind)
			if err := (Result{Report: &r}).Validate(r.Address); err != nil {
				t.Fatal(err)
			}
			a := assessFixture(r)
			if a.Score == nil || *a.Score != tc.want || a.Advice != "direct" || a.Status != "complete" {
				t.Fatalf("unexpected assessment: %+v", a)
			}
		})
	}
}

func TestAssessmentMissingNeverBecomesClean(t *testing.T) {
	r := assessmentReport("ISP")
	r.IPPure = nil
	a := assessFixture(r)
	if a.Score != nil || a.Min != 75 || a.Max != 100 || a.Advice != "recheck" || !slices.Contains(a.Missing, "IPPure") {
		t.Fatalf("missing IPPure: %+v", a)
	}
	r.Scores = r.Scores[1:]
	a = assessFixture(r)
	if a.Min != 65 || a.Max != 100 || !slices.Contains(a.Missing, "SCAMALYTICS") {
		t.Fatalf("missing source: %+v", a)
	}
	r = assessmentReport("Hosting")
	r.Scores = r.Scores[1:]
	a = assessFixture(r)
	if a.Score != nil || a.Status != "partial" || a.Min != 75 || a.Max != 79 {
		t.Fatalf("cap concealed missing source: %+v", a)
	}
	// Even when a cap collapses both bounds, incompleteness must remain visible.
	r = assessmentReport("Hosting")
	r.Scores = r.Scores[:2]
	a = assessFixture(r)
	if a.Score != nil || a.Min != 79 || a.Max != 79 {
		t.Fatalf("missing abuse evidence was ranked: %+v", a)
	}
}

func TestAssessmentIPQSOnlyConservativeScore(t *testing.T) {
	r := assessmentReport("Hosting")
	r.Scores = []Score{{"SCAMALYTICS", "0"}, {"AbuseIPDB", "14"}}
	risk := 46.0
	r.IPPure.RiskScore = &risk
	r.RecordObservations(assessmentTime)
	a := assessFixture(r)
	if a.Version != "meridian-v4" || a.Status != "conservative" || a.Score == nil || *a.Score != 63 || a.Min != 63 || a.Max != 73 || a.Grade != "good" || a.Advice != "direct" || !slices.Equal(a.Missing, []string{"IPQS"}) {
		t.Fatalf("IPQS-only absence must produce a transparent lower bound: %+v", a)
	}
	r.Scores = append(r.Scores, Score{"IPQS", "100"})
	r.RecordObservations(assessmentTime)
	if full := assessFixture(r); full.Status != "complete" || full.Score == nil || *full.Score != 63 || len(full.Missing) != 0 {
		t.Fatalf("valid IPQS score did not restore complete assessment: %+v", full)
	}
	r.Scores = r.Scores[:2]
	r.IPPure = nil
	r.RecordObservations(assessmentTime)
	if partial := assessFixture(r); partial.Status != "partial" || partial.Score != nil {
		t.Fatalf("additional missing evidence got a conservative score: %+v", partial)
	}
	r = assessmentReport("Hosting")
	r.Scores = []Score{{"SCAMALYTICS", "0"}, {"AbuseIPDB", "14"}}
	r.RecordObservations(assessmentTime)
	if expired := Assess(&r, assessmentTime.Format(time.RFC3339Nano), false, assessmentTime.Add(EvidenceMaxAge+time.Second), DefaultPreferences()); expired.Status != "expired" || expired.Score != nil {
		t.Fatalf("expired conservative score remained usable: %+v", expired)
	}
}

func TestAssessmentTypeEvidenceAndConflict(t *testing.T) {
	r := assessmentReport("Hosting")
	r.UsageTypes[0].Value = `x1b[41mx1b[37m Hosting x1b[0m`
	if a := assessFixture(r); a.TypeEvidence[0].Value != "Hosting" || a.IPType != "hosting" {
		t.Fatalf("stored terminal formatting leaked into evidence: %+v", a)
	}
	r.UsageTypes[0].Value = "Hosting"
	r.CompanyTypes = []Classification{{"IPinfo", "ISP"}, {"ipregistry", "ISP"}}
	if a := assessFixture(r); a.IPType != "hosting" {
		t.Fatalf("company ISP voted as usage: %+v", a)
	}
	r.UsageTypes[1].Value = "ISP"
	a := assessFixture(r)
	if a.Score == nil || *a.Score != 79 || a.Status != "conservative" || a.Min != 79 || a.Max != 100 || a.IPType != "unknown" || !slices.Contains(a.Missing, "type") {
		t.Fatalf("conflicting type: %+v", a)
	}
	r.UsageTypes = append(r.UsageTypes, Classification{"ipapi", "Hosting"})
	r.RecordObservations(assessmentTime)
	if a := assessFixture(r); a.IPType != "hosting" {
		t.Fatalf("plurality rejected: %+v", a)
	}
	r.UsageTypes = r.UsageTypes[:1]
	if a := assessFixture(r); a.IPType != "hosting" || len(a.TypeCandidates) != 1 || a.Score == nil {
		t.Fatalf("single available provider not used: %+v", a)
	}
	r.UsageTypes = []Classification{{"IPinfo", "Business"}, {"ipregistry", "Business"}, {"ipapi", "Hosting"}, {"AbuseIPDB", "Business"}, {"IP2LOCATION", "Hosting"}}
	r.Scores = slices.DeleteFunc(r.Scores, func(s Score) bool { return s.Source == "IPQS" })
	r.RecordObservations(assessmentTime)
	a = assessFixture(r)
	if a.Status != "conservative" || a.Score == nil || *a.Score != 80 || a.Max != 89 || a.IPType != "business" || !slices.Equal(a.Missing, []string{"IPQS"}) {
		t.Fatalf("type conflict and unavailable IPQS did not retain a bounded score: %+v", a)
	}
	if a := Assess(nil, "", false, assessmentTime, DefaultPreferences()); a.Score != nil || a.Status != "partial" {
		t.Fatalf("no report received a synthetic score: %+v", a)
	}
}

func TestAssessmentRiskAndServiceSemantics(t *testing.T) {
	r := assessmentReport("ISP")
	for i := range r.Scores {
		r.Scores[i].Value = "100"
	}
	high := 100.0
	r.IPPure.RiskScore = &high
	a := assessFixture(r)
	if a.Score == nil || *a.Score != 50 || a.Advice != "compare" {
		t.Fatalf("residential hid risk: %+v", a)
	}
	r = assessmentReport("ISP")
	for i := range r.Services {
		if r.Services[i].Name == "ChatGPT" {
			r.Services[i].Status = "No"
		}
	}
	a = assessFixture(r)
	if a.Score == nil || *a.Score != 85 || a.Advice != "compare" {
		t.Fatalf("required failure ignored: %+v", a)
	}
	r.IPPure = nil
	if a := assessFixture(r); a.Advice != "compare" {
		t.Fatalf("missing source hid known failure: %+v", a)
	}
	for i := range r.Services {
		if r.Services[i].Name == "ChatGPT" {
			r.Services[i].Status = "Failed"
		}
	}
	a = assessFixture(r)
	if a.Advice != "recheck" || len(a.RequiredFailed) != 0 || !slices.Contains(a.RequiredUnknown, "ChatGPT") {
		t.Fatalf("probe failure became blocked service: %+v", a)
	}
}

func TestAssessmentIPQualityDisplayStatuses(t *testing.T) {
	for _, tc := range []struct {
		service string
		status  string
		weight  int
	}{
		{"DisneyPlus", "Block", 4},
		{"Netflix", "NF.Only", 6},
		{"ChatGPT", "APPOnly", 15},
		{"ChatGPT", "WebOnly", 15},
		{"Youtube", "China", 2},
		{"Youtube", "NoPrem.", 2},
	} {
		t.Run(tc.status, func(t *testing.T) {
			r := assessmentReport("ISP")
			for i := range r.Services {
				if r.Services[i].Name == tc.service {
					r.Services[i].Status = tc.status
				}
			}
			r.Scores = slices.DeleteFunc(r.Scores, func(s Score) bool { return s.Source == "IPQS" })
			r.RecordObservations(assessmentTime)
			a := assessFixture(r)
			if a.Status != "conservative" || a.Score == nil || *a.Score != 90-tc.weight || !slices.Equal(a.Missing, []string{"IPQS"}) {
				t.Fatalf("known restriction became missing or unlocked: %+v", a)
			}
			if slices.Contains(DefaultPreferences().RequiredServices, tc.service) && (a.Advice != "compare" || !slices.Contains(a.RequiredFailed, tc.service)) {
				t.Fatalf("required restriction did not suggest comparison: %+v", a)
			}
		})
	}
	for _, status := range []string{"Failed", "Pending", "Unrecognized"} {
		r := assessmentReport("ISP")
		for i := range r.Services {
			if r.Services[i].Name == "ChatGPT" {
				r.Services[i].Status = status
			}
		}
		r.RecordObservations(assessmentTime)
		a := assessFixture(r)
		if a.Score != nil || a.Advice != "recheck" || len(a.RequiredFailed) != 0 || !slices.Contains(a.RequiredUnknown, "ChatGPT") {
			t.Fatalf("unknown probe result became negative evidence: %+v", a)
		}
	}
}

func TestAssessmentRegionAndFreshness(t *testing.T) {
	r := assessmentReport("ISP")
	p := DefaultPreferences()
	p.TargetRegion = "JP"
	a := Assess(&r, assessmentTime.Format(time.RFC3339Nano), false, assessmentTime, p)
	if a.Score == nil || *a.Score != 70 || len(a.RequiredFailed) != 3 || a.Advice != "compare" {
		t.Fatalf("region ignored: %+v", a)
	}
	for i := range r.Services {
		r.Services[i].RegionCode = ""
	}
	a = Assess(&r, assessmentTime.Format(time.RFC3339Nano), false, assessmentTime, p)
	if a.Score != nil || a.Min != 70 || a.Max != 100 || a.Advice != "recheck" {
		t.Fatalf("unknown region treated as failure: %+v", a)
	}
	for _, tc := range []struct {
		changed bool
		now     time.Time
		status  string
	}{{true, assessmentTime, "ip_changed"}, {false, assessmentTime.Add(24*time.Hour + time.Second), "expired"}} {
		a := Assess(&r, assessmentTime.Format(time.RFC3339Nano), tc.changed, tc.now, DefaultPreferences())
		if a.Status != tc.status || a.Score != nil || a.Advice != "recheck" {
			t.Fatalf("stale score remained usable: %+v", a)
		}
	}
	r = assessmentReport("ISP")
	r.Observations = nil
	if a := assessFixture(r); a.Score != nil {
		t.Fatal("old report without provenance got a formal score")
	}
	r = assessmentReport("ISP")
	r.IPPure.Address = "203.0.113.9"
	if a := assessFixture(r); a.Score != nil || !slices.Contains(a.Missing, "IPPure") {
		t.Fatal("IPPure from another IP was accepted")
	}
}

func TestComparisonRequiresEvidenceAndMeaningfulImprovement(t *testing.T) {
	good := assessFixture(assessmentReport("ISP"))
	base := assessFixture(assessmentReport("Mobile"))
	if yes, reason, delta := Compare(base, good, true); yes || reason != "no_clear_improvement" || delta == nil || *delta != 5 {
		t.Fatal("small improvement recommended", yes, reason, delta)
	}
	base = assessFixture(assessmentReport("Hosting"))
	if yes, _, _ := Compare(base, good, true); !yes {
		t.Fatal("21 point improvement not recommended")
	}
	if yes, reason, _ := Compare(base, good, false); yes || reason != "connection_unverified" {
		t.Fatal("unverified connection recommended")
	}
	r := assessmentReport("ISP")
	for i := range r.Services {
		if r.Services[i].Name == "Netflix" {
			r.Services[i].Status = "Org"
		}
	}
	base = assessFixture(r)
	if yes, reason, _ := Compare(base, good, true); !yes || reason != "unlocks_improved" {
		t.Fatal("unlock improvement below 10 ignored")
	}
	r.IPPure = nil
	partial := assessFixture(r)
	if yes, _, _ := Compare(base, partial, true); yes {
		t.Fatal("partial candidate recommended")
	}
}

func TestComparisonWithConservativeBounds(t *testing.T) {
	r := assessmentReport("Hosting")
	r.Scores = []Score{{"SCAMALYTICS", "0"}, {"AbuseIPDB", "14"}}
	risk := 46.0
	r.IPPure.RiskScore = &risk
	r.RecordObservations(assessmentTime)
	base := assessFixture(r) // 63–73
	r = assessmentReport("Business")
	r.Scores = []Score{{"SCAMALYTICS", "0"}, {"AbuseIPDB", "0"}}
	r.RecordObservations(assessmentTime)
	if yes, reason, delta := Compare(base, assessFixture(r), true); yes || reason != "no_clear_improvement" || delta == nil || *delta != 7 {
		t.Fatal("uncertain improvement was recommended", yes, reason, delta)
	}
	r.UsageTypes = []Classification{{"IPinfo", "ISP"}, {"ipregistry", "ISP"}}
	r.RecordObservations(assessmentTime)
	if yes, reason, delta := Compare(base, assessFixture(r), true); !yes || reason != "score_improved" || delta == nil || *delta != 17 {
		t.Fatal("certain conservative improvement was missed", yes, reason, delta)
	}
}

func TestAssessmentSelectedUnweightedServiceMustBeKnown(t *testing.T) {
	r := assessmentReport("ISP")
	for i := range r.Services {
		if r.Services[i].Name == "Reddit" {
			r.Services[i].Status = "Failed"
		}
	}
	p := DefaultPreferences()
	p.RequiredServices = append(p.RequiredServices, "Reddit")
	a := Assess(&r, assessmentTime.Format(time.RFC3339Nano), false, assessmentTime, p)
	if a.Score != nil || a.Advice != "recheck" || !slices.Contains(a.Missing, "Reddit") {
		t.Fatal("unknown selected service entered ranking")
	}
}

func ipv6AssessmentReport(kind string) Report {
	r := assessmentReport(kind)
	r.Address = "2001:db8::8"
	r.IPPure = nil
	r.Scores = []Score{{"IPQS", "0"}, {"AbuseIPDB", "0"}}
	r.RecordObservations(assessmentTime)
	return r
}

func TestIPv6AssessmentWeightsAndCaps(t *testing.T) {
	for _, tc := range []struct {
		kind string
		want int
	}{{"ISP", 100}, {"Mobile ISP", 95}, {"Business", 88}, {"Hosting", 79}} {
		t.Run(tc.kind, func(t *testing.T) {
			a := assessFixture(ipv6AssessmentReport(tc.kind))
			if a.Version != IPv6AssessmentVersion || a.Score == nil || *a.Score != tc.want || a.Status != "complete" || len(a.Contributions) != 3 {
				t.Fatalf("IPv6 weights/caps: %+v", a)
			}
		})
	}
	r := ipv6AssessmentReport("ISP")
	r.Scores = []Score{{"IPQS", "100"}, {"AbuseIPDB", "100"}}
	a := assessFixture(r)
	if a.Score == nil || *a.Score != 75 {
		t.Fatalf("residential identity must not erase risk: %+v", a)
	}
}

func TestIPv6MissingEvidenceContributesZero(t *testing.T) {
	r := ipv6AssessmentReport("ISP")
	r.Scores = nil
	for i := range r.Services {
		switch r.Services[i].Name {
		case "AmazonPrimeVideo":
			r.Services[i].Status = "Block"
		case "TikTok":
			r.Services[i].Status = "Failed"
		}
	}
	r.RecordObservations(assessmentTime)
	a := assessFixture(r)
	if a.Score == nil || *a.Score != 71 || a.Status != "conservative" || a.Advice != "direct" || !slices.Equal(a.Missing, []string{"IPQS", "AbuseIPDB", "TikTok"}) {
		t.Fatalf("missing supported evidence must not prevent a numeric score: %+v", a)
	}
	r.UsageTypes = nil
	r.RecordObservations(assessmentTime)
	a = assessFixture(r)
	if a.Score == nil || *a.Score != 41 || a.IPType != "unknown" || a.Contributions[0].Min != 0 {
		t.Fatalf("unknown type must not receive free points: %+v", a)
	}
	for i := range r.Services {
		if r.Services[i].Name == "ChatGPT" {
			r.Services[i].Status = "Block"
		}
	}
	r.RecordObservations(assessmentTime)
	if a = assessFixture(r); a.Advice != "compare" || !slices.Contains(a.RequiredFailed, "ChatGPT") {
		t.Fatalf("required failure must remain actionable: %+v", a)
	}
}

func TestIPv6EvidenceBoundaries(t *testing.T) {
	r := ipv6AssessmentReport("ISP")
	for i := range r.Observations {
		if r.Observations[i].Source == "IPQS" {
			r.Observations[i].Address = "203.0.113.8"
		}
	}
	if a := assessFixture(r); a.Score == nil || *a.Score != 80 || !slices.Contains(a.Missing, "IPQS") {
		t.Fatalf("IPv4 evidence must not score IPv6: %+v", a)
	}
	r = ipv6AssessmentReport("ISP")
	r.IPPure = assessmentReport("ISP").IPPure
	r.IPPure.CheckedAt = assessmentTime.Add(-48 * time.Hour).Format(time.RFC3339Nano)
	for i := range r.Observations {
		if r.Observations[i].Source == "SCAMALYTICS" {
			r.Observations[i].Status = "ok"
			r.Observations[i].CheckedAt = r.IPPure.CheckedAt
		}
	}
	if a := assessFixture(r); a.Score == nil || *a.Score != 100 || a.Status != "complete" {
		t.Fatalf("excluded providers must not affect IPv6 freshness: %+v", a)
	}
	stamp := assessmentTime.Format(time.RFC3339Nano)
	if a := Assess(&r, stamp, true, assessmentTime, DefaultPreferences()); a.Score != nil || a.Status != "ip_changed" {
		t.Fatalf("changed IP retained score: %+v", a)
	}
	if a := Assess(&r, stamp, false, assessmentTime.Add(25*time.Hour), DefaultPreferences()); a.Score != nil || a.Status != "expired" {
		t.Fatalf("expired report retained score: %+v", a)
	}
	if a := Assess(nil, "", false, assessmentTime, DefaultPreferences()); a.Score != nil {
		t.Fatalf("absent report acquired score: %+v", a)
	}
	v4, v6 := assessFixture(assessmentReport("Hosting")), assessFixture(ipv6AssessmentReport("ISP"))
	if recommended, _, delta := Compare(v4, v6, true); recommended || delta != nil {
		t.Fatal("different scoring rules must not produce a score improvement recommendation")
	}
}

func TestHistoricalTypeKeepsSameIPProvenance(t *testing.T) {
	r := assessmentReport("ISP")
	stamp := assessmentTime.Format(time.RFC3339Nano)
	a := Assess(&r, stamp, false, assessmentTime.Add(25*time.Hour), DefaultPreferences())
	if a.Status != "expired" || a.IPType != "residential" || a.Score != nil {
		t.Fatalf("historical type should survive without reviving expired score: %+v", a)
	}
	for i := range r.Observations {
		r.Observations[i].Address = "203.0.113.9"
	}
	a = Assess(&r, stamp, false, assessmentTime.Add(25*time.Hour), DefaultPreferences())
	if a.IPType != "unknown" {
		t.Fatalf("different IP supplied historical type: %+v", a)
	}
}
