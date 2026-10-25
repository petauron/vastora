package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/platform"
	"github.com/petauron/vastora/internal/secret"
	"github.com/petauron/vastora/internal/senulun"
)

// These collectors own their business state on another service. Recovery may
// install their original package with newly issued credentials, but installation
// does not prove that the original business identity is reporting or usable.
func currentReinstallApp(key string) bool {
	return key == "vastora-official/glivela-entry" || key == komariAppKey || key == senulunAgentAppKey
}

const currentReinstallSchema = `CREATE TABLE agent_reinstall_current_applications (
 deployment_id TEXT PRIMARY KEY REFERENCES deployments(id),
 operation_id TEXT NOT NULL REFERENCES agent_reinstall_operations(id),
 application_id TEXT NOT NULL REFERENCES applications(id),
 source_deployment_id TEXT NOT NULL REFERENCES deployments(id),
 source_revision TEXT NOT NULL,
 replacement_key BLOB NOT NULL,
 approval_json BLOB NOT NULL,
 operator_verification_json BLOB NOT NULL DEFAULT '{}' CHECK(json_valid(operator_verification_json)),
 sealed_task BLOB NOT NULL,
 UNIQUE(operation_id,application_id)
)`

type AgentReinstallCurrentInput struct {
	AgentReinstallApplicationInput
	Config json.RawMessage `json:"config"`
	// Only a freshly rotated token for the original independently managed node.
	// This deliberately does not create a new enrollment or a second identity.
	SenulunRestore *senulun.RestoreCredentials `json:"senulunRestore,omitempty"`
}

func (s *Store) QueueAgentReinstallCurrent(ctx context.Context, agentID, adminID string, input AgentReinstallCurrentInput) (AgentReinstallMonitorRestore, error) {
	var result AgentReinstallMonitorRestore
	s.agentReinstallMu.Lock()
	defer s.agentReinstallMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return result, err
	}
	op := plan.Recovery
	if op == nil || op.ID != input.OperationID || op.AuthorizedBy != adminID || op.State != "review_required" || op.ReplacementFingerprint == "" || op.ReplacementFingerprint != plan.IdentityFingerprint || plan.CredentialRevoked || (op.PrivateIsolation != "withdrawn" && op.PrivateIsolation != "not_required") {
		return result, errExecutionAuthorization
	}
	if plan.Revision != input.PlanRevision || len(input.PlanRevision) != 64 {
		return result, errors.New("center: recovery plan changed; review it again")
	}
	if len(plan.Executions) > 0 || len(plan.PendingWork) > 0 || len(plan.UnclaimedLocalWork) > 0 {
		return result, errors.New("center: resolve outstanding work before restoring current applications")
	}
	if plan.NetworkReview == nil || plan.NetworkReview.Approval == nil || !plan.NetworkReview.ApprovalCurrent {
		return result, errors.New("center: approve the replacement network before restoring applications")
	}
	idx := slices.IndexFunc(plan.Applications, func(a AgentReinstallApplication) bool { return a.ApplicationID == input.ApplicationID })
	if idx < 0 {
		return result, errExecutionAuthorization
	}
	app := plan.Applications[idx]
	if !currentReinstallApp(app.AppKey) || app.Recovery == "keep_stopped" || app.Recovery == "reenroll_monitor" || app.State != "succeeded" {
		return result, errors.New("center: reviewed current application required; use managed monitoring recovery for managed collectors")
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT deployment_id FROM agent_reinstall_current_applications WHERE operation_id=? AND application_id=?`, op.ID, app.ApplicationID).Scan(&existing)
	if err == nil {
		return result, errors.New("center: current application restoration already exists; inspect its result before proceeding")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var task AgentTask
	var manifest, oldConfig []byte
	var previousSecret, registry sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT d.manifest_json,d.config_json,d.secret_id,d.registry_credential_id,a.role FROM deployments d JOIN applications a ON a.id=d.application_id WHERE d.id=? AND d.application_id=? AND d.agent_id=? AND d.reconciliation_required=0`, app.DeploymentID, app.ApplicationID, agentID).Scan(&manifest, &oldConfig, &previousSecret, &registry, &task.ApplicationRole)
	if err != nil {
		return result, err
	}
	if registry.Valid || json.Unmarshal(manifest, &task.Manifest) != nil || catalog.ValidateApp(task.Manifest) != nil || task.Manifest.Version != app.Version {
		return result, errors.New("center: original package requires review")
	}
	if err = readReinstallPackageAuthorization(ctx, tx, app.DeploymentID, &task); err != nil {
		return result, err
	}
	// All original non-secret settings are retained. Recovery is not permission
	// to redirect an independent collector to a different service.
	normalized := task.Manifest
	if app.AppKey == senulunAgentAppKey {
		if input.SenulunRestore == nil || input.SenulunRestore.Validate() != nil {
			return result, errors.New("center: original Senulun node and newly rotated token required")
		}
		normalized.Config = slices.DeleteFunc(slices.Clone(normalized.Config), func(f catalog.ConfigField) bool { return f.Key == "enrollment_token" })
	} else if input.SenulunRestore != nil {
		return result, errExecutionAuthorization
	}
	config, credentials, err := normalizeDeploymentConfig(normalized, input.Config)
	if err != nil {
		return result, err
	}
	var oldValues, newValues map[string]any
	if json.Unmarshal(oldConfig, &oldValues) != nil || json.Unmarshal(config, &newValues) != nil {
		return result, errors.New("center: invalid saved configuration")
	}
	oldJSON, _ := json.Marshal(oldValues)
	newJSON, _ := json.Marshal(newValues)
	if string(oldJSON) != string(newJSON) {
		return result, errors.New("center: recovery must retain the original service and non-secret settings")
	}
	if app.AppKey != senulunAgentAppKey {
		var fresh, previous map[string]string
		if json.Unmarshal(credentials, &fresh) != nil {
			return result, errExecutionAuthorization
		}
		if previousSecret.Valid {
			var sealed []byte
			if err = tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id=?`, previousSecret.String).Scan(&sealed); err != nil {
				return result, err
			}
			raw, openErr := secret.Open(s.key, sealed, []byte("deployment:"+app.DeploymentID))
			if openErr != nil || json.Unmarshal(raw, &previous) != nil {
				return result, errors.New("center: previous credential evidence unavailable")
			}
		}
		keys := []string{"token"}
		if app.AppKey == "vastora-official/glivela-entry" {
			keys = []string{"private_key_pem", "certificate_pem"}
		}
		for _, key := range keys {
			if strings.TrimSpace(fresh[key]) == "" || fresh[key] == previous[key] {
				return result, errors.New("center: supply new application credentials; retired credentials cannot be reused")
			}
		}
	}
	token, err := randomToken(18)
	if err != nil {
		return result, err
	}
	id := "reinstall-current-" + token
	task.ID = id
	task.Kind = "application.apply"
	task.Attempt = 1
	task.Revision = applicationTaskRevision
	task.AppKey = app.AppKey
	task.ApplicationID = app.ApplicationID
	task.Operation = "install"
	task.Config = config
	task.Secrets = credentials
	task.RequiredRuntimeGeneration = platform.ApplicationRuntimeGeneration
	task.SenulunRestore = input.SenulunRestore
	task.ServiceAddress = plan.NetworkReview.Approval.Profile.ServiceAddress
	if app.AppKey == senulunAgentAppKey {
		task.Secrets = json.RawMessage(`{}`)
		task.ServiceAddress = ""
	}
	var secretID any
	if string(task.Secrets) != "{}" {
		secretID, err = s.putSecret(ctx, tx, task.Secrets, "deployment:"+id)
		if err != nil {
			return result, err
		}
	}
	stamp := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO deployments(id,agent_id,app_key,app_version,manifest_json,config_json,secret_id,operation,state,created_at,updated_at,application_id,service_address,runtime_generation,pre_dispatch_application_status) VALUES(?,?,?,?,?,?,?,'install','pending',?,?,?,?,?,(SELECT status FROM applications WHERE id=?))`, id, agentID, app.AppKey, app.Version, manifest, config, secretID, stamp, stamp, app.ApplicationID, task.ServiceAddress, platform.ApplicationRuntimeGeneration, app.ApplicationID)
	if err != nil {
		return result, err
	}
	if err = copyReinstallPackageAuthorization(ctx, tx, app.DeploymentID, id); err != nil {
		return result, err
	}
	source := AgentReinstallPlan{AgentID: agentID}
	// The new deployment is excluded by registration before source hashing below.
	var key []byte
	if err = tx.QueryRowContext(ctx, `SELECT x25519_public_key FROM agents WHERE id=?`, agentID).Scan(&key); err != nil {
		return result, err
	}
	approval, _ := json.Marshal(plan.NetworkReview.Approval)
	raw, _ := json.Marshal(task)
	sealed, err := secret.Seal(s.key, raw, []byte("reinstall-current:"+id))
	if err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_reinstall_current_applications(deployment_id,operation_id,application_id,source_deployment_id,source_revision,replacement_key,approval_json,sealed_task) VALUES(?,?,?,?,'',?,?,?)`, id, op.ID, app.ApplicationID, app.DeploymentID, key, approval, sealed)
	if err != nil {
		return result, err
	}
	sourceRevision, err := readReinstallApplications(ctx, tx, &source)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_current_applications SET source_revision=? WHERE deployment_id=?`, sourceRevision, id); err != nil {
		return result, err
	}
	if _, err = s.reinstallCurrentTask(ctx, tx, agentID, id); err != nil {
		return result, err
	}
	if err = s.recordTaskEvent(ctx, tx, id, agentID, "application.apply", applicationTaskRevision, "queued", "Restore original current application with renewed credentials; business acceptance remains required"); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	s.taskChanges.notify("agent:" + agentID)
	return AgentReinstallMonitorRestore{DeploymentID: id, State: "pending"}, nil
}

// Immutable replacement-bound exception. Revalidate at claim, execution
// authorization and result projection; no normal work is unfenced.
func (s *Store) reinstallCurrentTask(ctx context.Context, tx *sql.Tx, agentID, id string) (*AgentTask, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_reinstall_current_applications WHERE deployment_id=?`, id).Scan(&count); err != nil {
		return nil, err
	}
	if count == 0 {
		if strings.HasPrefix(id, "reinstall-current-") {
			return nil, errExecutionAuthorization
		}
		return nil, nil
	}
	var sealed, approval, manifest, config []byte
	var source, app, key, version, operation, address string
	var generation, attempt int
	var invalid bool
	err := tx.QueryRowContext(ctx, `SELECT r.sealed_task,r.approval_json,r.source_revision,d.application_id,d.app_key,d.app_version,d.manifest_json,d.config_json,d.operation,d.service_address,d.runtime_generation,d.attempt,d.delete_data OR d.reconciliation_required OR d.reconciliation_requested OR d.registry_credential_id IS NOT NULL
 FROM agent_reinstall_current_applications r JOIN deployments d ON d.id=r.deployment_id JOIN agent_reinstall_operations op ON op.id=r.operation_id JOIN agents a ON a.id=op.agent_id
 WHERE d.id=? AND d.agent_id=? AND op.agent_id=d.agent_id AND r.application_id=d.application_id AND op.state='review_required' AND op.private_isolation IN ('withdrawn','not_required') AND a.x25519_public_key=r.replacement_key AND a.status='active' AND a.credential_revoked_at='' AND EXISTS(SELECT 1 FROM admins WHERE id=op.authorized_by)`, id, agentID).Scan(&sealed, &approval, &source, &app, &key, &version, &manifest, &config, &operation, &address, &generation, &attempt, &invalid)
	if err != nil || invalid || attempt > 1 || operation != "install" || !currentReinstallApp(key) {
		return nil, errExecutionAuthorization
	}
	raw, err := secret.Open(s.key, sealed, []byte("reinstall-current:"+id))
	if err != nil {
		return nil, errExecutionAuthorization
	}
	var task AgentTask
	if json.Unmarshal(raw, &task) != nil || task.ID != id || task.ApplicationID != app || task.AppKey != key || task.Manifest.Version != version || task.Operation != operation || task.ServiceAddress != address || task.RequiredRuntimeGeneration != generation || string(task.Config) != string(config) {
		return nil, errExecutionAuthorization
	}
	actualManifest, _ := json.Marshal(task.Manifest)
	var savedManifest catalog.AppManifest
	if json.Unmarshal(manifest, &savedManifest) != nil {
		return nil, errExecutionAuthorization
	}
	canonicalManifest, _ := json.Marshal(savedManifest)
	if string(actualManifest) != string(canonicalManifest) {
		return nil, errExecutionAuthorization
	}
	authorization := AgentTask{Manifest: task.Manifest}
	if err = readReinstallPackageAuthorization(ctx, tx, id, &authorization); err != nil {
		return nil, err
	}
	if authorization.PackageRevision != task.PackageRevision || authorization.ManifestSHA256 != task.ManifestSHA256 || !slices.Equal(authorization.AuthorizedCapabilities, task.AuthorizedCapabilities) {
		return nil, errExecutionAuthorization
	}
	plan := AgentReinstallPlan{AgentID: agentID}
	revision, err := readReinstallApplications(ctx, tx, &plan)
	if err != nil {
		return nil, err
	}
	if revision != source {
		return nil, errors.New("center: current application recovery source changed")
	}
	review, err := s.agentReinstallNetworkReview(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	if review == nil || !review.ApprovalCurrent || review.Approval == nil {
		return nil, errExecutionAuthorization
	}
	current, _ := json.Marshal(review.Approval)
	if string(current) != string(approval) {
		return nil, errExecutionAuthorization
	}
	// Decryption proves the immutable approved task; execution authorization also
	// compares the actual issued task including fresh secrets to this exact value.
	return &task, nil
}

func (s *Server) handleAgentReinstallCurrent(w http.ResponseWriter, r *http.Request) {
	var input AgentReinstallCurrentInput
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	admin, err := s.requestAdminID(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err)
		return
	}
	result, err := s.store.QueueAgentReinstallCurrent(r.Context(), r.PathValue("id"), admin, input)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, result)
}
