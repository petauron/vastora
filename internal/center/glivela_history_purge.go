package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// This is a local administrator operation, not an uninstall or a legacy writer.
// No archive is made: the operator explicitly discards the old installation's
// history. The independent Glivela identity and ledger are never changed.
type SupersededHistoryPurgeResult struct {
	Committed           bool             `json:"committed"`
	Deleted             map[string]int64 `json:"deleted"`
	PreservedExecutions int64            `json:"preservedExecutions"`
}

func (s *Store) PurgeSupersededGlivelaHistory(ctx context.Context, oldID, newID, confirmation string, commit bool) (SupersededHistoryPurgeResult, error) {
	result := SupersededHistoryPurgeResult{Deleted: map[string]int64{}}
	refused := errors.New("center: superseded Glivela history purge refused")
	if !s.businessRetired || oldID == "" || newID == "" || oldID == newID || confirmation == "" {
		return result, refused
	}
	s.agentRemovalMu.Lock()
	defer s.agentRemovalMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var oldKey, newKey, oldState, newState, oldNode, newNode, name, revoked, agentState, endpointID string
	var sourceRaw, replacementRaw, evidenceRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT old.app_key,new.app_key,old.status,new.status,old.node_id,new.node_id,a.name,a.status,a.credential_revoked_at,source.resources_json,replacement.resources_json,r.evidence_json
 FROM applications old JOIN applications new ON new.id=?
 JOIN agents a ON a.id=old.node_id
 JOIN application_execution_retirements r ON r.application_id=old.id AND r.replacement_application_id=new.id
 JOIN application_resources source ON source.application_id=old.id AND source.adoption_state='ready'
 JOIN application_resources replacement ON replacement.application_id=new.id AND replacement.adoption_state='ready'
 WHERE old.id=?`, newID, oldID).Scan(&oldKey, &newKey, &oldState, &newState, &oldNode, &newNode, &name, &agentState, &revoked, &sourceRaw, &replacementRaw, &evidenceRaw)
	if err != nil {
		return result, refused
	}
	if oldKey != meridianAppKey || newKey != "vastora-official/glivela-entry" || oldNode != newNode || name != confirmation || agentState != "active" || revoked != "" || newState != "running" || (oldState != "pending" && oldState != "stopped" && oldState != "running") {
		return result, refused
	}
	var evidence GlivelaExecutionRetirement
	var receipt struct {
		ApplicationID string `json:"applicationId"`
		AppKey        string `json:"appKey"`
		Runtime       string `json:"runtime"`
		State         string `json:"state"`
	}
	if json.Unmarshal(evidenceRaw, &evidence) != nil || evidence.ApplicationID != oldID || evidence.ReplacementApplicationID != newID || !evidence.LegacyStopped || !evidence.RestartDisabled || !evidence.NativeReady || !legacyGlivelaReceipt(sourceRaw) || rawManifestDigest(sourceRaw) != evidence.SourceReceiptSHA256 {
		return result, refused
	}
	if oldState == "running" && evidence.LegacyState != "execution-retired" {
		return result, refused
	}
	if json.Unmarshal(replacementRaw, &receipt) != nil || receipt.ApplicationID != newID || receipt.AppKey != newKey || receipt.Runtime != "native" || receipt.State != "ready" {
		return result, refused
	}
	if err := s.requireReadyGlivelaHistoryReplacement(oldID); err != nil {
		return result, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id FROM meridian_endpoints WHERE application_id=? AND legacy_retired=1`, oldID).Scan(&endpointID); err != nil {
		return result, refused
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_endpoints WHERE application_id=?`, oldID).Scan(&n); err != nil || n != 1 {
		return result, refused
	}
	var serviceID, serviceOwner string
	if err := tx.QueryRowContext(ctx, `SELECT v.id,v.application_id FROM meridian_endpoints e JOIN services v ON v.id=e.service_id WHERE e.id=?`, endpointID).Scan(&serviceID, &serviceOwner); err != nil || (serviceOwner != oldID && serviceOwner != newID) {
		return result, refused
	}
	oldService := ""
	if serviceOwner == oldID {
		oldService = serviceID
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE application_id=?`, oldID).Scan(&n); err != nil || n != 1 {
			return result, errors.New("center: unexpected obsolete service ownership")
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications WHERE service_id=? AND (status<>'stopped' OR cleanup_pending<>0)`, oldService).Scan(&n); err != nil || n != 0 {
			return result, errors.New("center: withdraw obsolete publications before history purge")
		}
		if err := requireOnlyHistoryReferences(ctx, tx, "services", []string{oldService}, map[string]bool{"publications/service_id": true, "meridian_endpoints/service_id": true, "landing_client_grants/service_id": true, "three_x_ui_reality_guards/service_id": true, "three_x_ui_inbound_plans/service_id": true, "three_x_ui_node_protocols/service_id": true}); err != nil {
			return result, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_endpoints WHERE service_id=? AND id<>?`, oldService, endpointID).Scan(&n); err != nil || n != 0 {
			return result, errors.New("center: obsolete service is shared")
		}
	}
	for _, table := range []string{"deployments", "application_commands", "application_adoptions", "application_maintenance"} {
		var pending int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE application_id IN (?,?) AND (state IN ('pending','running')"+historyReconciliationClause(table)+")", oldID, newID).Scan(&pending); err != nil {
			return result, err
		}
		if pending != 0 {
			return result, errors.New("center: resolve pending installation work before history purge")
		}
	}
	// Frozen route flags are historical after permanent handover. A route
	// owned by another endpoint must still block removal of shared credentials.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants g JOIN meridian_credentials c ON c.id IN(g.base_credential_id,g.route_credential_id) WHERE c.endpoint_id=? AND g.endpoint_id<>?`, endpointID, endpointID).Scan(&n); err != nil || n != 0 {
		return result, errors.New("center: shared route still references obsolete history")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants g WHERE g.endpoint_id=? AND (NOT EXISTS(SELECT 1 FROM meridian_credentials c WHERE c.id=g.base_credential_id AND c.endpoint_id=?) OR NOT EXISTS(SELECT 1 FROM meridian_credentials c WHERE c.id=g.route_credential_id AND c.endpoint_id=?))`, endpointID, endpointID, endpointID).Scan(&n); err != nil || n != 0 {
		return result, errors.New("center: obsolete route uses another endpoint's credential")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM landing_client_grants WHERE (application_id=? AND service_id<>?) OR (service_id=? AND application_id<>?)`, oldID, oldService, serviceID, oldID).Scan(&n); err != nil || n != 0 {
		return result, errors.New("center: retained landing grant crosses installation ownership")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM xray_configuration_recoveries WHERE application_id=? AND state NOT IN('succeeded','failed')`, oldID).Scan(&n); err != nil || n != 0 {
		return result, errors.New("center: resolve obsolete configuration recovery first")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_adoptions WHERE deployment_id IN(SELECT id FROM deployments WHERE application_id=?) AND application_id<>?`, oldID, oldID).Scan(&n); err != nil || n != 0 {
		return result, errors.New("center: deployment is shared with a retained adoption")
	}
	if err := requireOnlyHistoryReferences(ctx, tx, "applications", []string{oldID}, map[string]bool{"deployments/application_id": true, "application_commands/application_id": true, "application_resources/application_id": true, "application_execution_retirements/application_id": true, "meridian_endpoints/application_id": true, "services/application_id": true, "three_x_ui_nodes/worker_application_id": true, "application_secrets/application_id": true, "landing_client_grants/application_id": true, "landing_proxy_states/application_id": true, "xray_configuration_recoveries/application_id": true, "application_adoptions/application_id": true}); err != nil {
		return result, err
	}
	tasks, err := historyStrings(ctx, tx, `SELECT id FROM deployments WHERE application_id=? UNION ALL SELECT id FROM application_commands WHERE application_id=? UNION ALL SELECT id FROM application_adoptions WHERE application_id=? UNION ALL SELECT id FROM xray_configuration_recoveries WHERE application_id=?`, oldID, oldID, oldID, oldID)
	if err != nil {
		return result, err
	}
	for _, table := range []string{"deployments", "application_commands", "application_adoptions", "xray_configuration_recoveries"} {
		ids, err := historyStrings(ctx, tx, "SELECT id FROM "+table+" WHERE application_id=?", oldID)
		if err != nil {
			return result, err
		}
		allowed := map[string]bool{}
		if table == "deployments" {
			allowed["application_adoptions/deployment_id"] = true
		}
		if err := requireOnlyHistoryReferences(ctx, tx, table, ids, allowed); err != nil {
			return result, err
		}
	}
	// Disposed executions are independent immutable audit envelopes. Keep the
	// entire task's executions/events when any attempt has a disposition; never
	// change their task ID, ciphertext, actor or historical identity.
	for _, id := range tasks {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_executions WHERE task_id=? AND disposition='' AND state NOT IN('failed','succeeded')`, id).Scan(&n); err != nil {
			return result, err
		}
		if n != 0 {
			return result, errors.New("center: obsolete task has an unresolved execution")
		}
	}
	if err := requireOnlyHistoryReferences(ctx, tx, "meridian_endpoints", []string{endpointID}, map[string]bool{"meridian_credentials/endpoint_id": true, "meridian_deployments/endpoint_id": true, "glivela_pilot_checkpoint/endpoint_id": true, "meridian_route_grants/endpoint_id": true}); err != nil {
		return result, err
	}
	credentials, err := historyStrings(ctx, tx, `SELECT id FROM meridian_credentials WHERE endpoint_id=?`, endpointID)
	if err != nil {
		return result, err
	}
	if err := requireOnlyHistoryReferences(ctx, tx, "meridian_credentials", credentials, map[string]bool{"meridian_usage_watermarks/credential_id": true, "meridian_line_usage/credential_id": true, "meridian_route_grants/base_credential_id": true, "meridian_route_grants/route_credential_id": true}); err != nil {
		return result, err
	}
	var secrets []string
	for _, q := range []string{
		`SELECT private_key_secret_id FROM meridian_endpoints WHERE id=?`,
		`SELECT hy2_certificate_secret_id FROM meridian_endpoints WHERE id=? AND hy2_certificate_secret_id IS NOT NULL`,
		`SELECT hy2_private_key_secret_id FROM meridian_endpoints WHERE id=? AND hy2_private_key_secret_id IS NOT NULL`,
		`SELECT protocol_secret_id FROM meridian_credentials WHERE endpoint_id=?`,
		`SELECT hy2_auth_secret_id FROM meridian_credentials WHERE endpoint_id=? AND hy2_auth_secret_id IS NOT NULL`,
	} {
		ids, err := historyStrings(ctx, tx, q, endpointID)
		if err != nil {
			return result, err
		}
		secrets = append(secrets, ids...)
	}
	for _, q := range []string{`SELECT secret_id FROM deployments WHERE application_id=? AND secret_id IS NOT NULL`, `SELECT result_secret_id FROM application_commands WHERE application_id=? AND result_secret_id IS NOT NULL`, `SELECT secret_id FROM application_secrets WHERE application_id=?`, `SELECT credential_secret_id FROM landing_client_grants WHERE application_id=?`, `SELECT material_secret_id FROM landing_client_grants WHERE application_id=? AND material_secret_id IS NOT NULL`, `SELECT certificate_secret_id FROM three_x_ui_node_protocols WHERE service_id IN(SELECT id FROM services WHERE application_id=?) AND certificate_secret_id IS NOT NULL`} {
		ids, err := historyStrings(ctx, tx, q, oldID)
		if err != nil {
			return result, err
		}
		secrets = append(secrets, ids...)
	}
	scopes, err := supersededHistoryScopes(ctx, tx, oldID, endpointID, oldService, credentials)
	if err != nil {
		return result, err
	}
	for i := range scopes {
		item := &scopes[i]
		if !item.frozen {
			continue
		}
		item.original, err = allowFrozenHistoryDelete(ctx, tx, item.table, item.column, item.ids)
		if err != nil {
			return result, err
		}
	}
	remove := func(table, query string, args ...any) error {
		r, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		count, err := r.RowsAffected()
		result.Deleted[table] += count
		return err
	}
	for _, id := range tasks {
		var disposed, preserved int64
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_executions WHERE task_id=? AND disposition<>''`, id).Scan(&disposed); err != nil {
			return result, err
		}
		if disposed != 0 {
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_executions WHERE task_id=?`, id).Scan(&preserved); err != nil {
				return result, err
			}
			result.PreservedExecutions += preserved
			continue
		}
		if err := remove("task_events", `DELETE FROM task_events WHERE task_id=?`, id); err != nil {
			return result, err
		}
		if err := remove("task_executions", `DELETE FROM task_executions WHERE task_id=? AND disposition='' AND state IN('failed','succeeded')`, id); err != nil {
			return result, err
		}
	}
	for _, item := range scopes {
		for _, id := range item.ids {
			if err := remove(item.table, "DELETE FROM "+item.table+" WHERE "+item.column+"=?", id); err != nil {
				return result, err
			}
		}
	}
	// Historical receipt/commands/deployments cascade only after their dependencies
	// above have been proven absent. Current replacement services remain untouched.
	if err := remove("application_execution_retirements", `DELETE FROM application_execution_retirements WHERE application_id=? AND replacement_application_id=?`, oldID, newID); err != nil {
		return result, err
	}
	if err := remove("applications", `DELETE FROM applications WHERE id=? AND app_key=?`, oldID, meridianAppKey); err != nil {
		return result, err
	}
	if result.Deleted["applications"] != 1 {
		return result, refused
	}
	if err := deleteUnreferencedRemovalSecrets(ctx, tx, secrets); err != nil {
		return result, err
	}
	for _, item := range scopes {
		if err := restoreFrozenHistoryDelete(ctx, tx, item.table, item.original); err != nil {
			return result, err
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return result, err
	}
	broken := rows.Next()
	readErr := rows.Err()
	rows.Close()
	if broken || readErr != nil {
		return result, errors.New("center: history purge has unresolved references")
	}
	if !commit {
		return result, nil
	} // deferred rollback includes temporary guard exceptions
	if err := tx.Commit(); err != nil {
		return result, err
	}
	result.Committed = true
	return result, nil
}

func historyReconciliationClause(table string) string {
	if table == "application_adoptions" {
		return ""
	}
	return " OR reconciliation_required=1"
}

func historyStrings(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return scanRemovalStrings(rows)
}

// Unknown CASCADE/SET NULL dependents are not permission to erase other history.
func requireOnlyHistoryReferences(ctx context.Context, tx *sql.Tx, target string, ids []string, allowed map[string]bool) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.name,f."from",f."to" FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f WHERE m.type='table' AND f."table"=?`, target)
	if err != nil {
		return err
	}
	type ref struct{ table, column, target string }
	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.table, &r.column, &r.target); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	quote := func(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
	for _, r := range refs {
		if allowed[r.table+"/"+r.column] && r.target == "id" {
			continue
		}
		for _, id := range ids {
			var n int
			if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quote(r.table)+" WHERE "+quote(r.column)+"=?", id).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("center: retained history still referenced by %s", r.table)
			}
		}
	}
	return nil
}

func (s *Store) requireReadyGlivelaHistoryReplacement(entryID string) error {
	path := s.glivelaPilotStatePath
	if path == "" {
		path = "/run/glivela-pilot/nodes.json"
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 16<<20 {
		return errors.New("center: current Glivela identity evidence unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	type applied struct {
		Revision     int64  `json:"revision"`
		ConfigSHA256 string `json:"configSha256"`
		RuntimeReady bool   `json:"runtimeReady"`
	}
	var nodes []struct {
		ID            string    `json:"id"`
		CertificateID string    `json:"certificateId"`
		ReportedAt    time.Time `json:"reportedAt"`
		Desired       applied   `json:"desired"`
		Receipt       *applied  `json:"receipt"`
	}
	if err := json.NewDecoder(io.LimitReader(f, (16<<20)+1)).Decode(&nodes); err != nil {
		return err
	}
	count := 0
	for _, n := range nodes {
		if n.ID != entryID {
			continue
		}
		count++
		if n.CertificateID == "" || n.Receipt == nil || !n.Receipt.RuntimeReady || n.Desired.Revision <= 0 || n.Desired.Revision != n.Receipt.Revision || !sha256Text(n.Desired.ConfigSHA256) || n.Desired.ConfigSHA256 != n.Receipt.ConfigSHA256 || s.now().UTC().Sub(n.ReportedAt) > 30*time.Second || n.ReportedAt.After(s.now().UTC().Add(time.Second)) {
			return errors.New("center: current Glivela replacement is not verified ready")
		}
	}
	if count != 1 {
		return errors.New("center: exact Glivela identity required")
	}
	return nil
}
