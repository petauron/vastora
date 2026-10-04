package center

import (
	"context"
	"testing"
)

func TestAgentReinstallCompletionManualDNSRequiresEveryCurrentEntry(t *testing.T) {
	dns := &AgentReinstallDNS{Current: true, State: "pending", Entries: []AgentReinstallDNSEntry{{PublicationID: "entry", Hostname: "entry.example.test", Address: "1.1.1.1", Provider: "manual", State: "manual"}}}
	check := &AgentReinstallEntryCheck{Current: true, State: "passed", Entries: []AgentReinstallEntryCheckResult{{PublicationID: "entry", Hostname: "entry.example.test", PublicAddress: "1.1.1.1", State: "passed"}}}
	if !reinstallCompletionDNSVerified(dns, check) {
		t.Fatal("current manual DNS verification rejected")
	}
	for _, mode := range []string{"address", "publication", "stale", "missing", "provider"} {
		t.Run(mode, func(t *testing.T) {
			changed := *check
			changed.Entries = append([]AgentReinstallEntryCheckResult{}, check.Entries...)
			d := *dns
			d.Entries = append([]AgentReinstallDNSEntry{}, dns.Entries...)
			switch mode {
			case "address":
				changed.Entries[0].PublicAddress = "8.8.8.8"
			case "publication":
				changed.Entries[0].PublicationID = "another"
			case "stale":
				changed.Current = false
			case "missing":
				changed.Entries = nil
			case "provider":
				d.Entries[0].Provider = "cloudflare"
			}
			if reinstallCompletionDNSVerified(&d, &changed) {
				t.Fatal("unverified DNS accepted")
			}
		})
	}
}

func TestAgentReinstallCompletionActivatesApprovedNetworkWithoutApplications(t *testing.T) {
	s, node, heartbeat := replacementNetworkFixture(t)
	ctx := context.Background()
	if err := s.RecordAgentHeartbeat(ctx, node.ID, node.Credential, heartbeat); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveAgentReinstallNetwork(ctx, node.ID, "reinstall-review-admin", networkApprovalInput(t, s, node.ID)); err != nil {
		t.Fatal(err)
	}
	p, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := AgentReinstallCompleteInput{OperationID: p.Recovery.ID, PlanRevision: p.Revision}
	if _, err := s.CompleteAgentReinstall(ctx, node.ID, "reinstall-review-admin", input); err != nil {
		t.Fatal(err)
	}
	profile, err := networkProfile(ctx, s.db, node.ID)
	if err != nil || profile == nil || profile.ServiceAddress != "10.0.0.8" {
		t.Fatalf("approved replacement network was not activated: %+v %v", profile, err)
	}
	var retained int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_network_profile_recovery WHERE agent_id=?`, node.ID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("obsolete network intent retained: %d %v", retained, err)
	}
}
