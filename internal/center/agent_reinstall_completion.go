package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/secret"
)

// These values are reconstructed inside the final write transaction. Never
// authorize completion from UI summaries or previously returned review DTOs.
type reinstallCompletionEvidence struct {
	Plan     AgentReinstallPlan
	Clients  map[string][]string
	Runtimes map[string]meridianruntime.Result
}

func (s *Store) requireReinstallCompletion(ctx context.Context, tx *sql.Tx, target, admin, operation, revision string) (reinstallCompletionEvidence, error) {
	evidence := reinstallCompletionEvidence{Clients: map[string][]string{}, Runtimes: map[string]meridianruntime.Result{}}
	plan, err := s.agentReinstallPlan(ctx, tx, target)
	if err != nil {
		return evidence, err
	}
	evidence.Plan = plan
	op := plan.Recovery
	if op == nil || op.ID != operation || op.AuthorizedBy != admin || op.State != "review_required" || plan.Revision != revision || plan.CredentialRevoked || op.ReplacementFingerprint == "" || op.ReplacementFingerprint != plan.IdentityFingerprint || (op.PrivateIsolation != "withdrawn" && op.PrivateIsolation != "not_required") {
		return evidence, errExecutionAuthorization
	}
	if len(plan.Executions) > 0 || len(plan.PendingWork) > 0 || len(plan.UnclaimedLocalWork) > 0 {
		return evidence, errors.New("center: resolve outstanding work before completing recovery")
	}
	if plan.NetworkReview == nil || !plan.NetworkReview.ApprovalCurrent {
		return evidence, errors.New("center: current replacement network approval is required")
	}
	// Read-only checks are intentionally excluded from source-plan hashing but
	// must still be terminal before finalization. An uncertain result is not a
	// successful check even if another historic request happened to pass.
	var outstanding bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_reinstall_client_checks a JOIN application_commands c ON c.id=a.command_id WHERE a.operation_id=? AND (c.state IN ('pending','running') OR c.reconciliation_required=1 OR EXISTS(SELECT 1 FROM task_executions e WHERE e.task_id=c.id AND e.disposition='' AND (e.state='unknown' OR e.phase='result_received'))))`, operation).Scan(&outstanding)
	if err != nil {
		return evidence, err
	}
	if outstanding {
		return evidence, errors.New("center: client verification is still running or requires inspection")
	}
	for _, app := range plan.Applications {
		switch {
		case app.Recovery == "keep_stopped":
			var running bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM applications WHERE id=? AND status='running')`, app.ApplicationID).Scan(&running); err != nil {
				return evidence, err
			}
			if running {
				return evidence, errors.New("center: an uninstalled application still has running state")
			}
		case app.Recovery == "restore_data":
			return evidence, errors.New("center: business data restoration has not been verified")
		case app.AppKey == pulseAgentAppKey && app.Recovery == "reenroll_monitor":
			verified := false
			for _, monitor := range plan.Monitoring {
				if monitor.ApplicationID != app.ApplicationID {
					continue
				}
				if monitor.Restoration == nil || monitor.Restoration.State != "succeeded" {
					break
				}
				// Re-read authenticated proof, not just a cached reporting state.
				receipt, err := s.readReinstallMonitorReporting(ctx, tx, monitor.Restoration.DeploymentID)
				if err != nil {
					return evidence, err
				}
				verified = receipt != nil && receipt.State == "verified"
			}
			if !verified {
				return evidence, errors.New("center: fresh reports from the original monitoring identity are required")
			}
		case app.AppKey == meridianAppKey && app.Recovery == "rebuild_configuration":
			p := app.Preparation
			if p == nil || p.State != "succeeded" || p.Runtime == nil || p.Runtime.State != "succeeded" || p.Access == nil || p.Access.State != "applied" || p.Landing != nil && p.Landing.State != "authorized" {
				return evidence, errors.New("center: restored application runtime and reviewed access are required")
			}
			if !reinstallCompletionDNSVerified(p.DNS, p.EntryCheck) {
				return evidence, errors.New("center: current entry DNS verification is required")
			}
			if app.SharedEntry && (p.Listener == nil || p.Listener.State != "succeeded" || p.EntryCheck == nil || !p.EntryCheck.Current || p.EntryCheck.State != "passed") {
				return evidence, errors.New("center: current shared entry verification is required")
			}
			_, task, err := s.reinstallAccessTarget(ctx, tx, target, p.DeploymentID)
			if err != nil {
				return evidence, err
			}
			runtime, err := s.reinstallRuntimeProof(ctx, tx, target, p.Runtime.CommandID, *task.MeridianRuntime)
			if err != nil {
				return evidence, err
			}
			evidence.Runtimes[app.ApplicationID] = runtime
			ids, err := s.requireReinstallClientCoverage(ctx, tx, target, admin, AgentReinstallApplicationInput{OperationID: operation, PlanRevision: revision, ApplicationID: app.ApplicationID})
			if err != nil {
				return evidence, err
			}
			evidence.Clients[app.ApplicationID] = ids
		default:
			return evidence, errors.New("center: an application has no verified recovery procedure")
		}
	}
	return evidence, nil
}

func (s *Store) reinstallRuntimeProof(ctx context.Context, tx *sql.Tx, target, id string, task meridianruntime.Task) (meridianruntime.Result, error) {
	var result meridianruntime.Result
	var execution string
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT id,sealed_result FROM task_executions WHERE task_id=? AND agent_id=? AND kind='application.command' AND attempt=1 AND state='succeeded' AND phase='reported' AND identity_retired_at=''`, id, target).Scan(&execution, &sealed); err != nil {
		return result, errExecutionAuthorization
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-result:"+execution))
	if err != nil {
		return result, errExecutionAuthorization
	}
	var proof executionResultEvidence
	var envelope ApplicationTaskResult
	if json.Unmarshal(raw, &proof) != nil || !proof.Succeeded || proof.Unknown || json.Unmarshal(proof.Result, &envelope) != nil || envelope.MeridianRuntime == nil || envelope.MeridianRuntime.Validate(task.Desired) != nil {
		return result, errExecutionAuthorization
	}
	return s.currentReinstallRuntimeObservation(ctx, tx, id, task)
}

// Only the latest explicit batch is authoritative. A failed recheck cannot be
// hidden by an earlier successful batch, and every expected identity/protocol
// must have its own current authenticated proof in that batch.
func (s *Store) requireReinstallClientCoverage(ctx context.Context, tx *sql.Tx, target, admin string, input AgentReinstallApplicationInput) ([]string, error) {
	var verifier, request string
	err := tx.QueryRowContext(ctx, `SELECT c.agent_id,json_extract(a.input_json,'$.requestId') FROM agent_reinstall_client_checks a JOIN application_commands c ON c.id=a.command_id WHERE a.operation_id=? AND c.application_id=? ORDER BY c.rowid DESC LIMIT 1`, input.OperationID, input.ApplicationID).Scan(&verifier, &request)
	if err != nil {
		return nil, errors.New("center: real client verification is required")
	}
	expected, err := s.reinstallAcceptanceTasks(ctx, tx, target, verifier, admin, input)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(expected))
	for _, task := range expected {
		digest, err := task.Digest()
		if err != nil {
			return nil, err
		}
		var id string
		if err = tx.QueryRowContext(ctx, `SELECT c.id FROM agent_reinstall_client_checks a JOIN application_commands c ON c.id=a.command_id WHERE a.operation_id=? AND c.application_id=? AND c.agent_id=? AND json_extract(a.input_json,'$.requestId')=? AND json_extract(a.input_json,'$.taskDigest')=? ORDER BY c.rowid DESC LIMIT 1`, input.OperationID, input.ApplicationID, verifier, request, digest).Scan(&id); err != nil {
			return nil, errors.New("center: an original client identity has no current verification")
		}
		receipt, err := s.readReinstallClientCheck(ctx, tx, verifier, id)
		if err != nil {
			return nil, err
		}
		if receipt.State != "verified" {
			return nil, errors.New("center: current authenticated client verification is incomplete")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Manual records have no provider-write receipt. Their supported completion
// path is the current DNS/TLS probe matching every exact reviewed entry.
func reinstallCompletionDNSVerified(dns *AgentReinstallDNS, check *AgentReinstallEntryCheck) bool {
	if dns == nil {
		return true
	}
	if !dns.Current {
		return false
	}
	if dns.State == "succeeded" {
		return true
	}
	if check == nil || !check.Current || check.State != "passed" || len(dns.Entries) == 0 {
		return false
	}
	for _, entry := range dns.Entries {
		if entry.Provider != "manual" || entry.State != "manual" {
			return false
		}
		matched := false
		for _, observed := range check.Entries {
			if observed.PublicationID == entry.PublicationID && observed.Hostname == entry.Hostname && observed.PublicAddress == entry.Address && observed.State == "passed" {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
