package agent

import (
	"testing"
	"time"

	"github.com/petauron/vastora/internal/ipquality"
)

func TestIPPureRequiresValidScoreAndMatchingIP(t *testing.T) {
	now := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ name, body, status string }{
		{"zero", `{"ip":"203.0.113.8","fraudScore":0}`, "ok"},
		{"high", `{"ip":"203.0.113.8","fraudScore":100,"isResidential":false}`, "ok"},
		{"missing", `{"ip":"203.0.113.8"}`, "invalid_response"},
		{"null", `{"ip":"203.0.113.8","fraudScore":null}`, "invalid_response"},
		{"string", `{"ip":"203.0.113.8","fraudScore":"12"}`, "invalid_response"},
		{"out of range", `{"ip":"203.0.113.8","fraudScore":101}`, "invalid_response"},
		{"mismatch", `{"ip":"203.0.113.9","fraudScore":0}`, "ip_mismatch"},
		{"html", `<html>challenge</html>`, "invalid_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := parseIPPure([]byte(tc.body), "203.0.113.8", now)
			if value.Status != tc.status || value.Provider != ipquality.IPPureProvider {
				t.Fatalf("unexpected result: %+v", value)
			}
			if tc.status != "ok" && value.RiskScore != nil {
				t.Fatal("failure retained a usable risk score")
			}
			if tc.name == "zero" && value.Residential != nil {
				t.Fatal("missing residential value became false")
			}
		})
	}
}

func TestIPPureIPv6DoesNotQueryOrSubstituteIPv4(t *testing.T) {
	value := missingIPPure("2001:4860:4860::8888", time.Now())
	if value.Status != "unsupported" || value.RiskScore != nil {
		t.Fatalf("unexpected IPv6 result: %+v", value)
	}
}
