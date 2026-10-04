package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
)

// A receipt may be reopened only by the immediately preceding operator-reviewed
// attempt. This runs in the claim transaction before sealing the new task.
func (s *Store) attachPackageRecovery(ctx context.Context, tx *sql.Tx, agentID string, task *AgentTask) error {
	task.PackageRecovery = nil
	if task.Kind != "application.apply" || task.Attempt < 2 || (task.Operation != "upgrade" && task.Operation != "configure") {
		return nil
	}
	var id, state, disposition, retired string
	var sealed []byte
	err := tx.QueryRowContext(ctx, `SELECT id,state,disposition,identity_retired_at,sealed_task FROM task_executions
		WHERE agent_id=? AND task_id=? AND kind=? AND attempt=?`, agentID, task.ID, task.Kind, task.Attempt-1).Scan(&id, &state, &disposition, &retired, &sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if disposition != "reexecute" || retired != "" || (state != "failed" && state != "unknown") {
		return nil
	}
	raw, err := secret.Open(s.key, sealed, []byte("execution-task:"+id))
	if err != nil {
		return errors.New("center: package recovery evidence cannot be verified")
	}
	var previous AgentTask
	if json.Unmarshal(raw, &previous) != nil || previous.ID != task.ID || previous.Kind != task.Kind || previous.Attempt != task.Attempt-1 || previous.ApplicationID != task.ApplicationID || previous.AppKey != task.AppKey || previous.Operation != task.Operation || previous.ManifestSHA256 != task.ManifestSHA256 || previous.PackageRevision != task.PackageRevision || previous.Manifest.Version != task.Manifest.Version {
		return errors.New("center: package recovery does not match the reviewed task")
	}
	task.PackageRecovery = &controlplane.PackageRecovery{ExecutionID: id, TaskID: task.ID, Attempt: previous.Attempt}
	return nil
}
