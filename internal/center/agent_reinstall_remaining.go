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
		if app.AppKey == pulseAgentAppKey && app.Recovery == "reenroll_monitor" {
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
		if app.AppKey != meridianAppKey || app.Recovery != "rebuild_configuration" {
			add("application_review", app.ApplicationID)
			continue
		}
		p := app.Preparation
		// Show the next actionable step per application, not every dependent
		// symptom. Refreshing the plan advances this step using server receipts.
		switch {
		case p == nil || p.State != "succeeded":
			add("application_prepare", app.ApplicationID)
		case p.Landing != nil && p.Landing.State != "authorized":
			add("landing_authorization", app.ApplicationID)
		case p.Runtime == nil || p.Runtime.State != "succeeded":
			add("application_runtime", app.ApplicationID)
		case app.SharedEntry && (p.Listener == nil || p.Listener.State != "succeeded"):
			add("entry_restore", app.ApplicationID)
		case p.Access == nil || p.Access.State != "applied":
			add("access_activate", app.ApplicationID)
		case !reinstallCompletionDNSVerified(p.DNS, p.EntryCheck):
			add("entry_dns", app.ApplicationID)
		case app.SharedEntry && (p.EntryCheck == nil || !p.EntryCheck.Current || p.EntryCheck.State != "passed"):
			add("entry_verify", app.ApplicationID)
		default:
			// DNS/TLS reachability and transport observations do not establish
			// authenticated native or fixed-route client success.
			add("client_acceptance", app.ApplicationID)
		}
	}
	// Finalization independently revalidates all evidence in its transaction.
	add("completion_review", "")
	return remaining
}
