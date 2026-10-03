package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"
)

// This is evidence about DNS and the TLS entry, not an authenticated proxy
// request. It must never release recovery or publish native/landing routes.
type AgentReinstallEntryCheck struct {
	ID        string                           `json:"id"`
	State     string                           `json:"state"`
	Current   bool                             `json:"current"`
	CheckedAt time.Time                        `json:"checkedAt"`
	Entries   []AgentReinstallEntryCheckResult `json:"entries"`
}

type AgentReinstallEntryCheckResult struct {
	PublicationID string `json:"publicationId"`
	Hostname      string `json:"hostname"`
	PublicAddress string `json:"publicAddress"`
	SNIHostname   string `json:"sniHostname"`
	State         string `json:"state"`
}

func (s *Store) readReinstallEntryCheck(ctx context.Context, tx *sql.Tx, preparationID, revision string) (*AgentReinstallEntryCheck, error) {
	var encoded []byte
	var reviewed, agentID, listenerID, runtimeState, listenerState string
	var listenerReady bool
	err := tx.QueryRowContext(ctx, `SELECT e.result_json,e.plan_revision,op.agent_id,p.listener_task_id,c.state,p.listener_state,
 n.status='ready' AND n.applied_revision=p.listener_revision AND n.desired_revision=p.listener_revision AND n.attempt=p.listener_attempt
 FROM agent_reinstall_entry_checks e JOIN agent_reinstall_app_preparations p ON p.deployment_id=e.preparation_id
 JOIN agent_reinstall_operations op ON op.id=p.operation_id JOIN application_commands c ON c.id=p.runtime_command_id
 JOIN node_listener_states n ON n.node_id=op.agent_id
 WHERE e.preparation_id=? ORDER BY e.started_at DESC,e.id DESC LIMIT 1`, preparationID).Scan(&encoded, &reviewed, &agentID, &listenerID, &runtimeState, &listenerState, &listenerReady)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result AgentReinstallEntryCheck
	if err = json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	age := s.now().UTC().Sub(result.CheckedAt)
	result.Current = revision == reviewed && listenerReady && runtimeState == "succeeded" && listenerState == "succeeded" && age >= 0 && age <= 30*time.Minute
	if result.Current {
		task, checkErr := s.reinstallListenerTask(ctx, tx, agentID, listenerID, false)
		result.Current = checkErr == nil && task != nil
	}
	return &result, nil
}

func (s *Store) reinstallEntryCheckTargets(ctx context.Context, tx *sql.Tx, agentID, adminID string, input AgentReinstallApplicationInput) (string, []AgentReinstallEntryCheckResult, error) {
	var preparationID, listenerID string
	var approvalJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT p.deployment_id,p.listener_task_id,p.approval_json
 FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id
 JOIN application_commands c ON c.id=p.runtime_command_id JOIN node_listener_states n ON n.node_id=op.agent_id
 WHERE op.id=? AND op.agent_id=? AND op.authorized_by=? AND op.state='review_required' AND p.application_id=?
 AND p.listener_state='succeeded' AND c.state='succeeded' AND n.status='ready'
 AND n.applied_revision=p.listener_revision AND n.desired_revision=p.listener_revision AND n.attempt=p.listener_attempt`, input.OperationID, agentID, adminID, input.ApplicationID).Scan(&preparationID, &listenerID, &approvalJSON)
	if err != nil {
		return "", nil, errExecutionAuthorization
	}
	if task, err := s.reinstallListenerTask(ctx, tx, agentID, listenerID, false); err != nil {
		return "", nil, err
	} else if task == nil {
		return "", nil, errExecutionAuthorization
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return "", nil, err
	}
	if len(input.PlanRevision) != 64 || plan.Revision != input.PlanRevision {
		return "", nil, errors.New("center: recovery plan changed; review it again")
	}
	if len(plan.Executions) != 0 || len(plan.UnclaimedLocalWork) != 0 {
		return "", nil, errors.New("center: settle previous work before checking public access")
	}
	var approval AgentReinstallNetworkApproval
	if json.Unmarshal(approvalJSON, &approval) != nil || !approval.Profile.DirectPublic || !isPublicPublicationVerificationIP(net.ParseIP(approval.Profile.PublicAddress)) {
		return "", nil, errExecutionAuthorization
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.hostname,p.sni_hostname FROM publications p JOIN services s ON s.id=p.service_id
 WHERE s.application_id=? AND p.entry_node_id=? AND p.kind='public_shared_443' AND p.ingress_owner='application_node' AND p.status<>'stopped' ORDER BY p.id`, input.ApplicationID, agentID)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	entries := []AgentReinstallEntryCheckResult{}
	for rows.Next() {
		entry := AgentReinstallEntryCheckResult{PublicAddress: approval.Profile.PublicAddress, State: "not_checked"}
		if err = rows.Scan(&entry.PublicationID, &entry.Hostname, &entry.SNIHostname); err != nil {
			return "", nil, err
		}
		entries = append(entries, entry)
	}
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	if len(entries) == 0 {
		return "", nil, errExecutionAuthorization
	}
	return preparationID, entries, nil
}

func (s *Store) VerifyAgentReinstallEntry(ctx context.Context, agentID, adminID string, input AgentReinstallApplicationInput) (AgentReinstallEntryCheck, error) {
	return s.verifyAgentReinstallEntry(ctx, agentID, adminID, input, checkReinstallEntry)
}

func (s *Store) verifyAgentReinstallEntry(ctx context.Context, agentID, adminID string, input AgentReinstallApplicationInput, check func(context.Context, AgentReinstallEntryCheckResult) string) (AgentReinstallEntryCheck, error) {
	result := AgentReinstallEntryCheck{State: "passed"}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	preparationID, entries, err := s.reinstallEntryCheckTargets(ctx, tx, agentID, adminID, input)
	_ = tx.Rollback()
	if err != nil {
		return result, err
	}
	started := s.now().UTC()
	// Release the database before DNS/TLS I/O. One bounded, read-only attempt;
	// interrupted requests are never automatically retried by recovery.
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for i := range entries {
		entries[i].State = check(probeCtx, entries[i])
		if entries[i].State != "passed" {
			result.State = "pending"
			break
		}
	}
	result.Entries = entries
	result.CheckedAt = s.now().UTC()
	result.ID, err = randomToken(18)
	if err != nil {
		return result, err
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// A changed identity, address, credential, listener or publication invalidates
	// an in-flight result. Never attach a prior machine's reachability to new intent.
	if _, _, err = s.reinstallEntryCheckTargets(ctx, tx, agentID, adminID, input); err != nil {
		return result, err
	}
	result.Current = true
	encoded, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_entry_checks(id,preparation_id,plan_revision,started_at,result_json) VALUES(?,?,?,?,?)`, result.ID, preparationID, input.PlanRevision, started.Format(time.RFC3339Nano), encoded); err != nil {
		return result, err
	}
	if err = s.recordReinstallPreparationProgress(ctx, tx, preparationID); err != nil {
		return result, err
	}
	event := "succeeded"
	if result.State != "passed" {
		event = "failed"
	}
	if err = s.recordTaskEvent(ctx, tx, result.ID, agentID, "reinstall.entry.verify", 0, event, "Public DNS/TLS check recorded; authenticated client verification remains pending"); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func checkReinstallEntry(ctx context.Context, entry AgentReinstallEntryCheckResult) string {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, entry.Hostname)
	if err != nil || !reinstallEntryDNSMatches(entry.PublicAddress, addresses) {
		return "dns_pending"
	}
	if err = verifyShared443TLS(ctx, entry.PublicAddress, entry.SNIHostname); err != nil {
		return "tls_pending"
	}
	return "passed"
}

func reinstallEntryDNSMatches(address string, addresses []net.IPAddr) bool {
	expected := net.ParseIP(address)
	if !isPublicPublicationVerificationIP(expected) || len(addresses) == 0 {
		return false
	}
	// A stale second answer can still send clients to the retired host.
	for _, answer := range addresses {
		if !answer.IP.Equal(expected) {
			return false
		}
	}
	return true
}

func (s *Server) handleAgentReinstallEntryCheck(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallApplicationInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.VerifyAgentReinstallEntry(request.Context(), request.PathValue("id"), adminID, input)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
