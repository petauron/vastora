package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/secret"
)

type reinstallRuntimeObservation struct {
	ObservedAt time.Time              `json:"observedAt"`
	Result     meridianruntime.Result `json:"result"`
}

// Called only after authenticated replacement heartbeat or runtime completion.
// Persist evidence without projecting endpoint/route health across the fence.
func (s *Store) recordReinstallRuntimeObservation(ctx context.Context, tx *sql.Tx, target string, result meridianruntime.Result, now time.Time) error {
	var id, digest string
	err := tx.QueryRowContext(ctx, `SELECT p.runtime_command_id,p.runtime_task_sha256 FROM agent_reinstall_app_preparations p JOIN agent_reinstall_operations op ON op.id=p.operation_id JOIN application_commands c ON c.id=p.runtime_command_id WHERE op.agent_id=? AND op.state='review_required' AND c.state='succeeded'`, target).Scan(&id, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// Startup clears cached public mapping before it is observed again. That
	// invalidates recovery approval temporarily, but must not reject the
	// heartbeat needed to refresh the network observation in the first place.
	review, err := s.agentReinstallNetworkReview(ctx, tx, target)
	if err != nil {
		return err
	}
	if review == nil || !review.ApprovalCurrent {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_app_preparations SET runtime_observation=X'' WHERE runtime_command_id=?`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE application_commands SET result_json=json_set(result_json,'$.transportReady',json('false')) WHERE id=?`, id)
		return err
	}
	task, err := s.reinstallRuntimeTask(ctx, tx, target, id, false)
	if err != nil || task == nil || task.MeridianRuntime == nil {
		return errExecutionAuthorization
	}
	if result.Validate(task.MeridianRuntime.Desired) != nil {
		return errors.New("center: recovery runtime observation does not match restored configuration")
	}
	health, err := result.PeerHealth(*task.MeridianRuntime, now)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(reinstallRuntimeObservation{ObservedAt: now.UTC(), Result: result})
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, raw, []byte("reinstall-runtime-observation:"+id+":"+digest))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE agent_reinstall_app_preparations SET runtime_observation=? WHERE runtime_command_id=? AND runtime_task_sha256=?`, sealed, id, digest)
	if err != nil {
		return err
	}
	ready := true
	for _, peerReady := range health {
		ready = ready && peerReady
	}
	readyJSON, _ := json.Marshal(ready)
	// Readiness is a current projection. The sealed execution receipt remains
	// unchanged, including any startup transport failure.
	_, err = tx.ExecContext(ctx, `UPDATE application_commands SET result_json=json_set(result_json,'$.transportReady',json(?)) WHERE id=? AND state='succeeded'`, string(readyJSON), id)
	return err
}

func (s *Store) currentReinstallRuntimeObservation(ctx context.Context, tx *sql.Tx, id string, task meridianruntime.Task) (meridianruntime.Result, error) {
	var sealed []byte
	var digest string
	var empty meridianruntime.Result
	if err := tx.QueryRowContext(ctx, `SELECT runtime_observation,runtime_task_sha256 FROM agent_reinstall_app_preparations WHERE runtime_command_id=?`, id).Scan(&sealed, &digest); err != nil {
		return empty, err
	}
	raw, err := secret.Open(s.key, sealed, []byte("reinstall-runtime-observation:"+id+":"+digest))
	if err != nil {
		return empty, errExecutionAuthorization
	}
	var observation reinstallRuntimeObservation
	if json.Unmarshal(raw, &observation) != nil || observation.ObservedAt.After(s.now()) || s.now().Sub(observation.ObservedAt) > 2*time.Minute || observation.Result.Validate(task.Desired) != nil {
		return empty, errors.New("center: fresh authenticated runtime observation is required")
	}
	health, err := observation.Result.PeerHealth(task, s.now())
	if err != nil {
		return empty, err
	}
	for _, ready := range health {
		if !ready {
			return empty, errors.New("center: landing transport authorization has expired; wait for a fresh observation")
		}
	}
	return observation.Result, nil
}
