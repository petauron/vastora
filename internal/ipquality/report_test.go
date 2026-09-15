package ipquality

import (
	"encoding/json"
	"strings"
	"testing"
)

const reportFixture = `{"Head":{"IP":"203.0.113.8","Version":"v2026-09-04","Command":"do-not-retain"},"Info":{"ASN":"979","Organization":"NetLab Global","TimeZone":"America/Chicago","City":{"Name":"Chicago"},"Region":{"Code":"US","Name":"United States"},"RegisteredRegion":{"Code":"US","Name":"United States"},"Coordinates":"do-not-retain"},"Type":{"Usage":{"IPinfo":"Commercial","ipregistry":"Residential"},"Company":{"IPinfo":"Commercial"}},"Score":{"IP2LOCATION":"3","IPQS":"75","ipapi":"1.75%","AbuseIPDB":"0","SCAMALYTICS":"null"},"RiskFactor":{"IPinfo":{"Proxy":false,"VPN":true},"DBIP":{"Robot":false}},"Media":{"Netflix":{"Status":" Yes ","Region":"CA","Type":"Native"},"ChatGPT":{"Status":"null"}},"Mail":{"private":"do-not-retain"}}`

func TestParseRetainsOnlyAvailableScoresAndUnlockFields(t *testing.T) {
	value, err := Parse([]byte("\r"+reportFixture+"\n"), "203.0.113.8")
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Scores) != 4 || value.Scores[2].Source != "AbuseIPDB" || value.Scores[2].Value != "0" {
		t.Fatalf("missing and zero scores conflated: %#v", value.Scores)
	}
	if len(value.Services) != 2 || value.Services[0].Status != "Yes" || value.Services[0].RegionCode != "CA" {
		t.Fatalf("invalid unlock result: %#v", value.Services)
	}
	if value.ASN != "AS979" || value.Organization != "NetLab Global" || value.City != "Chicago" || len(value.UsageTypes) != 2 || len(value.CompanyTypes) != 1 || len(value.RiskFactors) != 3 {
		t.Fatalf("structured intelligence missing: %#v", value)
	}
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "do-not-retain") {
		t.Fatal("raw report data retained")
	}
}

func TestParseRejectsWrongExitAndInvalidReports(t *testing.T) {
	for _, raw := range []string{strings.ReplaceAll(reportFixture, "203.0.113.8", "203.0.113.9"), "not-json", strings.Repeat("x", MaxReportBytes+1)} {
		if _, err := Parse([]byte(raw), "203.0.113.8"); err == nil {
			t.Fatal("invalid report accepted")
		}
	}
	if err := (Result{Report: &Report{Address: "not-ip", Version: "test"}}).Validate("not-ip"); err == nil {
		t.Fatal("invalid identity accepted")
	}
	if err := (Result{Error: "timeout"}).Validate("203.0.113.8"); err != nil {
		t.Fatal(err)
	}
}
