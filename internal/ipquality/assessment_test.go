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
	if a.Version != "meridian-v2" || a.Status != "conservative" || a.Score == nil || *a.Score != 63 || a.Min != 63 || a.Max != 73 || a.Grade != "good" || a.Advice != "direct" || !slices.Equal(a.Missing, []string{"IPQS"}) {
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
	if a.Score != nil || a.Min != 79 || a.Max != 100 || a.IPType != "unknown" {
		t.Fatalf("conflicting type: %+v", a)
	}
	r.UsageTypes = append(r.UsageTypes, Classification{"ipapi", "Hosting"})
	r.RecordObservations(assessmentTime)
	if a := assessFixture(r); a.IPType != "hosting" {
		t.Fatalf("two-thirds consensus rejected: %+v", a)
	}
	r.UsageTypes = r.UsageTypes[:1]
	if a := assessFixture(r); a.IPType != "unknown" || len(a.TypeCandidates) != 4 {
		t.Fatalf("single provider established type: %+v", a)
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
