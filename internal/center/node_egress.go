package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/meridianruntime"
)

type NodeEgressInput struct {
	Policy   meridian.EgressPolicy `json:"policy"`
	Revision int64                 `json:"revision"`
}

type NodeEgressView struct {
	Policy        meridian.EgressPolicy              `json:"policy"`
	AppliedPolicy meridian.EgressPolicy              `json:"appliedPolicy"`
	Revision      int64                              `json:"revision"`
	State         string                             `json:"state"`
	Error         string                             `json:"error,omitempty"`
	Available     bool                               `json:"available"`
	CommandID     string                             `json:"commandId,omitempty"`
	Verified      *meridianruntime.EgressObservation `json:"verified,omitempty"`
}

func (s *Store) NodeEgress(ctx context.Context, nodeID string) (NodeEgressView, error) {
	var v NodeEgressView
	var raw []byte
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(p.policy,'auto'),COALESCE(p.applied_policy,'auto'),COALESCE(p.revision,0),COALESCE(p.verified_json,'{}'),
 a.status='active' AND a.credential_revoked_at='' AND COALESCE(json_extract(a.capabilities_json,'$.nativeEgress'),0)=1 AND a.last_seen_at>=?
 FROM agents a LEFT JOIN node_egress_policies p ON p.node_id=a.id WHERE a.id=?`, s.now().Add(-2*time.Minute).UTC().Format(time.RFC3339Nano), nodeID).Scan(&v.Policy, &v.AppliedPolicy, &v.Revision, &raw, &v.Available)
	if err != nil {
		return v, err
	}
	if string(raw) != "{}" {
		if err := json.Unmarshal(raw, &v.Verified); err != nil {
			return v, err
		}
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM meridian_endpoints e JOIN applications a ON a.id=e.application_id WHERE a.node_id=? AND a.app_key=? AND a.status='running' AND e.status<>'retired'`, nodeID, meridianAppKey).Scan(&count); err != nil {
		return v, err
	}
	if count != 1 {
		v.Available = false
		v.State = "unavailable"
		v.Error = "此节点需要一个已配置的 Meridian 原生入口。远程落地出口不受此策略控制。"
		return v, nil
	}
	err = s.db.QueryRowContext(ctx, `SELECT e.status,e.last_error,COALESCE(d.command_id,'') FROM meridian_endpoints e JOIN applications a ON a.id=e.application_id LEFT JOIN meridian_deployments d ON d.endpoint_id=e.id WHERE a.node_id=? AND a.app_key=? AND a.status='running' AND e.status<>'retired'`, nodeID, meridianAppKey).Scan(&v.State, &v.Error, &v.CommandID)
	if err != nil {
		return v, err
	}
	if !v.Available {
		v.Error = "请更新并连接此节点的 Agent 后再配置出口策略。"
	}
	return v, nil
}

func (s *Store) SetNodeEgress(ctx context.Context, nodeID string, input NodeEgressInput) (string, error) {
	if input.Policy == "" || input.Policy.Validate() != nil || input.Revision < 0 {
		return "", errors.New("center: unsupported native egress policy")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = ensureMeridianManagementWritable(ctx, tx); err != nil {
		return "", err
	}
	if blocked, err := agentReinstallBlocked(ctx, tx, nodeID); err != nil || blocked {
		return "", errExecutionBlocked
	}
	if paused, err := executionClaimsPaused(ctx, tx); err != nil || paused {
		return "", errExecutionBlocked
	}
	var available bool
	err = tx.QueryRowContext(ctx, `SELECT status='active' AND credential_revoked_at='' AND COALESCE(json_extract(capabilities_json,'$.nativeEgress'),0)=1 AND last_seen_at>=? FROM agents WHERE id=?`, s.now().Add(-2*time.Minute).UTC().Format(time.RFC3339Nano), nodeID).Scan(&available)
	if err != nil || !available {
		return "", errors.New("center: update and reconnect the node Agent before configuring egress")
	}
	var blocked bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_executions WHERE agent_id=? AND disposition='' AND state<>'succeeded') OR EXISTS(SELECT 1 FROM application_commands WHERE agent_id=? AND (state IN ('pending','running') OR reconciliation_required=1))`, nodeID, nodeID).Scan(&blocked)
	if err != nil {
		return "", err
	}
	if blocked {
		return "", errExecutionBlocked
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT revision FROM node_egress_policies WHERE node_id=?),0)`, nodeID).Scan(&revision); err != nil {
		return "", err
	}
	if revision != input.Revision {
		return "", errors.New("center: egress policy changed; refresh and retry")
	}
	var endpoint string
	var count int
	err = tx.QueryRowContext(ctx, `SELECT count(*),COALESCE(min(e.id),'') FROM meridian_endpoints e JOIN applications a ON a.id=e.application_id WHERE a.node_id=? AND a.app_key=? AND a.status='running' AND e.status<>'retired'`, nodeID, meridianAppKey).Scan(&count, &endpoint)
	if err != nil {
		return "", err
	}
	if count != 1 {
		return "", errors.New("center: exactly one managed native endpoint is required; remote landing policy is not supported here")
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO node_egress_policies(node_id,policy,revision,updated_at) VALUES(?,?,1,?) ON CONFLICT(node_id) DO UPDATE SET policy=excluded.policy,revision=revision+1,updated_at=excluded.updated_at`, nodeID, input.Policy, now)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE meridian_endpoints SET desired_revision=desired_revision+1,status='pending',runtime_healthy=0,last_error='',updated_at=? WHERE id=?`, now, endpoint); err != nil {
		return "", err
	}
	id, err := s.queueMeridianRuntime(ctx, tx, endpoint, false)
	if err != nil {
		return "", err
	}
	if err = s.recordTaskEvent(ctx, tx, id, nodeID, "node.egress", revision+1, "queued", "Native egress policy: "+string(input.Policy)); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func applyNativeEgressProjection(ctx context.Context, tx *sql.Tx, p *meridianRuntimeProjection, endpoint meridian.RealityEndpoint, hy2 meridian.HysteriaEndpoint, vlessEnabled, hy2Enabled int, materials []meridian.CredentialMaterial) error {
	var policy meridian.EgressPolicy
	var needsVerification bool
	err := tx.QueryRowContext(ctx, `SELECT policy,revision<>verified_revision FROM node_egress_policies WHERE node_id=?`, p.agentID).Scan(&policy, &needsVerification)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	p.task.NativeEgress = policy
	p.task.ImageReference = meridianruntime.EgressImage
	if !needsVerification {
		return nil
	}
	var material *meridian.CredentialMaterial
	for i := range materials {
		if materials[i].Credential.Kind == meridian.NativeCredential && materials[i].Credential.Enabled {
			material = &materials[i]
			break
		}
	}
	if material == nil {
		return errors.New("center: an enabled native client is required to verify the egress policy")
	}
	// Verification reaches the real configured private backend with its original
	// REALITY identity, avoiding a provider's public-address hairpin requirement.
	if vlessEnabled == 1 {
		endpoint.PrivateKey = ""
		endpoint.AdvertiseHost, endpoint.AdvertisePort = endpoint.ListenAddress, endpoint.ListenPort
		p.task.EgressClients = append(p.task.EgressClients, meridianruntime.AcceptanceClient{Protocol: meridian.VLESSReality, Reality: &endpoint, Material: *material})
	}
	if hy2Enabled == 1 {
		hy2.PrivateKeyPEM = ""
		hy2.CertificatePEM = ""
		hy2.AdvertiseHost = endpoint.ListenAddress
		p.task.EgressClients = append(p.task.EgressClients, meridianruntime.AcceptanceClient{Protocol: meridian.Hysteria2, Hysteria: &hy2, Material: *material})
	}
	return nil
}

func (s *Server) handleNodeEgress(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut {
		var input NodeEgressInput
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if _, err := s.store.SetNodeEgress(r.Context(), r.PathValue("id"), input); err != nil {
			writeError(w, http.StatusConflict, err)
			return
		}
	}
	view, err := s.store.NodeEgress(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
