package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"
)

type AgentReinstallDNSInput struct {
	OperationID     string `json:"operationId"`
	PlanRevision    string `json:"planRevision"`
	ApplicationID   string `json:"applicationId"`
	ExpectedAttempt int    `json:"expectedAttempt"`
}

type AgentReinstallDNS struct {
	ID          string                   `json:"id"`
	State       string                   `json:"state"`
	Attempt     int                      `json:"attempt"`
	Current     bool                     `json:"current"`
	CanContinue bool                     `json:"canContinue"`
	CheckedAt   time.Time                `json:"checkedAt"`
	Entries     []AgentReinstallDNSEntry `json:"entries"`
}

type AgentReinstallDNSEntry struct {
	PublicationID   string `json:"publicationId"`
	Hostname        string `json:"hostname"`
	Provider        string `json:"provider"`
	PreviousAddress string `json:"previousAddress"`
	Address         string `json:"address"`
	State           string `json:"state"`
}

// Ownership identifiers stay in the durable operation, not in the UI response.
// This step updates existing owned public A records only; it never creates or
// adopts records, modifies tunnels, or changes the global private DNS snapshot.
type reinstallDNSTarget struct {
	Entry     AgentReinstallDNSEntry `json:"entry"`
	RecordID  string                 `json:"recordId"`
	ZoneID    string                 `json:"zoneId"`
	AccountID string                 `json:"accountId"`
}

func (s *Store) reinstallDNSTargets(ctx context.Context, tx *sql.Tx, agentID, preparationID string) ([]reinstallDNSTarget, error) {
	access, err := s.readReinstallAccess(ctx, tx, agentID, preparationID)
	if err != nil {
		return nil, err
	}
	if access == nil || access.State != "applied" {
		return nil, errExecutionAuthorization
	}
	var approvalJSON []byte
	var serviceID string
	if err = tx.QueryRowContext(ctx, `SELECT p.approval_json,e.service_id FROM agent_reinstall_app_preparations p JOIN meridian_endpoints e ON e.id=p.runtime_endpoint_id WHERE p.deployment_id=?`, preparationID).Scan(&approvalJSON, &serviceID); err != nil {
		return nil, err
	}
	var approval AgentReinstallNetworkApproval
	if json.Unmarshal(approvalJSON, &approval) != nil {
		return nil, errExecutionAuthorization
	}
	previous := ""
	if approval.Previous != nil {
		previous = approval.Previous.PublicAddress
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.hostname,p.dns_provider,p.dns_record_id,
 COALESCE(i.account_id,''),COALESCE(i.zone_id,''),
 EXISTS(SELECT 1 FROM publications other WHERE other.id<>p.id AND other.status<>'stopped'
 AND (other.hostname=p.hostname OR (p.dns_record_id<>'' AND other.dns_record_id=p.dns_record_id))
 AND (other.service_id<>p.service_id OR other.entry_node_id<>p.entry_node_id OR other.kind<>p.kind))
 FROM publications p LEFT JOIN network_integrations i ON i.kind='cloudflare' AND i.status='configured'
 WHERE p.service_id=? AND p.entry_node_id=? AND p.kind='public_shared_443' AND p.ingress_owner='application_node' AND p.status<>'stopped' ORDER BY p.id`, serviceID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []reinstallDNSTarget{}
	for rows.Next() {
		target := reinstallDNSTarget{Entry: AgentReinstallDNSEntry{PreviousAddress: previous, Address: approval.Profile.PublicAddress, State: "pending"}}
		var conflict bool
		if err = rows.Scan(&target.Entry.PublicationID, &target.Entry.Hostname, &target.Entry.Provider, &target.RecordID, &target.AccountID, &target.ZoneID, &conflict); err != nil {
			return nil, err
		}
		if target.Entry.Provider == "manual" {
			target.Entry.State = "manual"
			target.AccountID = ""
			target.ZoneID = ""
		}
		if conflict || (target.Entry.Provider != "manual" && (target.Entry.Provider != "cloudflare" || target.RecordID == "" || target.ZoneID == "" || target.AccountID == "" || !isPublicPublicationVerificationIP(net.ParseIP(previous)))) {
			target.Entry.State = "blocked"
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (s *Store) savedReinstallDNS(ctx context.Context, tx *sql.Tx, preparationID string) (*AgentReinstallDNS, []reinstallDNSTarget, error) {
	var body, targetJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT result_json,targets_json FROM agent_reinstall_dns_migrations WHERE preparation_id=? ORDER BY attempt DESC LIMIT 1`, preparationID).Scan(&body, &targetJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var value AgentReinstallDNS
	var targets []reinstallDNSTarget
	if err = json.Unmarshal(body, &value); err != nil {
		return nil, nil, err
	}
	if err = json.Unmarshal(targetJSON, &targets); err != nil {
		return nil, nil, err
	}
	return &value, targets, nil
}

func (s *Store) readReinstallDNS(ctx context.Context, tx *sql.Tx, agentID, preparationID string) (*AgentReinstallDNS, error) {
	saved, expected, err := s.savedReinstallDNS(ctx, tx, preparationID)
	if err != nil {
		return nil, err
	}
	targets, err := s.reinstallDNSTargets(ctx, tx, agentID, preparationID)
	if saved != nil {
		// Bind durable DNS evidence to its owned records and approved addresses.
		// Unrelated task history must not invalidate an unchanged external result.
		saved.Current = err == nil && reflect.DeepEqual(expected, targets)
		age := s.now().UTC().Sub(saved.CheckedAt)
		saved.CanContinue = saved.CanContinue && saved.Current && age >= 0 && age <= 30*time.Minute
		return saved, nil
	}
	if errors.Is(err, errExecutionAuthorization) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, nil
	}
	value := AgentReinstallDNS{State: "pending", Current: true, Entries: []AgentReinstallDNSEntry{}}
	for _, target := range targets {
		value.Entries = append(value.Entries, target.Entry)
	}
	return &value, nil
}

func (s *Store) authorizeReinstallDNS(ctx context.Context, tx *sql.Tx, agentID, adminID string, input AgentReinstallDNSInput) (string, []reinstallDNSTarget, error) {
	var preparationID string
	err := tx.QueryRowContext(ctx, `SELECT p.deployment_id FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id WHERE op.id=? AND op.agent_id=? AND op.authorized_by=? AND op.state='review_required' AND p.application_id=?`, input.OperationID, agentID, adminID, input.ApplicationID).Scan(&preparationID)
	if err != nil {
		return "", nil, errExecutionAuthorization
	}
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return "", nil, err
	}
	if len(input.PlanRevision) != 64 || plan.Revision != input.PlanRevision || len(plan.Executions) != 0 || len(plan.UnclaimedLocalWork) != 0 {
		return "", nil, errors.New("center: recovery plan changed; review it again")
	}
	targets, err := s.reinstallDNSTargets(ctx, tx, agentID, preparationID)
	return preparationID, targets, err
}

func (s *Store) MigrateAgentReinstallDNS(ctx context.Context, agentID, adminID string, input AgentReinstallDNSInput) (AgentReinstallDNS, error) {
	return s.runReinstallDNS(ctx, agentID, adminID, input, false)
}
func (s *Store) InspectAgentReinstallDNS(ctx context.Context, agentID, adminID string, input AgentReinstallDNSInput) (AgentReinstallDNS, error) {
	return s.runReinstallDNS(ctx, agentID, adminID, input, true)
}

func (s *Store) runReinstallDNS(ctx context.Context, agentID, adminID string, input AgentReinstallDNSInput, inspect bool) (AgentReinstallDNS, error) {
	var result AgentReinstallDNS
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	s.publicationCleanupMu.Lock()
	defer s.publicationCleanupMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	preparationID, targets, err := s.authorizeReinstallDNS(ctx, tx, agentID, adminID, input)
	if err != nil {
		return result, err
	}
	saved, expected, err := s.savedReinstallDNS(ctx, tx, preparationID)
	if err != nil {
		return result, err
	}
	if saved != nil && !reflect.DeepEqual(expected, targets) {
		return result, errors.New("center: saved DNS migration no longer matches the reviewed recovery")
	}
	if !inspect && saved != nil && input.ExpectedAttempt == 0 {
		return *saved, nil
	}
	if inspect {
		if saved == nil || input.ExpectedAttempt != saved.Attempt {
			return result, errExecutionAuthorization
		}
		result = *saved
	} else {
		attempt := 1
		if saved != nil {
			age := s.now().UTC().Sub(saved.CheckedAt)
			if input.ExpectedAttempt != saved.Attempt || !saved.Current || !saved.CanContinue || age < 0 || age > 30*time.Minute {
				return result, errors.New("center: inspect the saved DNS result before explicitly continuing")
			}
			attempt = saved.Attempt + 1
		} else if input.ExpectedAttempt != 0 {
			return result, errExecutionAuthorization
		}
		id, err := randomToken(18)
		if err != nil {
			return result, err
		}
		result = AgentReinstallDNS{ID: id, Attempt: attempt, State: "needs_review", Current: true}
	}
	managed := 0
	result.CanContinue = false
	result.CheckedAt = s.now().UTC()
	result.Entries = []AgentReinstallDNSEntry{}
	for _, target := range targets {
		if target.Entry.State == "blocked" {
			return result, errors.New("center: inspect missing or shared DNS ownership before migration")
		}
		entry := target.Entry
		if entry.Provider == "cloudflare" {
			managed++
			entry.State = "not_checked"
		}
		result.Entries = append(result.Entries, entry)
	}
	if managed == 0 {
		return result, errors.New("center: configure the displayed manual DNS records and verify the public entry")
	}
	// Commit intent before any external API call. A crash or lost response leaves
	// a result to inspect, never a pending job for background replay.
	if !inspect {
		body, err := json.Marshal(result)
		if err != nil {
			return result, err
		}
		targetJSON, err := json.Marshal(targets)
		if err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_dns_migrations(id,preparation_id,attempt,plan_revision,targets_json,result_json) VALUES(?,?,?,?,?,?)`, result.ID, preparationID, result.Attempt, input.PlanRevision, targetJSON, body); err != nil {
			return result, err
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, clientErr := s.cloudflare(probeCtx)
	allApplied, canContinue := true, true
	for i, target := range targets {
		if target.Entry.Provider == "manual" {
			continue
		}
		if clientErr != nil || client.zoneID != target.ZoneID || client.accountID != target.AccountID {
			result.Entries[i].State = "unconfirmed"
			allApplied = false
			canContinue = false
			break
		}
		state := inspectReinstallDNSRecord(probeCtx, client, target)
		if state == "pending" && !inspect {
			// Recheck persisted authority immediately before each external mutation.
			if err = s.checkReinstallDNSTargets(probeCtx, agentID, adminID, input, targets); err != nil {
				state = "unconfirmed"
			} else if err = client.updateDNSRecord(probeCtx, target.RecordID, "A", target.Entry.Hostname, target.Entry.Address, false); err != nil {
				state = "unconfirmed"
			} else {
				state = inspectReinstallDNSRecord(probeCtx, client, target)
			}
		}
		result.Entries[i].State = state
		if state != "applied" {
			allApplied = false
		}
		if state != "applied" && state != "pending" {
			canContinue = false
			break
		}
		if !inspect && state != "applied" {
			canContinue = false
			break
		}
	}
	// Persist observed uncertainty even when the caller disconnected. No external
	// compensation or retry is performed under this non-cancelled context.
	saveCtx, saveCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer saveCancel()
	tx, err = s.db.BeginTx(saveCtx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	_, actual, authorityErr := s.authorizeReinstallDNS(saveCtx, tx, agentID, adminID, input)
	current := authorityErr == nil && reflect.DeepEqual(actual, targets)
	result.Current = current
	result.CanContinue = inspect && current && canContinue && !allApplied
	result.State = "needs_review"
	if current && allApplied {
		result.State = "succeeded"
	}
	result.CheckedAt = s.now().UTC()
	body, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(saveCtx, `UPDATE agent_reinstall_dns_migrations SET result_json=? WHERE id=?`, body, result.ID); err != nil {
		return result, err
	}
	if err = s.recordReinstallPreparationProgress(saveCtx, tx, preparationID); err != nil {
		return result, err
	}
	event := "failed"
	if result.State == "succeeded" {
		event = "succeeded"
	}
	if err = s.recordTaskEvent(saveCtx, tx, result.ID, agentID, "agent.reinstall.dns", int64(result.Attempt), event, "Reviewed DNS migration evidence recorded; public and client access remain under recovery"); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (s *Store) checkReinstallDNSTargets(ctx context.Context, agentID, adminID string, input AgentReinstallDNSInput, expected []reinstallDNSTarget) error {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, actual, err := s.authorizeReinstallDNS(ctx, tx, agentID, adminID, input)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return errExecutionAuthorization
	}
	return nil
}

func inspectReinstallDNSRecord(ctx context.Context, client cloudflareClient, target reinstallDNSTarget) string {
	records, err := client.listDNSRecords(ctx, target.Entry.Hostname)
	if err != nil {
		return "unconfirmed"
	}
	// The shared client fetches at most 100 records. Refuse a possibly truncated result.
	if len(records) >= 100 {
		return "conflict"
	}
	found := false
	state := "conflict"
	for _, record := range records {
		if record.Type != "A" && record.Type != "AAAA" && record.Type != "CNAME" {
			continue
		}
		if found || record.ID != target.RecordID || record.Type != "A" || record.Proxied || !strings.EqualFold(strings.TrimSuffix(record.Name, "."), strings.TrimSuffix(target.Entry.Hostname, ".")) {
			return "conflict"
		}
		found = true
		if record.Content == target.Entry.Address {
			state = "applied"
		} else if record.Content == target.Entry.PreviousAddress {
			state = "pending"
		}
	}
	return state
}

func (s *Server) handleAgentReinstallDNS(writer http.ResponseWriter, request *http.Request) {
	var input AgentReinstallDNSInput
	if err := decodeJSON(request, &input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	adminID, err := s.requestAdminID(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.runReinstallDNS(request.Context(), request.PathValue("id"), adminID, input, strings.HasSuffix(request.URL.Path, "/inspect-dns"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, result)
}
