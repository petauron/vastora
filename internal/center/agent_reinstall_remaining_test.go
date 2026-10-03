package center

import (
	"context"
	"slices"
	"testing"
)

func TestAgentReinstallRemainingDoesNotTreatEntryAsClientAcceptance(t *testing.T) {
	plan := AgentReinstallPlan{
		IdentityFingerprint: "replacement",
		Recovery:            &AgentReinstallOperation{State: "review_required", PrivateIsolation: "withdrawn", ReplacementFingerprint: "replacement"},
		NetworkReview:       &AgentReinstallNetworkReview{ApprovalCurrent: true},
		Applications: []AgentReinstallApplication{{ApplicationID: "app", AppKey: meridianAppKey, Recovery: "rebuild_configuration", SharedEntry: true,
			Preparation: &AgentReinstallPreparation{State: "succeeded", Runtime: &AgentReinstallRuntime{State: "succeeded"}, Listener: &AgentReinstallListener{State: "succeeded"}, Access: &AgentReinstallAccess{State: "applied"}, EntryCheck: &AgentReinstallEntryCheck{State: "passed", Current: true}}}},
	}
	for _, test := range []struct {
		name   string
		change func(*AgentReinstallPlan)
		want   string
	}{
		{"reachable", func(p *AgentReinstallPlan) {}, "client_acceptance"},
		{"expired entry", func(p *AgentReinstallPlan) { p.Applications[0].Preparation.EntryCheck.Current = false }, "entry_verify"},
		{"stale DNS", func(p *AgentReinstallPlan) {
			p.Applications[0].Preparation.DNS = &AgentReinstallDNS{State: "succeeded"}
		}, "entry_dns"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Restore the independent mutable receipts for each case.
			p := plan
			prep := *plan.Applications[0].Preparation
			entry := *prep.EntryCheck
			prep.EntryCheck = &entry
			p.Applications = append([]AgentReinstallApplication(nil), plan.Applications...)
			p.Applications[0].Preparation = &prep
			test.change(&p)
			got := reinstallRemaining(p)
			if len(got) != 2 || got[0].Code != test.want || got[0].ApplicationID != "app" || got[1].Code != "completion_review" {
				t.Fatalf("remaining: %+v", got)
			}
		})
	}
}

func TestAgentReinstallRemainingDistinguishesMissingMonitorAndBackups(t *testing.T) {
	plan := AgentReinstallPlan{Recovery: &AgentReinstallOperation{}, Applications: []AgentReinstallApplication{
		{ApplicationID: "monitor", AppKey: pulseAgentAppKey, Recovery: "reenroll_monitor"},
		{ApplicationID: "data", Recovery: "restore_data"},
		{ApplicationID: "removed", Recovery: "keep_stopped"},
	}}
	for _, state := range []string{"", "stale", "verified"} {
		plan.Monitoring = nil
		want := "monitor_restore"
		if state != "" {
			plan.Monitoring = []AgentReinstallMonitoring{{ApplicationID: "monitor", Restoration: &AgentReinstallMonitorRestore{State: "succeeded"}, Reporting: &AgentReinstallMonitorReporting{State: state}}}
			want = "monitor_reporting"
		}
		got := reinstallRemaining(plan)
		has := func(code, app string) bool { return slices.Contains(got, AgentReinstallRemaining{code, app}) }
		if (state != "verified" && !has(want, "monitor")) || (state == "verified" && (has("monitor_restore", "monitor") || has("monitor_reporting", "monitor"))) || !has("data_restore", "data") {
			t.Fatalf("%s: %+v", state, got)
		}
		for _, item := range got {
			if item.ApplicationID == "removed" {
				t.Fatal("uninstalled application was resurrected")
			}
		}
	}
}

func TestAgentReinstallRemainingReadPreservesRevisionAndFence(t *testing.T) {
	s, node, _ := reinstallEntryCheckFixture(t)
	ctx := context.Background()
	first, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AgentReinstallPlan(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Remaining) == 0 || first.Revision != second.Revision || first.Recovery.State != "review_required" || second.Recovery.State != "review_required" {
		t.Fatalf("read changed recovery or omitted remaining work: %+v", second.Remaining)
	}
	var active int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agent_reinstall_operations WHERE agent_id=? AND state NOT IN ('completed','superseded')`, node.ID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("recovery fence: %d %v", active, err)
	}
}
