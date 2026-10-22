package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"time"

	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

// ExportGlivelaPilotPlan is for the privileged offline preparation tool on a
// verified backup. Its result contains credentials and must only be written to
// a private file, never a log or an audit event. It does not transfer ownership.
func (s *Store) ExportGlivelaPilotPlan(ctx context.Context, endpointID string) (glivela.XrayPlan, []meridianruntime.Peer, *landing.PeerIdentity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return glivela.XrayPlan{}, nil, nil, err
	}
	defer tx.Rollback()
	projection, err := s.buildMeridianRuntimeProjection(ctx, tx, endpointID, "", nil)
	if err != nil {
		return glivela.XrayPlan{}, nil, nil, err
	}
	plan, err := pilotNativePlan(ctx, tx, endpointID, projection.plan)
	return plan, projection.task.Peers, projection.task.Source, err
}

func pilotNativePlan(ctx context.Context, tx *sql.Tx, endpointID string, plan glivela.XrayPlan) (glivela.XrayPlan, error) {
	var targetIP string
	if err := tx.QueryRowContext(ctx, `SELECT target_ip FROM meridian_endpoints WHERE id=?`, endpointID).Scan(&targetIP); err != nil {
		return glivela.XrayPlan{}, err
	}
	address, err := netip.ParseAddr(targetIP)
	if err != nil || !landing.PublicIP(address) {
		return glivela.XrayPlan{}, errors.New("center: reviewed public REALITY target required")
	}
	// Preserve the retained target, just like SNI, keys, users and listener.
	// The reviewed public address remains a prerequisite, not a replacement
	// for the domain. Glivela validates the resolved destination when dialing.
	if _, err := glivela.BuildDesiredArtifact(plan); err != nil {
		return glivela.XrayPlan{}, err
	}
	return plan, nil
}

// Activation is a one-entry maintenance operation, not a new account or
// subscription owner. An authenticated operator must drain ingress, capture
// the final counters from the pinned old runtime, and stop that runtime before
// committing. No caller-provided account/credential mapping is accepted.
type GlivelaPilotActivation struct {
	LegacyConfigSHA256 string                    `json:"legacyConfigSha256"`
	ConfigSHA256       string                    `json:"configSha256"`
	CertificateID      string                    `json:"certificateId"`
	Revision           uint64                    `json:"revision"`
	BackupSHA256       string                    `json:"backupSha256"`
	SampledAt          time.Time                 `json:"sampledAt"`
	DrainedAndStopped  bool                      `json:"drainedAndStopped"`
	Counters           []glivela.CounterSnapshot `json:"counters"`
	ExpectedExits      map[string]string         `json:"expectedExits"`
}

func (s *Store) ActivateGlivelaPilot(ctx context.Context, endpointID, adminID string, input GlivelaPilotActivation) error {
	now := s.now().UTC()
	if !glivela.ValidIdentifier(endpointID) || !glivela.ValidIdentifier(adminID) ||
		!glivela.ValidIdentity(input.LegacyConfigSHA256) || !glivela.ValidIdentity(input.ConfigSHA256) ||
		!glivela.ValidIdentity(input.CertificateID) || !glivela.ValidIdentity(input.BackupSHA256) ||
		input.Revision == 0 || !input.DrainedAndStopped || input.SampledAt.IsZero() ||
		input.SampledAt.After(now) || now.Sub(input.SampledAt) > 30*time.Second {
		return errors.New("center: fresh final counters, verified backup and stopped legacy runtime required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var admin, exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM admins WHERE id=?),
		EXISTS(SELECT 1 FROM glivela_pilot_checkpoint WHERE endpoint_id=?)`, adminID, endpointID).Scan(&admin, &exists); err != nil {
		return err
	}
	if !admin || exists {
		return errors.New("center: administrator and unclaimed entry required")
	}
	projection, err := s.buildMeridianRuntimeProjection(ctx, tx, endpointID, "", nil)
	if err != nil {
		return err
	}
	if projection.task.Desired.ConfigSHA256 != input.LegacyConfigSHA256 {
		return errors.New("center: legacy configuration changed before pilot activation")
	}
	nativePlan, err := pilotNativePlan(ctx, tx, endpointID, projection.plan)
	if err != nil {
		return err
	}
	statePath := s.glivelaPilotStatePath
	if statePath == "" {
		statePath = "/run/glivela-pilot/nodes.json"
	}
	if input.Revision != nativePlan.Revision || meridianruntime.VerifyPilotDesired(statePath,
		projection.task.ApplicationID, input.CertificateID, input.ConfigSHA256, nativePlan,
		projection.task.Source, projection.task.Peers, input.ExpectedExits) != nil {
		return errors.New("center: native pilot differs from retained entry configuration")
	}
	var busy, unsupported bool
	if err := tx.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded')
		OR EXISTS(SELECT 1 FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))
		OR EXISTS(SELECT 1 FROM deployments WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))`,
		projection.agentID, projection.agentID, projection.agentID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return errors.New("center: settle entry tasks before pilot activation")
	}
	// This scoped transition keeps the existing unlimited account policy. It
	// must not silently omit quota/expiry enforcement for a different plan.
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_endpoints
		WHERE id=? AND (desired_revision<>applied_revision OR applied_revision=0))
		OR EXISTS(SELECT 1 FROM meridian_credentials c JOIN meridian_accounts a ON a.id=c.account_id
		WHERE c.endpoint_id=? AND (a.enabled<>1 OR a.status<>'active' OR a.total_bytes<>0 OR a.expiry_time<>0 OR a.reset_days<>0))`,
		endpointID, endpointID).Scan(&unsupported); err != nil {
		return err
	}
	if unsupported {
		return errors.New("center: pilot requires unchanged applied runtime and unlimited active policy")
	}
	expected := map[string]bool{}
	var retained []glivela.CounterSnapshot
	for _, material := range projection.materials {
		if !material.Credential.Enabled {
			// The exact applied plan and pinned native plan retain this disabled
			// identity. Only a settled zero raw watermark can be carried without
			// a final runtime observation. Lifetime observed/baseline data stays
			// untouched; never accept caller-invented counters for this identity.
			var up, down int64
			if err := tx.QueryRowContext(ctx, `SELECT raw_up_bytes,raw_down_bytes FROM meridian_usage_watermarks WHERE credential_id=?`, material.Credential.ID).Scan(&up, &down); err != nil {
				return err
			}
			if up != 0 || down != 0 {
				return errors.New("center: disabled credential accounting is not settled")
			}
			retained = append(retained, glivela.CounterSnapshot{CredentialID: material.Credential.ID})
			continue
		}
		expected[material.Credential.ID] = true
	}
	if len(input.Counters) != len(expected) || len(expected) == 0 {
		return errors.New("center: final counter inventory differs")
	}
	for _, counter := range input.Counters {
		if !expected[counter.CredentialID] || counter.UpBytes < 0 || counter.DownBytes < 0 {
			return errors.New("center: invalid final counter")
		}
		delete(expected, counter.CredentialID)
		var up, down int64
		if err := tx.QueryRowContext(ctx, `SELECT raw_up_bytes,raw_down_bytes FROM meridian_usage_watermarks WHERE credential_id=?`,
			counter.CredentialID).Scan(&up, &down); err != nil {
			return err
		}
		if counter.UpBytes < up || counter.DownBytes < down {
			return errors.New("center: old runtime restarted or final counters were superseded")
		}
	}
	if len(input.ExpectedExits) != len(projection.task.Peers) {
		return errors.New("center: pilot requires exact landing exit inventory")
	}
	for _, peer := range projection.task.Peers {
		address, err := netip.ParseAddr(input.ExpectedExits[peer.EgressID])
		if err != nil || !landing.PublicIP(address) || address.String() != input.ExpectedExits[peer.EgressID] {
			return errors.New("center: pilot requires verified public landing exits")
		}
	}
	if err := s.ensureMeridianSubscriptionSnapshotsForEndpointInTx(ctx, tx, endpointID); err != nil {
		return err
	}
	stamp := now.Format(time.RFC3339Nano)
	if err := s.observeMeridianUsageInTx(ctx, tx, endpointID, input.Counters, stamp); err != nil {
		return err
	}
	checkpoint := meridianruntime.PilotCounters{
		EntryID: projection.task.ApplicationID, ConfigSHA256: input.ConfigSHA256,
		CertificateID: input.CertificateID, Revision: input.Revision, CutoverAt: now,
		Totals: input.Counters, Source: projection.task.Source, Peers: projection.task.Peers, ExpectedExits: input.ExpectedExits,
	}
	checkpoint.Totals = append(append([]glivela.CounterSnapshot(nil), input.Counters...), retained...)
	var quota meridianruntime.PilotQuota
	var lastReset, nextReset string
	if err := tx.QueryRowContext(ctx, `SELECT e.total_bytes,e.used_bytes,e.reset_day,e.last_reset_at,e.next_reset_at,site.timezone
		FROM meridian_endpoints e JOIN services service ON service.id=e.service_id JOIN sites site ON site.id=service.site_id WHERE e.id=?`, endpointID).Scan(&quota.TotalBytes, &quota.UsedBytes, &quota.ResetDay, &lastReset, &nextReset, &quota.Timezone); err != nil {
		return err
	}
	if quota.TotalBytes != 0 || quota.ResetDay != 0 {
		// A blocked old projection does not contain the pre-quota enabled
		// inventory needed for later recovery. Do not infer or re-enable it.
		if quota.TotalBytes > 0 && quota.UsedBytes >= quota.TotalBytes {
			return errors.New("center: staged handover requires the retained active pre-quota plan")
		}
		quota.CutoverAt = input.SampledAt
		if lastReset != "" {
			quota.LastResetAt, err = time.Parse(time.RFC3339Nano, lastReset)
			if err != nil {
				return err
			}
		}
		if nextReset != "" {
			quota.NextResetAt, err = time.Parse(time.RFC3339Nano, nextReset)
			if err != nil {
				return err
			}
		}
		verified, err := meridianruntime.ReadPilotQuotaSeed(statePath, checkpoint.EntryID, checkpoint.CertificateID, checkpoint.ConfigSHA256, checkpoint.Revision, quota)
		if err != nil {
			return err
		}
		checkpoint.Quota = &verified
		checkpoint.CutoverAt = input.SampledAt
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO glivela_pilot_checkpoint(endpoint_id,checkpoint_json,updated_at) VALUES(?,?,?)`,
		endpointID, raw, stamp); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_endpoints SET runtime_healthy=0,last_error='Waiting for first Glivela pilot receipt',updated_at=? WHERE id=?`, stamp, endpointID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE meridian_route_grants SET runtime_healthy=0,health_expires_unix_ms=0,
		last_error='Waiting for first Glivela pilot route proof',updated_at=? WHERE endpoint_id=? AND status NOT IN ('revoking','revoked')`, stamp, endpointID); err != nil {
		return err
	}
	audit, _ := json.Marshal(map[string]any{"adminId": adminID, "backupSha256": input.BackupSHA256,
		"sampledAt": input.SampledAt, "legacyConfigSha256": input.LegacyConfigSHA256, "configSha256": input.ConfigSHA256})
	if err := s.recordTaskEvent(ctx, tx, endpointID, projection.agentID, "glivela.pilot.activate", int64(input.Revision), "succeeded", string(audit)); err != nil {
		return err
	}
	return tx.Commit()
}
