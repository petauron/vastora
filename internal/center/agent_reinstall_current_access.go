package center

import (
	"context"
	"database/sql"
	"errors"
	"math"
)

// Restore existing host access in the same immutable replacement-bound task.
// No new publication, hostname, certificate authority or grant is created.
func (s *Store) attachReinstallCurrentAccess(ctx context.Context, tx *sql.Tx, agentID, appID, key string, approval *AgentReinstallNetworkApproval, task *AgentTask) error {
	task.NodeSite, task.NodeListenerState = nil, nil
	if key == "vastora-official/daylight" {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM node_sites WHERE application_id=? AND agent_id=?)`, appID, agentID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			site, owner, err := s.nodeSiteTask(ctx, tx, appID, "apply")
			if err != nil {
				return err
			}
			if owner != agentID {
				return errExecutionAuthorization
			}
			task.NodeSite = &site
		}
	}
	if key != "vastora-official/glivela-entry" {
		return nil
	}
	var published bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM publications p JOIN services v ON v.id=p.service_id WHERE v.application_id=? AND p.entry_node_id=? AND p.ingress_owner='application_node' AND p.kind='public_shared_443' AND p.status<>'stopped' AND v.status<>'stopped')`, appID, agentID).Scan(&published); err != nil {
		return err
	}
	if !published {
		return nil
	}
	if approval == nil || !approval.Profile.DirectPublic {
		return errors.New("center: approve original public ingress before restoring its listener")
	}
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT desired_revision FROM node_listener_states WHERE node_id=?`, agentID).Scan(&revision)
	if err != nil {
		return err
	}
	if revision < 1 || revision == math.MaxInt64 {
		return errExecutionAuthorization
	}
	state, err := s.buildNodeListenerState(ctx, tx, agentID, revision+1, &nodeListenerRestoreTarget{ApplicationID: appID, BackendAddress: approval.Profile.ServiceAddress, BindAddress: approval.Profile.PublicBindAddress})
	if err != nil {
		return err
	}
	task.NodeListenerState = &state
	return nil
}
