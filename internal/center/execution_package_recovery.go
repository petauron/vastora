package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/secret"
	"golang.org/x/mod/semver"
)

// A receipt may be reopened only by the immediately preceding operator-reviewed
// attempt. This runs in the claim transaction before sealing the new task.
func (s *Store) attachPackageRecovery(ctx context.Context, tx *sql.Tx, agentID string, task *AgentTask) error {
	task.PackageRecovery = nil
	nativeHandover := task.Operation == "install" && task.SourceReceiptSHA256 != ""
	nativeInstall := task.Operation == "install" && task.SourceReceiptSHA256 == "" && task.Manifest.Runtime != nil && (task.Manifest.Runtime.Kind == "systemd" || task.Manifest.Runtime.Kind == "native") && task.Manifest.Runtime.Version == 2
	if task.Kind != "application.apply" || (task.Operation != "upgrade" && task.Operation != "configure" && !nativeHandover && !nativeInstall) {
		return nil
	}
	if task.Attempt == 1 && (task.Operation == "upgrade" || nativeInstall) {
		return s.attachPackageSuccessorRecovery(ctx, tx, agentID, task)
	}
	// Corrected first installs use a new task and the exact abandoned receipt,
	// never the unchanged same-task retry path.
	if nativeInstall {
		return nil
	}
	if task.Attempt < 2 {
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
	if json.Unmarshal(raw, &previous) != nil || previous.ID != task.ID || previous.Kind != task.Kind || previous.Attempt != task.Attempt-1 || previous.ApplicationID != task.ApplicationID || previous.AppKey != task.AppKey || previous.Operation != task.Operation || previous.ManifestSHA256 != task.ManifestSHA256 || previous.PackageRevision != task.PackageRevision || previous.Manifest.Version != task.Manifest.Version || previous.SourceReceiptSHA256 != task.SourceReceiptSHA256 {
		return errors.New("center: package recovery does not match the reviewed task")
	}
	task.PackageRecovery = &controlplane.PackageRecovery{ExecutionID: id, TaskID: task.ID, Attempt: previous.Attempt}
	return nil
}

// A fresh upgrade or corrected first native install can follow an abandoned
// failed task only when its authenticated result identifies the exact locked
// receipt. Both the latest attempt and the mutating predecessor are reviewed.
func (s *Store) attachPackageSuccessorRecovery(ctx context.Context, tx *sql.Tx, agentID string, task *AgentTask) error {
	var latestID string
	err := tx.QueryRowContext(ctx, `SELECT e.id FROM task_executions e
  JOIN deployments d ON d.id=e.task_id AND d.agent_id=e.agent_id
  WHERE e.agent_id=? AND d.application_id=? AND e.kind='application.apply'
  ORDER BY e.rowid DESC LIMIT 1`, agentID, task.ApplicationID).Scan(&latestID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	previous, receipt, rawReceipt, err := s.reviewedAbandonedPackage(ctx, tx, agentID, latestID)
	if err != nil || previous == nil {
		return err
	}
	predecessorID := latestID
	if receipt.TaskID != previous.ID {
		// A rejected successor can report the original receipt without changing it.
		// Resolve that mutating task rather than authorizing its rejected successor.
		err = tx.QueryRowContext(ctx, `SELECT id FROM task_executions
   WHERE agent_id=? AND task_id=? AND kind='application.apply'
   ORDER BY attempt DESC LIMIT 1`, agentID, receipt.TaskID).Scan(&predecessorID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var originalRaw json.RawMessage
		previous, receipt, originalRaw, err = s.reviewedAbandonedPackage(ctx, tx, agentID, predecessorID)
		if err != nil || previous == nil {
			return err
		}
		latestDigest, digestErr := controlplane.PackageReceiptDigest(rawReceipt)
		originalDigest, originalErr := controlplane.PackageReceiptDigest(originalRaw)
		if digestErr != nil || originalErr != nil || latestDigest != originalDigest {
			return nil
		}
		rawReceipt = originalRaw
	}
	if previous.ID == task.ID || receipt.TaskID != previous.ID || previous.Kind != task.Kind ||
		previous.ApplicationID != task.ApplicationID || previous.AppKey != task.AppKey ||
		previous.Operation != task.Operation || previous.Manifest.Runtime == nil || task.Manifest.Runtime == nil ||
		previous.Manifest.Runtime.Kind != task.Manifest.Runtime.Kind || receipt.Runtime != task.Manifest.Runtime.Kind ||
		receipt.ApplicationID != task.ApplicationID || receipt.AppKey != task.AppKey {
		return nil
	}
	if task.Operation == "install" {
		// Keep the reviewed package and instance namespace unchanged; only its
		// declared inputs may be corrected. An earlier healthy install is not a
		// first-install recovery, even if a later attempt failed.
		if (task.Manifest.Runtime.Kind != "systemd" && task.Manifest.Runtime.Kind != "native") || task.Manifest.Runtime.Version != 2 || previous.Manifest.Runtime.Version != 2 ||
			task.SourceReceiptSHA256 != "" || previous.SourceReceiptSHA256 != "" ||
			task.ManifestSHA256 != previous.ManifestSHA256 || task.PackageRevision != previous.PackageRevision || task.Manifest.Version != previous.Manifest.Version {
			return nil
		}
		var installed bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM deployments WHERE agent_id=? AND application_id=? AND state='succeeded')`, agentID, task.ApplicationID).Scan(&installed); err != nil {
			return err
		}
		if installed {
			return nil
		}
	} else if task.Operation != "upgrade" || !semver.IsValid(canonicalAppVersion(previous.Manifest.Version)) || !semver.IsValid(canonicalAppVersion(task.Manifest.Version)) ||
		semver.Compare(canonicalAppVersion(task.Manifest.Version), canonicalAppVersion(previous.Manifest.Version)) <= 0 {
		return nil
	}
	digest, err := controlplane.PackageReceiptDigest(rawReceipt)
	if err != nil {
		return err
	}
	task.PackageRecovery = &controlplane.PackageRecovery{ExecutionID: predecessorID, TaskID: previous.ID, Attempt: previous.Attempt, ReceiptSHA256: digest}
	return nil
}

func (s *Store) reviewedAbandonedPackage(ctx context.Context, tx *sql.Tx, agentID, id string) (*AgentTask, maintenanceReceipt, json.RawMessage, error) {
	var taskID, state, phase, disposition, retired, actor, note, disposed string
	var attempt int64
	var sealedTask, sealedResult []byte
	var receipt maintenanceReceipt
	err := tx.QueryRowContext(ctx, `SELECT task_id,attempt,state,phase,disposition,identity_retired_at,
  disposition_actor,disposition_note,disposed_at,sealed_task,sealed_result FROM task_executions
  WHERE id=? AND agent_id=? AND kind='application.apply'`, id, agentID).Scan(
		&taskID, &attempt, &state, &phase, &disposition, &retired, &actor, &note, &disposed, &sealedTask, &sealedResult)
	if err != nil {
		return nil, receipt, nil, err
	}
	if (state != "failed" && state != "unknown") || phase != "reported" || disposition != "abandon" ||
		retired != "" || actor == "" || note == "" || disposed == "" || len(sealedResult) == 0 {
		return nil, receipt, nil, nil
	}
	rawTask, err := secret.Open(s.key, sealedTask, []byte("execution-task:"+id))
	if err != nil {
		return nil, receipt, nil, errors.New("center: package recovery task evidence cannot be verified")
	}
	var previous AgentTask
	if json.Unmarshal(rawTask, &previous) != nil || previous.ID != taskID || previous.Attempt != attempt || previous.Kind != "application.apply" {
		return nil, receipt, nil, errors.New("center: package recovery task evidence does not match")
	}
	rawResult, err := secret.Open(s.key, sealedResult, []byte("execution-result:"+id))
	if err != nil {
		return nil, receipt, nil, errors.New("center: package recovery result evidence cannot be verified")
	}
	var evidence executionResultEvidence
	var result ApplicationTaskResult
	if json.Unmarshal(rawResult, &evidence) != nil || evidence.Succeeded || json.Unmarshal(evidence.Result, &result) != nil {
		return nil, receipt, nil, errors.New("center: package recovery result evidence does not match")
	}
	receipt, err = decodeMaintenanceReceipt(result.Resources)
	if err != nil && previous.Operation == "install" && previous.SourceReceiptSHA256 == "" && previous.Manifest.Runtime != nil &&
		(previous.Manifest.Runtime.Kind == "systemd" || previous.Manifest.Runtime.Kind == "native") && previous.Manifest.Runtime.Version == 2 {
		// First-install completion metadata is written only after health passes.
		// Decode only that exact incomplete shape here; maintenance still requires
		// a complete healthy package receipt. The sealed source task binds its recipe.
		if len(result.Resources) <= 1<<20 && json.Unmarshal(result.Resources, &receipt) == nil && receipt.Version == 1 && len(receipt.Resources) > 0 &&
			receipt.Runtime == previous.Manifest.Runtime.Kind && receipt.PackageVersion == "" && receipt.PackageRevision == 0 && receipt.ManifestSHA256 == "" {
			err = nil
		}
	}
	if err != nil || receipt.State != "review-required" || receipt.TaskID == "" ||
		receipt.ApplicationID != previous.ApplicationID || receipt.AppKey != previous.AppKey {
		return nil, receipt, nil, nil
	}
	return &previous, receipt, result.Resources, nil
}
