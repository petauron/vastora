package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/meridianruntime"
)

// Reconstruct secret client inputs from current saved intent, after validating
// the restored runtime. Neither UI input nor a historic score supplies exits.
func (s *Store) reinstallAcceptanceTasks(ctx context.Context, tx *sql.Tx, target, verifier, adminID string, input AgentReinstallApplicationInput) ([]meridianruntime.AcceptanceTask, error) {
	plan, err := s.agentReinstallPlan(ctx, tx, target)
	if err != nil {
		return nil, err
	}
	op := plan.Recovery
	if op == nil || op.ID != input.OperationID || op.AuthorizedBy != adminID || op.State != "review_required" || plan.Revision != input.PlanRevision || plan.CredentialRevoked || plan.IdentityFingerprint == "" || len(plan.Executions) != 0 || len(plan.UnclaimedLocalWork) != 0 {
		return nil, errExecutionAuthorization
	}
	var eligible bool
	var verifierKey []byte
	err = tx.QueryRowContext(ctx, `SELECT status='active' AND credential_revoked_at='' AND length(x25519_public_key)>0 AND json_extract(capabilities_json,'$.docker')=1 AND json_extract(capabilities_json,'$.meridianAcceptance')=1 AND last_seen_at>=? AND last_seen_at<=?,x25519_public_key FROM agents WHERE id=?`, s.now().Add(-2*time.Minute).UTC().Format(time.RFC3339Nano), s.now().UTC().Format(time.RFC3339Nano), verifier).Scan(&eligible, &verifierKey)
	if err != nil || !eligible || verifier == target {
		return nil, errors.New("center: a separate online verifier is required")
	}
	blocked, err := agentReinstallBlocked(ctx, tx, verifier)
	if err != nil || blocked {
		return nil, errExecutionAuthorization
	}
	var preparation, endpointID string
	err = tx.QueryRowContext(ctx, `SELECT deployment_id,runtime_endpoint_id FROM agent_reinstall_app_preparations WHERE operation_id=? AND application_id=?`, op.ID, input.ApplicationID).Scan(&preparation, &endpointID)
	if err != nil {
		return nil, errExecutionAuthorization
	}
	access, err := s.readReinstallAccess(ctx, tx, target, preparation)
	if err != nil || access == nil || access.State != "applied" {
		return nil, errors.New("center: restore and activate the reviewed entry before client verification")
	}
	// Revalidates preparation, listener, runtime digest and current landing ACL.
	if _, _, err = s.reinstallAccessTarget(ctx, tx, target, preparation); err != nil {
		return nil, err
	}
	var reality meridian.RealityEndpoint
	var names, shorts []byte
	var vless, hy2 bool
	var hy2Name string
	err = tx.QueryRowContext(ctx, `SELECT advertise_host,advertise_port,server_names_json,public_key,short_ids_json,fingerprint,vless_enabled,hy2_enabled,hy2_server_name FROM meridian_endpoints WHERE id=? AND application_id=? AND status<>'retired'`, endpointID, input.ApplicationID).Scan(&reality.AdvertiseHost, &reality.AdvertisePort, &names, &reality.PublicKey, &shorts, &reality.Fingerprint, &vless, &hy2, &hy2Name)
	if err != nil || json.Unmarshal(names, &reality.ServerNames) != nil || json.Unmarshal(shorts, &reality.ShortIDs) != nil {
		return nil, errExecutionAuthorization
	}
	reality.ID, reality.EntryID = endpointID, input.ApplicationID
	materials, _, err := s.meridianRuntimeMaterials(ctx, tx, endpointID, input.ApplicationID)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(verifierKey)
	tasks := []meridianruntime.AcceptanceTask{}
	for _, material := range materials {
		if !material.Credential.Enabled {
			continue
		}
		routed := material.Credential.Kind == meridian.RouteCredential
		exitNode := target
		if routed {
			var configured bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM meridian_route_grants WHERE endpoint_id=? AND route_credential_id=? AND enabled=1 AND status<>'revoked')`, endpointID, material.Credential.ID).Scan(&configured); err != nil {
				return nil, err
			}
			if !configured {
				continue
			}
			exitNode = material.Credential.EgressID
		}
		exit, err := s.reinstallAcceptanceExit(ctx, tx, exitNode, routed)
		if err != nil {
			return nil, err
		}
		base := meridianruntime.AcceptanceTask{VerifierFingerprint: hex.EncodeToString(fingerprint[:]), OperationID: op.ID, PlanRevision: plan.Revision, TargetAgentID: target, VerifierAgentID: verifier, IdentityFingerprint: plan.IdentityFingerprint, ExpectedExit: exit}
		if vless {
			base.Client = meridianruntime.AcceptanceClient{Protocol: meridian.VLESSReality, Reality: &reality, Material: material}
			if err = base.Validate(); err != nil {
				return nil, err
			}
			tasks = append(tasks, base)
		}
		// Routed HY2 is not a supported subscription in the Meridian domain model.
		if hy2 && material.Credential.Kind == meridian.NativeCredential {
			endpoint := meridian.HysteriaEndpoint{ID: endpointID + "-hy2", EntryID: input.ApplicationID, AdvertiseHost: reality.AdvertiseHost, AdvertisePort: 443, ServerName: hy2Name}
			base.Client = meridianruntime.AcceptanceClient{Protocol: meridian.Hysteria2, Hysteria: &endpoint, Material: material}
			if err = base.Validate(); err != nil {
				return nil, err
			}
			tasks = append(tasks, base)
		}
	}
	if len(tasks) == 0 {
		return nil, errors.New("center: no enabled original client credential is available for acceptance")
	}
	return tasks, nil
}

func (s *Store) reinstallAcceptanceExit(ctx context.Context, tx *sql.Tx, node string, routed bool) (string, error) {
	evidence, err := agentPublicEgress(ctx, tx, node)
	if err != nil || evidence == nil || evidence.ObservedAt.After(s.now()) || s.now().Sub(evidence.ObservedAt) > 10*time.Minute {
		return "", errors.New("center: fresh exit evidence is required")
	}
	if !routed {
		return evidence.Address, nil
	}
	// Resolve selected bind/NAT mapping using the same authenticated inventory
	// as IP-quality targets. An unknown selected exit must not become native.
	targets, err := listIPQualityTargets(ctx, tx, node)
	if err != nil {
		return "", err
	}
	for _, candidate := range targets {
		if candidate.Selected {
			return candidate.Address, nil
		}
	}
	return "", errors.New("center: selected landing exit is unconfirmed")
}
