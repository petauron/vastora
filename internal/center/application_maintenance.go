package center

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

// Maintenance accepts identifiers, never arbitrary host paths or commands.
type PackageMaintenanceTask struct {
	Action   string `json:"action"`
	BackupID string `json:"backupId,omitempty"`
}
type RuntimeLog struct {
	Resource string `json:"resource"`
	Content  string `json:"content"`
}
type PackageMaintenanceResult struct {
	Logs     []RuntimeLog `json:"logs,omitempty"`
	BackupID string       `json:"backupId,omitempty"`
}
type ApplicationMaintenanceView struct {
	ID                     string                    `json:"id"`
	ApplicationID          string                    `json:"applicationId"`
	Action                 string                    `json:"action"`
	BackupID               string                    `json:"backupId,omitempty"`
	State                  string                    `json:"state"`
	Error                  string                    `json:"error,omitempty"`
	ReconciliationRequired bool                      `json:"reconciliationRequired"`
	Result                 *PackageMaintenanceResult `json:"result,omitempty"`
	CreatedAt              string                    `json:"createdAt"`
	UpdatedAt              string                    `json:"updatedAt"`
}
type ApplicationBackupView struct {
	ID             string    `json:"id"`
	PackageVersion string    `json:"packageVersion"`
	CreatedAt      time.Time `json:"createdAt"`
	Resources      []string  `json:"resources"`
	Restorable     bool      `json:"restorable"`
}
type maintenanceReceipt struct {
	Version                int      `json:"version"`
	ApplicationID          string   `json:"applicationId"`
	AppKey                 string   `json:"appKey"`
	Runtime                string   `json:"runtime"`
	PackageVersion         string   `json:"packageVersion"`
	PackageRevision        int      `json:"packageRevision"`
	ManifestSHA256         string   `json:"manifestSha256"`
	AuthorizedCapabilities []string `json:"authorizedCapabilities"`
	State                  string   `json:"state"`
	TaskID                 string   `json:"taskId"`
	Resources              []struct {
		Name        string `json:"name"`
		LogicalName string `json:"logicalName"`
	} `json:"resources"`
	Backups []maintenanceBackup `json:"backups"`
}
type maintenanceBackup struct {
	ID             string    `json:"id"`
	Target         string    `json:"target,omitempty"`
	Resource       string    `json:"resource"`
	Path           string    `json:"path"`
	SHA256         string    `json:"sha256"`
	PackageVersion string    `json:"packageVersion"`
	CreatedAt      time.Time `json:"createdAt"`
}

func decodeMaintenanceReceipt(raw []byte) (maintenanceReceipt, error) {
	var receipt maintenanceReceipt
	if len(raw) > 1<<20 || json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.ApplicationID == "" || receipt.AppKey == "" || len(receipt.Resources) == 0 || (receipt.Runtime != "docker" && receipt.Runtime != "systemd") || len(receipt.ManifestSHA256) != 64 {
		return receipt, errors.New("center: invalid installed resource evidence")
	}
	return receipt, nil
}

func validateMaintenanceBackup(receipt maintenanceReceipt, id string) error {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\\x00\r\n") {
		return errors.New("center: invalid recorded backup ID")
	}
	found := false
	for _, backup := range receipt.Backups {
		if backup.ID != id {
			continue
		}
		if backup.PackageVersion != receipt.PackageVersion {
			return errors.New("center: backup belongs to a different package version; matched offline recovery is required")
		}
		if decoded, err := hex.DecodeString(backup.SHA256); err != nil || len(decoded) != 32 || backup.Resource == "" {
			return errors.New("center: recorded backup integrity evidence is incomplete")
		}
		found = true
	}
	if !found {
		return errors.New("center: backup ID is not recorded for this instance")
	}
	return nil
}

func (s *Store) QueueApplicationMaintenance(ctx context.Context, applicationID string, input PackageMaintenanceTask) (string, error) {
	if !slices.Contains([]string{"logs", "backup", "restore"}, input.Action) || input.Action != "restore" && input.BackupID != "" {
		return "", errors.New("center: invalid package maintenance request")
	}
	s.deploymentCreateMu.Lock()
	defer s.deploymentCreateMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var node, appKey, deploymentID, adoption string
	var raw, manifestRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT a.node_id,a.app_key,r.adoption_state,r.resources_json,d.id,d.manifest_json FROM applications a
	 JOIN application_resources r ON r.application_id=a.id
	 JOIN deployments d ON d.rowid=(SELECT rowid FROM deployments WHERE application_id=a.id AND state='succeeded' ORDER BY created_at DESC,rowid DESC LIMIT 1)
	 WHERE a.id=? AND d.operation<>'uninstall'`, applicationID).Scan(&node, &appKey, &adoption, &raw, &deploymentID, &manifestRaw)
	if err != nil || adoption != "ready" {
		return "", errors.New("center: maintenance requires verified ready application resources")
	}
	receipt, err := decodeMaintenanceReceipt(raw)
	if err != nil {
		return "", err
	}
	if receipt.ApplicationID != applicationID || receipt.AppKey != appKey || receipt.State != "ready" {
		return "", errors.New("center: installed resource identity or health does not match")
	}
	var manifest catalog.AppManifest
	if json.Unmarshal(manifestRaw, &manifest) != nil {
		return "", errors.New("center: installed manifest is invalid")
	}
	digest, err := maintenanceManifestDigest(manifestRaw, manifest)
	if err != nil {
		return "", err
	}
	if digest != receipt.ManifestSHA256 || manifest.Version != receipt.PackageVersion || manifest.PackageRevision != receipt.PackageRevision {
		return "", errors.New("center: installed manifest does not match resource receipt")
	}
	if input.Action == "restore" {
		if err := validateMaintenanceBackup(receipt, input.BackupID); err != nil {
			return "", err
		}
	}
	if err := maintenanceNodeIdle(ctx, tx, node, ""); err != nil {
		return "", err
	}
	id, err := randomToken(18)
	if err != nil {
		return "", err
	}
	id = "application-maintenance-" + id
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_maintenance(id,application_id,agent_id,deployment_id,action,backup_id,resources_json,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,'pending',?,?)`, id, applicationID, node, deploymentID, input.Action, input.BackupID, raw, now, now); err != nil {
		return "", err
	}
	if err = s.recordTaskEvent(ctx, tx, id, node, "application.maintenance", 1, "queued", "Package "+input.Action+" requested against recorded instance resources"); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func maintenanceNodeIdle(ctx context.Context, tx *sql.Tx, node, exclude string) error {
	var active int
	err := tx.QueryRowContext(ctx, `SELECT
	 (SELECT COUNT(*) FROM deployments WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+
	 (SELECT COUNT(*) FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))+
	 (SELECT COUNT(*) FROM application_adoptions WHERE agent_id=? AND state IN ('pending','running'))+
	 (SELECT COUNT(*) FROM application_maintenance WHERE agent_id=? AND id<>? AND (state IN ('pending','running') OR reconciliation_required=1))+
	 (SELECT COUNT(*) FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')`, node, node, node, node, exclude, node).Scan(&active)
	if err != nil {
		return err
	}
	if active != 0 {
		return errors.New("center: resolve unfinished node operations before package maintenance")
	}
	return nil
}

func maintenanceManifestDigest(raw []byte, manifest catalog.AppManifest) (string, error) {
	if manifest.PackageRevision > 0 {
		_, _, digest, err := canonicalPackage(manifest)
		return digest, err
	}
	return rawManifestDigest(raw), nil
}

func (s *Store) maintenanceSecrets(ctx context.Context, tx *sql.Tx, deploymentID string) (json.RawMessage, error) {
	var sealed []byte
	err := tx.QueryRowContext(ctx, `SELECT secret.sealed FROM deployments d JOIN secrets secret ON secret.id=d.secret_id WHERE d.id=?`, deploymentID).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return json.RawMessage(`{}`), nil
	}
	if err != nil {
		return nil, err
	}
	return secret.Open(s.key, sealed, []byte("deployment:"+deploymentID))
}

func (s *Store) claimApplicationMaintenance(ctx context.Context, tx *sql.Tx, agentID, requiredID string) (*AgentTask, error) {
	var task AgentTask
	var input PackageMaintenanceTask
	var deploymentID, adoption string
	var attempt int64
	var raw, manifestRaw, current []byte
	err := tx.QueryRowContext(ctx, `SELECT q.id,q.application_id,q.attempt,q.deployment_id,q.action,q.backup_id,q.resources_json,a.app_key,a.role,d.manifest_json,d.config_json,d.service_address,r.resources_json,r.adoption_state
	 FROM application_maintenance q JOIN applications a ON a.id=q.application_id JOIN deployments d ON d.id=q.deployment_id JOIN application_resources r ON r.application_id=q.application_id
	 WHERE q.agent_id=? AND q.state='pending' AND (?='' OR q.id=?) ORDER BY q.created_at,q.rowid LIMIT 1`, agentID, requiredID, requiredID).Scan(&task.ID, &task.ApplicationID, &attempt, &deploymentID, &input.Action, &input.BackupID, &raw, &task.AppKey, &task.ApplicationRole, &manifestRaw, &task.Config, &task.ServiceAddress, &current, &adoption)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := maintenanceNodeIdle(ctx, tx, agentID, task.ID); err != nil {
		return nil, err
	}
	if adoption != "ready" || string(current) != string(raw) {
		return nil, errors.New("center: resource evidence changed after maintenance was queued")
	}
	receipt, err := decodeMaintenanceReceipt(raw)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(manifestRaw, &task.Manifest) != nil {
		return nil, errors.New("center: stored maintenance manifest is invalid")
	}
	if task.Manifest.Runtime != nil {
		if err := requireNodeRuntime(ctx, tx, agentID, task.Manifest); err != nil {
			return nil, err
		}
	} else {
		task.HistoricalManifest = manifestRaw
	}
	task.Kind = "application.maintenance"
	task.Operation = input.Action
	task.PackageMaintenance = &input
	task.Attempt = attempt + 1
	task.Revision = 1
	task.Resources = raw
	task.PackageRevision = receipt.PackageRevision
	task.ManifestSHA256 = receipt.ManifestSHA256
	task.AuthorizedCapabilities = receipt.AuthorizedCapabilities
	task.Secrets, err = s.maintenanceSecrets(ctx, tx, deploymentID)
	if err != nil {
		return nil, err
	}
	var registryID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT registry_credential_id FROM deployments WHERE id=?`, deploymentID).Scan(&registryID); err != nil {
		return nil, err
	}
	if registryID.Valid {
		var credential AgentRegistryCredential
		var sealed []byte
		if err := tx.QueryRowContext(ctx, `SELECT r.host,r.username,s.sealed FROM registry_credentials r JOIN secrets s ON s.id=r.secret_id WHERE r.id=?`, registryID.String).Scan(&credential.Host, &credential.Username, &sealed); err != nil {
			return nil, err
		}
		password, err := secret.Open(s.key, sealed, []byte("registry-credential:"+registryID.String))
		if err != nil {
			return nil, err
		}
		credential.Password = string(password)
		task.RegistryCredential = &credential
	}
	now := s.now().UTC()
	updated, err := tx.ExecContext(ctx, `UPDATE application_maintenance SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND state='pending' AND attempt=?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), task.ID, attempt)
	if err != nil {
		return nil, err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return nil, errStaleTaskLease
	}
	if err := s.recordTaskEvent(ctx, tx, task.ID, agentID, task.Kind, 1, "claimed", "Package maintenance claimed"); err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) completeApplicationMaintenance(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage, reconciliation bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.projectApplicationMaintenance(ctx, tx, agentID, id, attempt, succeeded, taskError, raw, reconciliation); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Store) projectApplicationMaintenance(ctx context.Context, tx *sql.Tx, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage, reconciliation bool) error {
	var appID, action, deploymentID string
	var previous []byte
	if err := tx.QueryRowContext(ctx, `SELECT application_id,action,deployment_id,resources_json FROM application_maintenance WHERE id=? AND agent_id=? AND attempt=? AND state='running'`, id, agentID, attempt).Scan(&appID, &action, &deploymentID, &previous); err != nil {
		return err
	}
	expected, err := decodeMaintenanceReceipt(previous)
	if err != nil {
		return err
	}
	secretsRaw, err := s.maintenanceSecrets(ctx, tx, deploymentID)
	if err != nil {
		return err
	}
	var secrets map[string]string
	if json.Unmarshal(secretsRaw, &secrets) != nil {
		return errors.New("center: invalid maintenance credentials")
	}
	redact := func(value string) string {
		for _, credential := range secrets {
			if credential != "" {
				value = strings.ReplaceAll(value, credential, "[REDACTED]")
			}
		}
		return value
	}
	taskError = controlplane.SafeError(redact(taskError))
	state := "failed"
	resultRaw := []byte(`{}`)
	var result ApplicationTaskResult
	if len(raw) > 1<<20 {
		return errors.New("center: oversized maintenance result")
	}
	if len(raw) > 0 && json.Unmarshal(raw, &result) != nil {
		return errors.New("center: invalid maintenance result")
	}
	if succeeded || len(result.Resources) > 0 {
		receipt, err := decodeMaintenanceReceipt(result.Resources)
		if err != nil {
			return err
		}
		slices.Sort(receipt.AuthorizedCapabilities)
		slices.Sort(expected.AuthorizedCapabilities)
		if receipt.ApplicationID != appID || receipt.AppKey != expected.AppKey || receipt.TaskID != id || receipt.Runtime != expected.Runtime || receipt.PackageVersion != expected.PackageVersion || receipt.PackageRevision != expected.PackageRevision || receipt.ManifestSHA256 != expected.ManifestSHA256 || !slices.Equal(receipt.AuthorizedCapabilities, expected.AuthorizedCapabilities) {
			return errors.New("center: maintenance resource identity mismatch")
		}
		if succeeded && (receipt.State != "ready" || result.PackageMaintenance == nil) {
			return errors.New("center: maintenance result does not prove healthy completion")
		}
		if !succeeded && receipt.State != "review-required" {
			return errors.New("center: failed maintenance must retain review-required evidence")
		}
		for _, old := range expected.Backups {
			if !slices.Contains(receipt.Backups, old) {
				return errors.New("center: maintenance discarded recorded backup evidence")
			}
		}
		for _, backup := range receipt.Backups {
			if !slices.Contains(expected.Backups, backup) && backup.ID != id {
				return errors.New("center: maintenance returned an unrelated backup")
			}
		}
		if succeeded && action != "logs" {
			if result.PackageMaintenance.BackupID != id {
				return errors.New("center: maintenance backup identity mismatch")
			}
			if err := validateMaintenanceBackup(receipt, id); err != nil {
				return err
			}
		}
		if succeeded && action == "logs" && (result.PackageMaintenance.BackupID != "" || !slices.Equal(receipt.Backups, expected.Backups)) {
			return errors.New("center: log inspection cannot modify backup evidence")
		}
		if succeeded && action == "logs" {
			var before, after struct {
				Resources []any `json:"resources"`
			}
			if json.Unmarshal(previous, &before) != nil || json.Unmarshal(result.Resources, &after) != nil || !reflect.DeepEqual(before.Resources, after.Resources) {
				return errors.New("center: log inspection cannot change instance resource ownership")
			}
		}
		adoption := "ready"
		if !succeeded {
			adoption = "blocked"
			reconciliation = true
		}
		if _, err := tx.ExecContext(ctx, `UPDATE application_resources SET resources_json=?,adoption_state=?,last_error=?,updated_at=? WHERE application_id=?`, result.Resources, adoption, taskError, s.now().UTC().Format(time.RFC3339Nano), appID); err != nil {
			return err
		}
	}
	if succeeded {
		if action != "logs" && len(result.PackageMaintenance.Logs) != 0 {
			return errors.New("center: unexpected maintenance logs")
		}
		if len(result.PackageMaintenance.Logs) > 64 {
			return errors.New("center: too many maintenance log resources")
		}
		for i := range result.PackageMaintenance.Logs {
			log := &result.PackageMaintenance.Logs[i]
			known := false
			for _, resource := range expected.Resources {
				if log.Resource == resource.Name || log.Resource == resource.LogicalName {
					known = true
				}
			}
			if !known || len(log.Content) > 64<<10 {
				return errors.New("center: unexpected or oversized maintenance log")
			}
			log.Content = redact(log.Content)
		}
		resultRaw, err = json.Marshal(result.PackageMaintenance)
		if err != nil {
			return err
		}
		state = "succeeded"
		taskError = ""
	} else if action != "logs" {
		// A missing failure receipt cannot establish whether writers stopped or
		// data changed. Preserve the last receipt and block further mutation.
		reconciliation = true
		if _, err := tx.ExecContext(ctx, `UPDATE application_resources SET adoption_state='blocked',last_error=?,updated_at=? WHERE application_id=?`, taskError, s.now().UTC().Format(time.RFC3339Nano), appID); err != nil {
			return err
		}
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `UPDATE application_maintenance SET state=?,error=?,result_json=?,reconciliation_required=?,lease_expires_at='',updated_at=? WHERE id=?`, state, taskError, resultRaw, reconciliation, now, id); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, id, agentID, "application.maintenance", 1, state, "Package maintenance "+state)
}

func (s *Store) ApplicationMaintenance(ctx context.Context, applicationID, id string) (ApplicationMaintenanceView, error) {
	var view ApplicationMaintenanceView
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,application_id,action,backup_id,state,error,reconciliation_required,result_json,created_at,updated_at FROM application_maintenance WHERE application_id=? AND id=?`, applicationID, id).Scan(&view.ID, &view.ApplicationID, &view.Action, &view.BackupID, &view.State, &view.Error, &view.ReconciliationRequired, &raw, &view.CreatedAt, &view.UpdatedAt)
	if err != nil {
		return view, err
	}
	if view.State == "succeeded" {
		if err = json.Unmarshal(raw, &view.Result); err != nil {
			return view, err
		}
	}
	return view, nil
}

func (s *Store) ApplicationBackups(ctx context.Context, applicationID string) ([]ApplicationBackupView, error) {
	var raw []byte
	var state string
	if err := s.db.QueryRowContext(ctx, `SELECT resources_json,adoption_state FROM application_resources WHERE application_id=?`, applicationID).Scan(&raw, &state); err != nil {
		return nil, err
	}
	receipt, err := decodeMaintenanceReceipt(raw)
	if err != nil {
		return nil, err
	}
	result := []ApplicationBackupView{}
	indexes := map[string]int{}
	for _, backup := range receipt.Backups {
		index, ok := indexes[backup.ID]
		if !ok {
			index = len(result)
			indexes[backup.ID] = index
			result = append(result, ApplicationBackupView{ID: backup.ID, PackageVersion: backup.PackageVersion, CreatedAt: backup.CreatedAt, Resources: []string{}, Restorable: state == "ready" && receipt.State == "ready" && validateMaintenanceBackup(receipt, backup.ID) == nil && !(receipt.Runtime == "docker" && receipt.PackageRevision == 0)})
		}
		result[index].Resources = append(result[index].Resources, backup.Resource)
	}
	return result, nil
}
func (s *Server) handleQueueApplicationMaintenance(w http.ResponseWriter, r *http.Request) {
	var input PackageMaintenanceTask
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.store.QueueApplicationMaintenance(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"taskId": id})
}
func (s *Server) handleApplicationMaintenance(w http.ResponseWriter, r *http.Request) {
	view, err := s.store.ApplicationMaintenance(r.Context(), r.PathValue("id"), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("center: maintenance task was not found"))
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleApplicationBackups(w http.ResponseWriter, r *http.Request) {
	backups, err := s.store.ApplicationBackups(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("center: application backup evidence was not found"))
		return
	}
	writeJSON(w, http.StatusOK, backups)
}
