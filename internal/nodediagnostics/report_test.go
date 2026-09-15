package nodediagnostics

import "testing"

func TestHostProfileRecommendationReasonsAreBounded(t *testing.T) {
	result := Result{Host: &HostProfile{
		CPUCount: 1, MemoryBytes: 1, DiskBytes: 1,
		Recommendations: []ParameterRecommendation{{Parameter: "tcp_congestion_control", Current: "cubic", Value: "cubic", Reason: "preserve_current"}},
	}}
	if err := result.Validate(HostProfileKind); err != nil {
		t.Fatalf("valid host profile rejected: %v", err)
	}
	result.Host.Recommendations[0].Reason = "agent_defined_reason"
	if err := result.Validate(HostProfileKind); err == nil {
		t.Fatal("unexpected recommendation reason accepted")
	}
}
