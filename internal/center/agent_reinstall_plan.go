package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
)

// This inventory is a read-only review of saved intent, not permission to replay
// it. In particular, a successful historical deployment is not evidence that a
// replacement machine has its data, private identity, or working applications.
type AgentReinstallPlan struct {
	AgentID             string                      `json:"agentId"`
	CheckedAt           time.Time                   `json:"checkedAt"`
	IdentityFingerprint string                      `json:"identityFingerprint"`
	CredentialRevoked   bool                        `json:"credentialRevoked"`
	PrivateNetwork      AgentReinstallNetwork       `json:"privateNetwork"`
	Applications        []AgentReinstallApplication `json:"applications"`
	PendingWork         []AgentReinstallPendingWork `json:"pendingWork"`
	Executions          []AgentReinstallExecution   `json:"executions"`
	Requirements        []string                    `json:"requirements"`
}

type AgentReinstallNetwork struct {
	Ownership       string `json:"ownership"`
	ServiceAddress  string `json:"serviceAddress"`
	PrivateAddress  string `json:"privateAddress"`
	ProfileRetained bool   `json:"profileRetained"`
	AddressRecovery string `json:"addressRecovery"`
	LandingRoutes   int    `json:"landingRoutes"`
	Publications    int    `json:"publications"`
}

type AgentReinstallApplication struct {
	ApplicationID string   `json:"applicationId"`
	Name          string   `json:"name"`
	AppKey        string   `json:"appKey"`
	DeploymentID  string   `json:"deploymentId"`
	Version       string   `json:"version"`
	Operation     string   `json:"operation"`
	State         string   `json:"state"`
	Recovery      string   `json:"recovery"`
	Requirements  []string `json:"requirements"`
}

type AgentReinstallPendingWork struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type AgentReinstallExecution struct {
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Phase           string `json:"phase"`
	IdentityRetired bool   `json:"identityRetired"`
}

func (s *Store) AgentReinstallPlan(ctx context.Context, agentID string) (AgentReinstallPlan, error) {
	plan := AgentReinstallPlan{AgentID: strings.TrimSpace(agentID), CheckedAt: s.now().UTC(),
		Applications: []AgentReinstallApplication{}, PendingWork: []AgentReinstallPendingWork{}, Executions: []AgentReinstallExecution{},
		Requirements: []string{"authorize_new_machine_identity", "verify_business_before_completion"}}
	// All rows come from one snapshot; concurrent configuration edits cannot
	// combine an old application's intent with a new node identity in one review.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return plan, err
	}
	defer tx.Rollback()
	var publicKey []byte
	err = tx.QueryRowContext(ctx, `SELECT a.x25519_public_key,a.credential_revoked_at<>'',a.tailscale_ownership,
		COALESCE(p.service_address,json_extract(r.profile_json,'$.serviceAddress'),''),COALESCE(p.headscale_address,json_extract(r.profile_json,'$.headscaleAddress'),''),
		p.agent_id IS NULL AND r.agent_id IS NOT NULL
		FROM agents a LEFT JOIN agent_network_profiles p ON p.agent_id=a.id
		LEFT JOIN agent_network_profile_recovery r ON r.agent_id=a.id WHERE a.id=? AND a.status IN ('active','disabled')`, plan.AgentID).
		Scan(&publicKey, &plan.CredentialRevoked, &plan.PrivateNetwork.Ownership, &plan.PrivateNetwork.ServiceAddress, &plan.PrivateNetwork.PrivateAddress, &plan.PrivateNetwork.ProfileRetained)
	if err != nil {
		return plan, err
	}
	if len(publicKey) != 0 {
		digest := sha256.Sum256(publicKey)
		plan.IdentityFingerprint = hex.EncodeToString(digest[:])
	}
	plan.PrivateNetwork.AddressRecovery = "review_network_profile"
	if plan.PrivateNetwork.PrivateAddress != "" {
		plan.PrivateNetwork.AddressRecovery = "operator_managed"
		if plan.PrivateNetwork.Ownership == "managed" {
			// Headscale 0.29.3 has no supported explicit per-node IP assignment.
			// Neither the old profile nor a reused IP proves identity replacement.
			plan.PrivateNetwork.AddressRecovery = "explicit_migration_required"
		}
		plan.Requirements = append(plan.Requirements, "withdraw_previous_private_identity", "review_address_and_dependencies")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM meridian_route_grants g JOIN meridian_endpoints e ON e.id=g.endpoint_id
		JOIN applications a ON a.id=e.application_id WHERE (a.node_id=? OR g.egress_node_id=?) AND (g.enabled=1 OR g.status='revoking')`, plan.AgentID, plan.AgentID).
		Scan(&plan.PrivateNetwork.LandingRoutes); err != nil {
		return plan, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM publications p JOIN services s ON s.id=p.service_id
		JOIN applications a ON a.id=s.application_id WHERE a.node_id=? OR p.entry_node_id=?`, plan.AgentID, plan.AgentID).Scan(&plan.PrivateNetwork.Publications); err != nil {
		return plan, err
	}
	if plan.PrivateNetwork.Publications > 0 {
		plan.Requirements = append(plan.Requirements, "rebuild_and_verify_access_entries")
	}
	if plan.PrivateNetwork.LandingRoutes > 0 {
		plan.Requirements = append(plan.Requirements, "withdraw_then_replace_landing_authorizations")
	}
	if err := readReinstallApplications(ctx, tx, &plan); err != nil {
		return plan, err
	}
	if err := readReinstallWork(ctx, tx, &plan); err != nil {
		return plan, err
	}
	return plan, tx.Commit()
}

func readReinstallApplications(ctx context.Context, tx *sql.Tx, plan *AgentReinstallPlan) error {
	// Latest intent, including a failed/pending uninstall, wins. Selecting the
	// latest successful deployment instead would resurrect an unwanted app.
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,a.app_key,COALESCE(d.id,''),COALESCE(d.app_version,''),
		COALESCE(d.operation,''),COALESCE(d.state,''),COALESCE(d.manifest_json,'{}'),COALESCE(d.reconciliation_required,0)
		FROM applications a LEFT JOIN deployments d ON d.rowid=(SELECT previous.rowid FROM deployments previous
		WHERE previous.application_id=a.id AND previous.agent_id=a.node_id ORDER BY previous.created_at DESC,previous.rowid DESC LIMIT 1)
		WHERE a.node_id=? ORDER BY a.id`, plan.AgentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		app := AgentReinstallApplication{Requirements: []string{}}
		var manifestJSON []byte
		var reconciliation bool
		if err := rows.Scan(&app.ApplicationID, &app.Name, &app.AppKey, &app.DeploymentID, &app.Version, &app.Operation, &app.State, &manifestJSON, &reconciliation); err != nil {
			return err
		}
		app.Recovery = "restore_data"
		switch {
		case app.DeploymentID == "":
			app.Recovery = "review_required"
			app.Requirements = append(app.Requirements, "saved_deployment_missing")
		case app.Operation == "uninstall":
			app.Recovery = "keep_stopped"
		default:
			var manifest catalog.AppManifest
			_, manifestID, qualified := strings.Cut(app.AppKey, "/")
			if json.Unmarshal(manifestJSON, &manifest) != nil || catalog.ValidateApp(manifest) != nil || manifest.Version != app.Version ||
				!qualified || manifest.ID != manifestID {
				app.Recovery = "review_required"
				app.Requirements = append(app.Requirements, "saved_artifact_invalid")
			} else {
				app.Requirements = append(app.Requirements, "verify_saved_artifact_and_credentials")
				policy, reconstructible := catalog.OfficialRecoveryPolicy(app.AppKey, app.Version)
				switch {
				case app.AppKey == meridianAppKey:
					app.Recovery = "rebuild_configuration"
					app.Requirements = append(app.Requirements, "validate_saved_meridian_intent_and_credentials")
				case app.AppKey == pulseAgentAppKey:
					app.Recovery = "reenroll_monitor"
					app.Requirements = append(app.Requirements, "replace_managed_monitor_enrollment")
				case reconstructible && policy.Consistency == "reconstructible":
					app.Recovery = "rebuild_configuration"
				default:
					app.Requirements = append(app.Requirements, "restore_backup_on_replacement")
				}
			}
		}
		if app.DeploymentID != "" && (app.State != "succeeded" || reconciliation) {
			app.Requirements = append(app.Requirements, "inspect_previous_operation")
		}
		plan.Applications = append(plan.Applications, app)
	}
	return rows.Err()
}

func readReinstallWork(ctx context.Context, tx *sql.Tx, plan *AgentReinstallPlan) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,kind,state,phase,identity_retired_at<>'' FROM task_executions
		WHERE agent_id=? AND disposition='' AND state<>'succeeded' ORDER BY created_at,id`, plan.AgentID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var execution AgentReinstallExecution
		if err := rows.Scan(&execution.ID, &execution.Kind, &execution.State, &execution.Phase, &execution.IdentityRetired); err != nil {
			rows.Close()
			return err
		}
		plan.Executions = append(plan.Executions, execution)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Include intents that never acquired an execution authorization. They are
	// not cleared by session retirement and must not become a recovery queue.
	rows, err = tx.QueryContext(ctx, `SELECT kind,COUNT(*) FROM (
		SELECT 'application.apply' AS kind,agent_id FROM deployments WHERE state IN ('pending','running') OR reconciliation_required=1
		UNION ALL SELECT kind,agent_id FROM application_commands WHERE state IN ('pending','running') OR reconciliation_required=1
		UNION ALL SELECT 'gateway.component.apply',gateway_node_id FROM gateway_components WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'gateway.routes.apply',gateway_node_id FROM gateway_states WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'node.listener.apply',node_id FROM node_listener_states WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'landing.server.apply',node_id FROM landing_server_states WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'landing.proxy.apply',node_id FROM landing_proxy_states WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'tunnel.state.apply',agent_id FROM cloudflare_tunnels WHERE status IN ('pending','applying','failed')
		UNION ALL SELECT 'agent.update',agent_id FROM agent_updates WHERE state IN ('pending','running','installing')
		UNION ALL SELECT 'agent.decommission',agent_id FROM agent_decommissions WHERE state IN ('pending','running','cleaning')
		UNION ALL SELECT CASE WHEN action='inspect' THEN 'xray.configuration.inspect' ELSE 'xray.configuration.apply' END,agent_id
			FROM xray_configuration_recoveries WHERE state<>'succeeded'
		UNION ALL SELECT 'node.ip-quality',agent_id FROM ip_quality_checks WHERE state IN ('pending','running')
		UNION ALL SELECT kind,agent_id FROM node_diagnostic_checks WHERE state IN ('pending','running')
	) WHERE agent_id=? GROUP BY kind ORDER BY kind`, plan.AgentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var work AgentReinstallPendingWork
		if err := rows.Scan(&work.Kind, &work.Count); err != nil {
			return err
		}
		plan.PendingWork = append(plan.PendingWork, work)
	}
	if len(plan.PendingWork)+len(plan.Executions) > 0 {
		plan.Requirements = append(plan.Requirements, "inspect_previous_effects_and_generate_fresh_plan")
	}
	return rows.Err()
}

func (s *Server) handleAgentReinstallPlan(writer http.ResponseWriter, request *http.Request) {
	plan, err := s.store.AgentReinstallPlan(request.Context(), request.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, errors.New("center: Agent not found"))
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writeJSON(writer, http.StatusOK, plan)
}
