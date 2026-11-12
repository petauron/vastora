package center

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/petauron/vastora/internal/nodesite"
	"github.com/petauron/vastora/internal/secret"
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
			site, owner, err := s.reinstallCurrentSiteTask(ctx, tx, appID, agentID)
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
	if approval.Previous == nil {
		return errors.New("center: original private service address evidence is required")
	}
	state, err := s.buildNodeListenerState(ctx, tx, agentID, revision+1, &nodeListenerRestoreTarget{ApplicationID: appID, PreviousBackendAddress: approval.Previous.ServiceAddress, BackendAddress: approval.Profile.ServiceAddress, BindAddress: approval.Profile.PublicBindAddress})
	if err != nil {
		return err
	}
	task.NodeListenerState = &state
	return nil
}

// Normal site commands require a running application. An explicit reinstall
// decision marks that original application pending, so recovery instead binds
// the retained ready certificate to the same reviewed original node and app.
// This does not relax the ordinary site-command gate or issue a new certificate.
func (s *Store) reinstallCurrentSiteTask(ctx context.Context, tx *sql.Tx, appID, agentID string) (nodesite.Task, string, error) {
	var task nodesite.Task
	var sealed []byte
	err := tx.QueryRowContext(ctx, `SELECT n.application_id,a.app_key,n.hostname,n.port,n.revision,n.certificate_pem,n.sealed_key FROM node_sites n JOIN applications a ON a.id=n.application_id WHERE n.application_id=? AND n.agent_id=? AND a.node_id=n.agent_id AND a.app_key='vastora-official/daylight' AND n.state='ready' AND n.applied_revision=n.revision`, appID, agentID).Scan(&task.ApplicationID, &task.AppKey, &task.Hostname, &task.Port, &task.Revision, &task.CertificatePEM, &sealed)
	if err != nil {
		return task, "", err
	}
	key, err := secret.Open(s.key, sealed, []byte("node-site-certificate:"+appID))
	if err != nil {
		return task, "", errors.New("center: retained original site key is unavailable")
	}
	task.Action = "apply"
	task.PrivateKeyPEM = string(key)
	return task, agentID, task.Validate()
}
