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
	"github.com/petauron/vastora/internal/networking"
)

// This inventory is a read-only review of saved intent, not permission to replay
// it. In particular, a successful historical deployment is not evidence that a
// replacement machine has its data, private identity, or working applications.
type AgentReinstallPlan struct {
	Revision             string                          `json:"revision"`
	Recovery             *AgentReinstallOperation        `json:"recovery,omitempty"`
	AgentID              string                          `json:"agentId"`
	CheckedAt            time.Time                       `json:"checkedAt"`
	IdentityFingerprint  string                          `json:"identityFingerprint"`
	CredentialRevoked    bool                            `json:"credentialRevoked"`
	NetworkReview        *AgentReinstallNetworkReview    `json:"networkReview,omitempty"`
	PrivateNetwork       AgentReinstallNetwork           `json:"privateNetwork"`
	Applications         []AgentReinstallApplication     `json:"applications"`
	PendingWork          []AgentReinstallPendingWork     `json:"pendingWork"`
	UnclaimedLocalWork   []AgentReinstallUnclaimedWork   `json:"unclaimedLocalWork"`
	Executions           []AgentReinstallExecution       `json:"executions"`
	LocalWorkDisposition *AgentReinstallLocalDisposition `json:"localWorkDisposition,omitempty"`
	Monitoring           []AgentReinstallMonitoring      `json:"monitoring"`
	Requirements         []string                        `json:"requirements"`
	Remaining            []AgentReinstallRemaining       `json:"remaining,omitempty"`
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
	ApplicationID string                     `json:"applicationId"`
	Name          string                     `json:"name"`
	AppKey        string                     `json:"appKey"`
	DeploymentID  string                     `json:"deploymentId"`
	Version       string                     `json:"version"`
	Operation     string                     `json:"operation"`
	State         string                     `json:"state"`
	Recovery      string                     `json:"recovery"`
	SharedEntry   bool                       `json:"sharedEntry"`
	Requirements  []string                   `json:"requirements"`
	Preparation   *AgentReinstallPreparation `json:"preparation,omitempty"`
}

type AgentReinstallPendingWork struct {
	AgentID string `json:"agentId"`
	Kind    string `json:"kind"`
	Count   int    `json:"count"`
}

type AgentReinstallExecution struct {
	ID              string `json:"id"`
	AgentID         string `json:"agentId"`
	TaskID          string `json:"taskId"`
	Attempt         int64  `json:"attempt"`
	Kind            string `json:"kind"`
	State           string `json:"state"`
	Phase           string `json:"phase"`
	IdentityRetired bool   `json:"identityRetired"`
	Resolution      string `json:"resolution"`
}

func (s *Store) AgentReinstallPlan(ctx context.Context, agentID string) (AgentReinstallPlan, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AgentReinstallPlan{}, err
	}
	defer tx.Rollback()
	plan, err := s.agentReinstallPlan(ctx, tx, agentID)
	if err != nil {
		return plan, err
	}
	return plan, tx.Commit()
}

func (s *Store) agentReinstallPlan(ctx context.Context, tx *sql.Tx, agentID string) (AgentReinstallPlan, error) {
	plan := AgentReinstallPlan{AgentID: strings.TrimSpace(agentID), CheckedAt: s.now().UTC(),
		Applications: []AgentReinstallApplication{}, PendingWork: []AgentReinstallPendingWork{}, Executions: []AgentReinstallExecution{}, UnclaimedLocalWork: []AgentReinstallUnclaimedWork{},
		Requirements: []string{"authorize_new_machine_identity", "verify_business_before_completion"}}
	var publicKey []byte
	err := tx.QueryRowContext(ctx, `SELECT a.x25519_public_key,a.credential_revoked_at<>'',COALESCE((SELECT json_extract(op.plan_json,'$.privateNetwork.ownership') FROM agent_reinstall_operations op WHERE op.agent_id=a.id AND op.state NOT IN ('superseded','completed')),a.tailscale_ownership),
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
	} else if plan.PrivateNetwork.Ownership != "" {
		plan.PrivateNetwork.AddressRecovery = "identity_evidence_required"
		plan.Requirements = append(plan.Requirements, "identify_previous_private_identity", "withdraw_previous_private_identity", "review_address_and_dependencies")
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
	applicationRevision, err := readReinstallApplications(ctx, tx, &plan)
	if err != nil {
		return plan, err
	}
	meridianRevision, err := reinstallMeridianEvidence(ctx, tx, agentID)
	if err != nil {
		return plan, err
	}
	monitoringRevision, err := s.readReinstallMonitoring(ctx, tx, &plan)
	if err != nil {
		return plan, err
	}
	workRevision, err := s.readReinstallWork(ctx, tx, &plan)
	if err != nil {
		return plan, err
	}
	plan.LocalWorkDisposition, err = readReinstallLocalDisposition(ctx, tx, plan.AgentID)
	if err != nil {
		return plan, err
	}
	plan.NetworkReview, err = s.agentReinstallNetworkReview(ctx, tx, plan.AgentID)
	if err != nil {
		return plan, err
	}
	// Exclude the read time and operation status from the review binding.
	review := plan
	review.CheckedAt = time.Time{}
	// Heartbeats refresh evidence time without changing the reviewed choices.
	// Freshness is represented by Ready; address, mapping and identity changes
	// still invalidate this revision.
	if plan.NetworkReview != nil {
		network := *plan.NetworkReview
		network.Candidates = append([]networking.Candidate{}, network.Candidates...)
		for i := range network.Candidates {
			network.Candidates[i].ObservedAt = time.Time{}
		}
		if network.PublicEgress != nil {
			egress := *network.PublicEgress
			egress.ObservedAt = time.Time{}
			network.PublicEgress = &egress
		}
		review.NetworkReview = &network
	}
	encoded, err := json.Marshal(struct {
		Plan                AgentReinstallPlan
		WorkRevision        string
		ApplicationRevision string
		MonitoringRevision  string
		MeridianRevision    string
	}{review, workRevision, applicationRevision, monitoringRevision, meridianRevision})
	if err != nil {
		return plan, err
	}
	digest := sha256.Sum256(encoded)
	plan.Revision = hex.EncodeToString(digest[:])
	for i := range plan.Applications {
		plan.Applications[i].Preparation, err = s.readReinstallPreparation(ctx, tx, plan.AgentID, plan.Applications[i].ApplicationID)
		if err != nil {
			return plan, err
		}
		if preparation := plan.Applications[i].Preparation; preparation != nil {
			preparation.DNS, err = s.readReinstallDNS(ctx, tx, agentID, preparation.DeploymentID, plan.Revision)
			if err != nil {
				return plan, err
			}
			preparation.EntryCheck, err = s.readReinstallEntryCheck(ctx, tx, preparation.DeploymentID, plan.Revision)
			if err != nil {
				return plan, err
			}
		}
	}
	plan.Recovery, err = readAgentReinstallOperation(ctx, tx, plan.AgentID)
	if err == nil && plan.Recovery != nil {
		plan.Remaining = reinstallRemaining(plan)
	}
	return plan, err
}

func readReinstallApplications(ctx context.Context, tx *sql.Tx, plan *AgentReinstallPlan) (string, error) {
	digest := sha256.New()
	encoder := json.NewEncoder(digest)
	// Latest intent, including a failed/pending uninstall, wins. Selecting the
	// latest successful deployment instead would resurrect an unwanted app.
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.name,a.app_key,COALESCE(d.id,''),COALESCE(d.app_version,''),
		COALESCE(d.operation,''),COALESCE(d.state,''),COALESCE(d.manifest_json,'{}'),COALESCE(d.reconciliation_required,0),
		EXISTS(SELECT 1 FROM publications p JOIN services service ON service.id=p.service_id WHERE service.application_id=a.id AND service.status<>'stopped'
 AND p.ingress_owner='application_node' AND p.entry_node_id=a.node_id AND p.kind='public_shared_443' AND p.status<>'stopped'),
		json_array(d.id,d.agent_id,d.application_id,d.app_key,d.app_version,CAST(d.manifest_json AS TEXT),CAST(d.config_json AS TEXT),
		 d.operation,d.delete_data,d.service_address,d.secret_id,hex(saved.sealed),d.registry_credential_id,d.runtime_generation,
		 registry.host,registry.username,registry.secret_id,hex(registry_secret.sealed))
		FROM applications a LEFT JOIN deployments d ON d.rowid=(SELECT previous.rowid FROM deployments previous
		WHERE previous.application_id=a.id AND previous.agent_id=a.node_id
 AND NOT EXISTS(SELECT 1 FROM agent_reinstall_app_preparations rp WHERE rp.deployment_id=previous.id) AND NOT EXISTS(SELECT 1 FROM agent_reinstall_monitor_restorations mr WHERE mr.deployment_id=previous.id) ORDER BY previous.created_at DESC,previous.rowid DESC LIMIT 1)
		LEFT JOIN secrets saved ON saved.id=d.secret_id
		LEFT JOIN registry_credentials registry ON registry.id=d.registry_credential_id
		LEFT JOIN secrets registry_secret ON registry_secret.id=registry.secret_id
		WHERE a.node_id=? ORDER BY a.id`, plan.AgentID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		app := AgentReinstallApplication{Requirements: []string{}}
		var manifestJSON []byte
		var reconciliation bool
		var intent string
		if err := rows.Scan(&app.ApplicationID, &app.Name, &app.AppKey, &app.DeploymentID, &app.Version, &app.Operation, &app.State, &manifestJSON, &reconciliation, &app.SharedEntry, &intent); err != nil {
			return "", err
		}
		// Successful deployments are restoration input too. Changes to their
		// configuration, artifact or credential must invalidate the review just
		// as changes to unfinished work do; none of this input is returned.
		if err := encoder.Encode(intent); err != nil {
			return "", err
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
	return hex.EncodeToString(digest.Sum(nil)), rows.Err()
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
