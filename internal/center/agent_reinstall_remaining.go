package center

// Remaining is a read-only navigation aid based on the current recovery
// receipts. It is deliberately not a completion authorization: finalization
// must independently validate identity, task evidence and client acceptance
// in its write transaction. Build it after hashing the saved source intent.
type AgentReinstallRemaining struct {
	Code          string `json:"code"`
	ApplicationID string `json:"applicationId,omitempty"`
}

func reinstallRemaining(plan AgentReinstallPlan) []AgentReinstallRemaining {
	remaining := []AgentReinstallRemaining{}
	add := func(code, app string) { remaining = append(remaining, AgentReinstallRemaining{code, app}) }
	op := plan.Recovery
	if op == nil {
		return remaining
	}
	if op.State != "review_required" || plan.CredentialRevoked || op.ReplacementFingerprint == "" || op.ReplacementFingerprint != plan.IdentityFingerprint {
		add("replacement_identity", "")
	}
	if op.PrivateIsolation != "withdrawn" && op.PrivateIsolation != "not_required" {
		add("previous_identity_isolation", "")
	}
	if len(plan.Executions) > 0 || len(plan.UnclaimedLocalWork) > 0 || len(plan.PendingWork) > 0 {
		add("previous_work", "")
	}
	if plan.NetworkReview == nil || !plan.NetworkReview.ApprovalCurrent {
		add("network_review", "")
	}
	for _, app := range plan.Applications {
		if app.Recovery == "keep_stopped" {
			continue
		}
		if app.Recovery == "restore_data" {
			add("data_restore", app.ApplicationID)
			continue
		}
		if app.AppKey == senulunAgentAppKey && app.Recovery == "reenroll_monitor" {
			var monitor *AgentReinstallMonitoring
			for i := range plan.Monitoring {
				if plan.Monitoring[i].ApplicationID == app.ApplicationID {
					monitor = &plan.Monitoring[i]
					break
				}
			}
			if monitor == nil || monitor.Restoration == nil || monitor.Restoration.State != "succeeded" {
				add("monitor_restore", app.ApplicationID)
			} else if monitor.Reporting == nil || monitor.Reporting.State != "verified" {
				add("monitor_reporting", app.ApplicationID)
			}
			continue
		}
		// Unknown/application-owned recovery cannot be inferred from legacy
		// process, DNS or proxy receipts. Keep the generic review requirement.
		add("application_review", app.ApplicationID)
	}
	// Finalization independently revalidates all evidence in its transaction.
	add("completion_review", "")
	return remaining
}
