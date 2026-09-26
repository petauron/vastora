package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
)

// QueueApplicationAdoption only records a request to inspect existing resources.
// It never reads a current catalog or creates desired runtime state.
func (s *Store) QueueApplicationAdoption(ctx context.Context, applicationID string) (string, error) {
	s.deploymentCreateMu.Lock()
	defer s.deploymentCreateMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var node, state, deploymentID string
	err = tx.QueryRowContext(ctx, `SELECT a.node_id,r.adoption_state,d.id FROM applications a
		JOIN application_resources r ON r.application_id=a.id
		JOIN deployments d ON d.rowid=(SELECT previous.rowid FROM deployments previous WHERE previous.application_id=a.id AND previous.state='succeeded' ORDER BY previous.created_at DESC,previous.rowid DESC LIMIT 1)
		WHERE a.id=? AND d.operation<>'uninstall'`, applicationID).Scan(&node, &state, &deploymentID)
	if err != nil {
		return "", errors.New("center: historical installed resources were not found")
	}
	if state == "ready" {
		return "", errors.New("center: application resources are already adopted")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM deployments WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1)) + (SELECT COUNT(*) FROM application_commands WHERE application_id=? AND (state IN ('pending','running') OR reconciliation_required=1)) + (SELECT COUNT(*) FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded') + (SELECT COUNT(*) FROM application_maintenance WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))`, applicationID, applicationID, node, node).Scan(&active); err != nil {
		return "", err
	}
	if active != 0 {
		return "", errors.New("center: resolve unfinished node operations before resource adoption")
	}
	var existingID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM application_adoptions WHERE application_id=?`, applicationID).Scan(&existingID)
	if err == nil {
		return existingID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id, err := randomToken(18)
	if err != nil {
		return "", err
	}
	id = "application-adopt-" + id
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO application_adoptions(id,application_id,agent_id,deployment_id,state,created_at,updated_at) VALUES(?,?,?,?,'pending',?,?)`, id, applicationID, node, deploymentID, now, now); err != nil {
		return "", err
	}
	if err = s.recordTaskEvent(ctx, tx, id, node, "application.adopt", 1, "queued", "Inspect historical resources without restarting or modifying application data"); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func (s *Store) claimApplicationAdoption(ctx context.Context, tx *sql.Tx, agentID, requiredID string) (*AgentTask, error) {
	var task AgentTask
	var attempt int64
	var capabilitiesRaw []byte
	var historical, config []byte
	err := tx.QueryRowContext(ctx, `SELECT q.id,q.application_id,q.attempt,a.app_key,a.role,d.manifest_json,d.config_json,d.service_address,node.capabilities_json
		FROM application_adoptions q JOIN applications a ON a.id=q.application_id JOIN deployments d ON d.id=q.deployment_id JOIN agents node ON node.id=q.agent_id
		WHERE q.agent_id=? AND q.state='pending' AND (?='' OR q.id=?) ORDER BY q.created_at LIMIT 1`, agentID, requiredID, requiredID).Scan(&task.ID, &task.ApplicationID, &attempt, &task.AppKey, &task.ApplicationRole, &historical, &config, &task.ServiceAddress, &capabilitiesRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	task.HistoricalManifest, task.Config = historical, config
	if json.Unmarshal(task.HistoricalManifest, &task.Manifest) != nil || task.Manifest.Runtime != nil || task.Manifest.PackageRevision != 0 {
		return nil, errors.New("center: adoption requires an unmodified historical manifest")
	}
	var capabilities NodeCapabilities
	if json.Unmarshal(capabilitiesRaw, &capabilities) != nil || capabilities.ExecutorVersions["docker"] != 1 && capabilities.ExecutorVersions["systemd"] != 1 {
		return nil, errors.New("center: update the Agent before resource adoption")
	}
	digest := sha256.Sum256(task.HistoricalManifest)
	task.ManifestSHA256 = hex.EncodeToString(digest[:])
	task.Kind = "application.adopt"
	task.Operation = "adopt"
	task.Attempt = attempt + 1
	task.Revision = 1
	task.Secrets = json.RawMessage(`{}`)
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE application_adoptions SET state='running',attempt=attempt+1,lease_expires_at=?,updated_at=? WHERE id=? AND state='pending' AND attempt=?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), task.ID, attempt); err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) completeApplicationAdoption(ctx context.Context, commit projectionCommit, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.projectApplicationAdoption(ctx, tx, agentID, id, attempt, succeeded, taskError, raw); err != nil {
		return err
	}
	return commit(tx)
}

func (s *Store) projectApplicationAdoption(ctx context.Context, tx *sql.Tx, agentID, id string, attempt int64, succeeded bool, taskError string, raw json.RawMessage) error {
	var appID, appKey, version string
	var historical []byte
	if err := tx.QueryRowContext(ctx, `SELECT q.application_id,a.app_key,d.app_version,d.manifest_json FROM application_adoptions q JOIN applications a ON a.id=q.application_id JOIN deployments d ON d.id=q.deployment_id WHERE q.id=? AND q.agent_id=? AND q.attempt=? AND q.state='running'`, id, agentID, attempt).Scan(&appID, &appKey, &version, &historical); err != nil {
		return err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	state, adoptionState := "failed", "blocked"
	if succeeded {
		var result ApplicationTaskResult
		var receipt struct {
			Version                int               `json:"version"`
			ApplicationID          string            `json:"applicationId"`
			AppKey                 string            `json:"appKey"`
			TaskID                 string            `json:"taskId"`
			PackageVersion         string            `json:"packageVersion"`
			PackageRevision        int               `json:"packageRevision"`
			ManifestSHA256         string            `json:"manifestSha256"`
			State                  string            `json:"state"`
			Resources              []json.RawMessage `json:"resources"`
			AuthorizedCapabilities []string          `json:"authorizedCapabilities"`
		}
		if len(raw) > 1<<20 || json.Unmarshal(raw, &result) != nil || json.Unmarshal(result.Resources, &receipt) != nil {
			return errors.New("center: invalid adoption resource receipt")
		}
		digest := sha256.Sum256(historical)
		if receipt.Version != 1 || receipt.ApplicationID != appID || receipt.AppKey != appKey || receipt.TaskID != id || receipt.PackageVersion != version || receipt.PackageRevision != 0 || receipt.ManifestSHA256 != hex.EncodeToString(digest[:]) || receipt.State != "ready" || len(receipt.Resources) == 0 {
			return errors.New("center: adoption resource receipt does not match historical evidence")
		}
		grants, err := json.Marshal(receipt.AuthorizedCapabilities)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE application_resources SET manifest_sha256=?,package_revision=0,authorized_capabilities_json=?,resources_json=? WHERE application_id=?`, receipt.ManifestSHA256, grants, result.Resources, appID); err != nil {
			return err
		}
		state, adoptionState = "succeeded", "ready"
		taskError = ""
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_resources SET adoption_state=?,last_error=?,updated_at=? WHERE application_id=?`, adoptionState, controlplane.SafeError(taskError), now, appID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_adoptions SET state=?,error=?,lease_expires_at='',updated_at=? WHERE id=?`, state, controlplane.SafeError(taskError), now, id); err != nil {
		return err
	}
	return s.recordTaskEvent(ctx, tx, id, agentID, "application.adopt", 1, state, "Historical resource verification completed; application runtime unchanged")
}

func (s *Server) handleAdoptApplication(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		BackupsConfirmed bool `json:"backupsConfirmed"`
	}
	if err := decodeJSON(request, &input); err != nil || !input.BackupsConfirmed {
		writeError(writer, http.StatusBadRequest, errors.New("center: confirm independent Center, Agent, and application data backups before adoption"))
		return
	}
	id, err := s.store.QueueApplicationAdoption(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusConflict, err)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]string{"taskId": id})
}
