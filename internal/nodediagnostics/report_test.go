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

func TestMeridianLinkRequiresPrivatePeersAndTimedSamples(t *testing.T) {
	task := Task{Link: &LinkBandwidthTask{SourceNodeID: "entry", LandingNodeID: "landing", SourceIP: "100.64.0.8", LandingIP: "100.64.0.9", Port: 30000}}
	if err := task.ValidateLinkBandwidth(LinkBandwidthKind); err != nil {
		t.Fatal(err)
	}
	task.Link.LandingIP = "203.0.113.9"
	if err := task.ValidateLinkBandwidth(LinkBandwidthKind); err == nil {
		t.Fatal("public target accepted")
	}
	task.Link.LandingIP = "not-an-address"
	if err := task.ValidateLinkBandwidth(LinkBandwidthKind); err == nil {
		t.Fatal("invalid target accepted")
	}
	task.Link.LandingIP = "100.64.0.9"
	result := Result{Link: &LinkBandwidthMeasurement{SourceNodeID: "entry", LandingNodeID: "landing", UploadMbps: 10, DownloadMbps: 20, UploadBytes: 12_500_000, DownloadBytes: 25_000_000, UploadSeconds: 10, DownloadSeconds: 10}}
	if err := result.Validate(LinkBandwidthKind); err != nil {
		t.Fatal(err)
	}
	result.Link.UploadSeconds = 1
	if err := result.Validate(LinkBandwidthKind); err == nil {
		t.Fatal("short sample accepted")
	}
}
