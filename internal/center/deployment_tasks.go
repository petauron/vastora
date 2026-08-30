package center

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
)

type AgentTask struct {
	Kind                      string                         `json:"kind"`
	ID                        string                         `json:"id"`
	Attempt                   int64                          `json:"attempt"`
	AppKey                    string                         `json:"appKey"`
	Manifest                  catalog.AppManifest            `json:"manifest"`
	Config                    json.RawMessage                `json:"config"`
	Secrets                   json.RawMessage                `json:"secrets"`
	Operation                 string                         `json:"operation"`
	DeleteData                bool                           `json:"deleteData"`
	Revision                  int64                          `json:"revision,omitempty"`
	ApplicationID             string                         `json:"applicationId,omitempty"`
	ApplicationRole           string                         `json:"applicationRole,omitempty"`
	ServiceAddress            string                         `json:"serviceAddress,omitempty"`
	GatewayState              *gateway.DesiredState          `json:"gatewayState,omitempty"`
	GatewayCertificates       []gateway.Certificate          `json:"gatewayCertificates,omitempty"`
	TunnelState               *TunnelTaskState               `json:"tunnelState,omitempty"`
	ApplicationCommand        *RealityCommandTask            `json:"applicationCommand,omitempty"`
	SubscriptionCommand       *SubscriptionCommandTask       `json:"subscriptionCommand,omitempty"`
	ClientCommand             *ThreeXUIClientCommandTask     `json:"clientCommand,omitempty"`
	NodeCommand               *ThreeXUINodeCommandTask       `json:"nodeCommand,omitempty"`
	ControllerCommand         *ThreeXUIControllerCommandTask `json:"controllerCommand,omitempty"`
	RegistryCredential        *AgentRegistryCredential       `json:"registryCredential,omitempty"`
	Reconcile                 bool                           `json:"reconcile,omitempty"`
	RequiredRuntimeGeneration int                            `json:"requiredRuntimeGeneration,omitempty"`
}

type AgentRegistryCredential struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type TunnelTaskIngress struct {
	Hostname string `json:"hostname"`
	Service  string `json:"service"`
}

type TunnelTaskState struct {
	Revision int64               `json:"revision"`
	Status   string              `json:"status"`
	Image    string              `json:"image"`
	Token    string              `json:"token"`
	Ingress  []TunnelTaskIngress `json:"ingress"`
}

func (s *Store) ClaimNextTask(ctx context.Context, agentID, credential string, requiredTaskIDs ...string) (*AgentTask, error) {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return nil, err
	}
	s.domainSwitchMu.Lock()
	_, aliasErr := s.beginDueSystemEndpointAliasRetirements(ctx)
	s.domainSwitchMu.Unlock()
	if aliasErr != nil {
		return nil, aliasErr
	}
	var publicKey []byte
	var agentRuntimeGeneration int
	if err := s.db.QueryRowContext(ctx, `SELECT x25519_public_key, runtime_generation FROM agents WHERE id = ?`, agentID).Scan(&publicKey, &agentRuntimeGeneration); err != nil || controlplane.ValidatePublicKey(publicKey) != nil {
		return nil, errors.New("center: Agent must heartbeat before claiming encrypted tasks")
	}
	if err := s.recoverExpiredTasks(ctx, agentID); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("center: begin task claim: %w", err)
	}
	defer tx.Rollback()
	var task AgentTask
	var manifest []byte
	var secretID sql.NullString
	var registryCredentialID sql.NullString
	var attempt int64
	var reconciliationRequested, requiredRuntimeGeneration int
	requiredTaskID := ""
	if len(requiredTaskIDs) != 0 {
		requiredTaskID = strings.TrimSpace(requiredTaskIDs[0])
	}
	query := `SELECT d.id, d.app_key, d.manifest_json, d.config_json, d.secret_id, d.registry_credential_id, d.operation, d.delete_data, d.application_id, a.role, d.service_address, d.attempt, d.reconciliation_requested, d.runtime_generation
		FROM deployments d JOIN applications a ON a.id = d.application_id WHERE d.agent_id = ? AND d.state = 'pending'`
	queryArgs := []any{agentID}
	if requiredTaskID != "" {
		query += ` AND d.id = ?`
		queryArgs = append(queryArgs, requiredTaskID)
	}
	query += ` ORDER BY d.created_at, d.rowid LIMIT 1`
	err = tx.QueryRowContext(ctx, query, queryArgs...).Scan(&task.ID, &task.AppKey, &manifest, &task.Config, &secretID, &registryCredentialID, &task.Operation, &task.DeleteData, &task.ApplicationID, &task.ApplicationRole, &task.ServiceAddress, &attempt, &reconciliationRequested, &requiredRuntimeGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		if requiredTaskID != "" {
			return nil, nil
		}
		commandTask, commandErr := s.claimApplicationCommand(ctx, tx, agentID)
		if errors.Is(commandErr, errApplicationCommandDiscarded) {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if commandErr != nil {
			return nil, commandErr
		}
		if commandTask != nil {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return commandTask, nil
		}
		var desiredStatus string
		var generation int64
		err = tx.QueryRowContext(ctx, `SELECT desired_status, generation, attempt FROM gateway_components
			WHERE gateway_node_id = ? AND generation > applied_generation AND status IN ('pending', 'failed')`, agentID).Scan(&desiredStatus, &generation, &attempt)
		if err == nil {
			task = AgentTask{Kind: "gateway.component.apply", ID: gatewayComponentTaskID(agentID, generation), Attempt: attempt + 1, Operation: desiredStatus, Revision: generation}
			now := s.now().UTC()
			claimed, err := tx.ExecContext(ctx, `UPDATE gateway_components SET status = 'applying', attempt = attempt + 1, lease_expires_at = ?, updated_at = ? WHERE gateway_node_id = ? AND generation = ? AND attempt = ? AND status IN ('pending', 'failed')`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), agentID, generation, attempt)
			if err != nil {
				return nil, fmt.Errorf("center: claim gateway component task: %w", err)
			}
			if changed, _ := claimed.RowsAffected(); changed != 1 {
				return nil, errors.New("center: gateway component task changed while claiming")
			}
			if err := s.recordTaskEvent(ctx, tx, task.ID, agentID, task.Kind, generation, "claimed", fmt.Sprintf("attempt %d", attempt+1)); err != nil {
				return nil, err
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return &task, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("center: read gateway component state: %w", err)
		}
		var desiredJSON []byte
		var revision int64
		err = tx.QueryRowContext(ctx, `SELECT s.desired_revision, s.desired_json, s.attempt FROM gateway_states s
			JOIN gateway_components c ON c.gateway_node_id = s.gateway_node_id
			WHERE s.gateway_node_id = ? AND c.desired_status = 'running' AND c.status = 'ready'
			AND s.desired_revision > s.applied_revision AND s.status IN ('pending', 'failed')`, agentID).Scan(&revision, &desiredJSON, &attempt)
		if errors.Is(err, sql.ErrNoRows) {
			tunnelTask, tunnelErr := s.claimTunnelTask(ctx, tx, agentID)
			if tunnelErr != nil {
				return nil, fmt.Errorf("center: read Tunnel desired state: %w", tunnelErr)
			}
			if tunnelTask == nil {
				decommissionTask, decommissionErr := s.claimAgentDecommission(ctx, tx, agentID)
				if decommissionErr != nil {
					return nil, decommissionErr
				}
				if decommissionTask == nil {
					return nil, nil
				}
				if err := tx.Commit(); err != nil {
					return nil, err
				}
				return decommissionTask, nil
			}
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return tunnelTask, nil
		}
		if err != nil {
			return nil, fmt.Errorf("center: read gateway desired state: %w", err)
		}
		var state gateway.DesiredState
		if json.Unmarshal(desiredJSON, &state) != nil || state.Validate() != nil || state.Revision != revision {
			return nil, errors.New("center: invalid stored gateway desired state")
		}
		certificates, err := s.gatewayCertificates(ctx, tx, agentID, state)
		if err != nil {
			return nil, err
		}
		task = AgentTask{Kind: "gateway.routes.apply", ID: gatewayRouteTaskID(agentID, revision), Attempt: attempt + 1, Revision: revision, GatewayState: &state, GatewayCertificates: certificates}
		now := s.now().UTC()
		claimed, err := tx.ExecContext(ctx, `UPDATE gateway_states SET status = 'applying', attempt = attempt + 1, lease_expires_at = ?, updated_at = ? WHERE gateway_node_id = ? AND desired_revision = ? AND attempt = ? AND status IN ('pending', 'failed')`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), agentID, revision, attempt)
		if err != nil {
			return nil, fmt.Errorf("center: claim gateway desired state: %w", err)
		}
		if changed, _ := claimed.RowsAffected(); changed != 1 {
			return nil, errors.New("center: gateway desired state changed while claiming")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE routes SET status = 'applying', updated_at = ? WHERE gateway_node_id = ? AND desired_revision = ?`, s.now().UTC().Format(time.RFC3339Nano), agentID, revision); err != nil {
			return nil, err
		}
		if err := s.recordTaskEvent(ctx, tx, task.ID, agentID, task.Kind, revision, "claimed", fmt.Sprintf("attempt %d", attempt+1)); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return &task, nil
	}
	if err != nil {
		return nil, fmt.Errorf("center: read pending task: %w", err)
	}
	if err := json.Unmarshal(manifest, &task.Manifest); err != nil {
		return nil, fmt.Errorf("center: decode pending task: %w", err)
	}
	task.Kind = "application.apply"
	task.Attempt = attempt + 1
	task.Revision = applicationTaskRevision
	task.Reconcile = reconciliationRequested == 1
	task.RequiredRuntimeGeneration = requiredRuntimeGeneration
	if agentRuntimeGeneration < requiredRuntimeGeneration {
		return nil, nil
	}
	if task.ServiceAddress != "" && net.ParseIP(task.ServiceAddress) == nil {
		return nil, errors.New("center: deployment target has an invalid private service address")
	}
	if secretID.Valid {
		var sealed []byte
		if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, secretID.String).Scan(&sealed); err != nil {
			return nil, fmt.Errorf("center: read deployment secrets: %w", err)
		}
		secretValue, err := secret.Open(s.key, sealed, []byte("deployment:"+task.ID))
		if err != nil {
			return nil, fmt.Errorf("center: decrypt deployment secrets: %w", err)
		}
		task.Secrets = secretValue
	} else {
		task.Secrets = json.RawMessage(`{}`)
	}
	if registryCredentialID.Valid {
		var sealed []byte
		var credential AgentRegistryCredential
		if err := tx.QueryRowContext(ctx, `SELECT r.host, r.username, s.sealed FROM registry_credentials r JOIN secrets s ON s.id = r.secret_id WHERE r.id = ?`, registryCredentialID.String).Scan(&credential.Host, &credential.Username, &sealed); err != nil {
			return nil, fmt.Errorf("center: read deployment Registry credential: %w", err)
		}
		password, err := secret.Open(s.key, sealed, []byte("registry-credential:"+registryCredentialID.String))
		if err != nil {
			return nil, fmt.Errorf("center: decrypt deployment Registry credential: %w", err)
		}
		credential.Password = string(password)
		task.RegistryCredential = &credential
	}
	now := s.now().UTC()
	claimed, err := tx.ExecContext(ctx, `UPDATE deployments SET state = 'running', attempt = attempt + 1, lease_expires_at = ?, updated_at = ? WHERE id = ? AND state = 'pending' AND attempt = ?`, now.Add(taskLeaseDuration).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), task.ID, attempt)
	if err != nil {
		return nil, fmt.Errorf("center: claim task: %w", err)
	}
	if changed, _ := claimed.RowsAffected(); changed != 1 {
		return nil, errors.New("center: application task changed while claiming")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'deploying', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), task.ApplicationID); err != nil {
		return nil, fmt.Errorf("center: mark application deploying: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, task.ID, agentID, task.Kind, task.Revision, "claimed", fmt.Sprintf("attempt %d", task.Attempt)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("center: commit task claim: %w", err)
	}
	return &task, nil
}

func (s *Store) WaitAndClaimNextTask(ctx context.Context, agentID, credential string, wait time.Duration, requiredTaskIDs ...string) (*AgentTask, error) {
	if wait <= 0 {
		return s.ClaimNextTask(ctx, agentID, credential, requiredTaskIDs...)
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		key := "agent:" + agentID
		changed := s.taskChanges.subscribe(key)
		task, err := s.ClaimNextTask(ctx, agentID, credential, requiredTaskIDs...)
		if err != nil || task != nil {
			s.taskChanges.unsubscribe(key, changed)
			return task, err
		}
		select {
		case <-ctx.Done():
			s.taskChanges.unsubscribe(key, changed)
			return nil, ctx.Err()
		case <-deadline.C:
			s.taskChanges.unsubscribe(key, changed)
			return nil, nil
		case <-changed:
		}
	}
}

func (s *Store) CompleteTask(ctx context.Context, agentID, credential, taskID string, expectedAttempt int64, succeeded bool, taskError string, rawResult json.RawMessage) error {
	return s.completeTaskWithDisposition(ctx, agentID, credential, taskID, expectedAttempt, succeeded, taskError, rawResult, false)
}

var errInvalidReconciliationDisposition = errors.New("center: invalid task reconciliation disposition")

func (s *Store) completeTaskWithDisposition(ctx context.Context, agentID, credential, taskID string, expectedAttempt int64, succeeded bool, taskError string, rawResult json.RawMessage, reconciliationRequired bool, executedRuntimeGenerations ...int) error {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return err
	}
	taskError = controlplane.SafeError(taskError)
	if reconciliationRequired && (succeeded || strings.TrimSpace(taskError) == "") {
		return errInvalidReconciliationDisposition
	}
	if strings.HasPrefix(taskID, "application-command-") {
		return s.completeApplicationCommand(ctx, agentID, taskID, expectedAttempt, succeeded, taskError, rawResult, reconciliationRequired)
	}
	if taskID == agentDecommissionTaskID(agentID) {
		if reconciliationRequired {
			return errInvalidReconciliationDisposition
		}
		return s.completeAgentDecommission(ctx, agentID, expectedAttempt, succeeded, taskError)
	}
	if revision, gatewayTask := gatewayTaskRevision(taskID); gatewayTask {
		if reconciliationRequired {
			return errInvalidReconciliationDisposition
		}
		return s.CompleteGatewayState(ctx, agentID, credential, revision, expectedAttempt, succeeded, taskError)
	}
	if revision, tunnelTask := tunnelTaskRevision(taskID); tunnelTask {
		if reconciliationRequired {
			return errInvalidReconciliationDisposition
		}
		return s.completeTunnelState(ctx, agentID, revision, expectedAttempt, succeeded, taskError)
	}
	if generation, componentTask := gatewayComponentTaskGeneration(taskID); componentTask {
		if reconciliationRequired {
			return errInvalidReconciliationDisposition
		}
		return s.completeGatewayComponent(ctx, agentID, generation, expectedAttempt, succeeded, taskError)
	}
	state := "succeeded"
	if !succeeded {
		state = "failed"
	}
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var applicationID, appKey, role, operation, currentState string
	var attempt int64
	var currentReconciliationRequired, requiredRuntimeGeneration, agentRuntimeGeneration int
	var manifestJSON, configJSON []byte
	var serviceAddress string
	if err := tx.QueryRowContext(ctx, `SELECT d.application_id, a.app_key, a.role, d.operation, d.state, d.reconciliation_required, d.manifest_json, d.config_json, d.service_address, d.attempt, d.runtime_generation, agent.runtime_generation
		FROM deployments d JOIN applications a ON a.id = d.application_id JOIN agents agent ON agent.id = d.agent_id WHERE d.id = ? AND d.agent_id = ?`, taskID, agentID).Scan(&applicationID, &appKey, &role, &operation, &currentState, &currentReconciliationRequired, &manifestJSON, &configJSON, &serviceAddress, &attempt, &requiredRuntimeGeneration, &agentRuntimeGeneration); err != nil {
		return errors.New("center: task not found")
	}
	if reconciliationRequired && appKey != threeXUIAppKey {
		return errInvalidReconciliationDisposition
	}
	if currentState == "succeeded" || currentState == "failed" {
		if reconciliationRequired && (currentState != "failed" || currentReconciliationRequired != 1) {
			return errInvalidReconciliationDisposition
		}
		return nil
	}
	if currentState != "running" {
		return errors.New("center: task is not active")
	}
	if expectedAttempt <= 0 || expectedAttempt != attempt {
		return errors.New("center: stale task result")
	}
	executedRuntimeGeneration := agentRuntimeGeneration
	if len(executedRuntimeGenerations) != 0 {
		executedRuntimeGeneration = executedRuntimeGenerations[0]
	}
	if executedRuntimeGeneration < requiredRuntimeGeneration || agentRuntimeGeneration < executedRuntimeGeneration {
		return errors.New("center: Agent task result does not prove the required application runtime generation")
	}
	now := s.now().UTC()
	publicationCleanups := []publicationCleanup{}
	var taskResult ApplicationTaskResult
	if succeeded || reconciliationRequired {
		if len(rawResult) != 0 && string(rawResult) != "null" && json.Unmarshal(rawResult, &taskResult) != nil {
			return errors.New("center: invalid Agent task result")
		}
	}
	if reconciliationRequired {
		if err := validateReconciliationGeneratedSecrets(taskResult.GeneratedSecrets); err != nil {
			return err
		}
		if operation != "uninstall" {
			if err := s.storeApplicationSecrets(ctx, tx, taskID, applicationID, taskResult.GeneratedSecrets, now); err != nil {
				return err
			}
		}
	}
	if succeeded {
		if operation != "uninstall" {
			var manifest catalog.AppManifest
			if json.Unmarshal(manifestJSON, &manifest) != nil || catalog.ValidateApp(manifest) != nil {
				return errors.New("center: stored deployment manifest is invalid")
			}
			if err := validateApplicationResult(manifest, appKey, role, configJSON, serviceAddress, taskResult); err != nil {
				state = "failed"
				taskError = err.Error()
				succeeded = false
			}
		}
		if succeeded {
			if err := s.completeApplication(ctx, tx, taskID, applicationID, operation, executedRuntimeGeneration, taskResult, now, &publicationCleanups); err != nil {
				state = "failed"
				taskError = err.Error()
				succeeded = false
			}
			if succeeded && operation != "uninstall" {
				if err := s.storeApplicationSecrets(ctx, tx, taskID, applicationID, taskResult.GeneratedSecrets, now); err != nil {
					state = "failed"
					taskError = err.Error()
					succeeded = false
				}
			}
			if succeeded {
				if operation == "uninstall" {
					err = s.queueThreeXUINodeRemoval(ctx, tx, applicationID, now)
				} else {
					err = s.queueThreeXUINodeReconcile(ctx, tx, taskID, applicationID, "", now)
				}
				if err != nil && operation != "uninstall" {
					if role == threeXUIRoleWorker {
						if _, syncErr := tx.ExecContext(ctx, `UPDATE three_x_ui_nodes SET status = 'failed', last_error = ?, updated_at = ? WHERE worker_application_id = ?`, err.Error(), now.Format(time.RFC3339Nano), applicationID); syncErr != nil {
							return syncErr
						}
						err = nil
					}
				}
				if err != nil {
					state = "failed"
					taskError = err.Error()
					succeeded = false
				}
			}
		}
	}
	if !succeeded {
		if taskError == "" {
			taskError = "application task failed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE applications SET status = 'failed', updated_at = ? WHERE id = ?`, now.Format(time.RFC3339Nano), applicationID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE services SET status = 'degraded', last_error = ?, updated_at = ? WHERE application_id = ? AND status <> 'stopped'`, taskError, now.Format(time.RFC3339Nano), applicationID); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE deployments SET state = ?, reconciliation_required = ?, reconciliation_requested = 0, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND agent_id = ? AND state = 'running'`, state, reconciliationRequired, taskError, now.Format(time.RFC3339Nano), taskID, agentID)
	if err != nil {
		return fmt.Errorf("center: complete task: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("center: complete task: %w", err)
	}
	if changed != 1 {
		return errors.New("center: task is not active")
	}
	if err := s.recordTaskEvent(ctx, tx, taskID, agentID, "application.apply", applicationTaskRevision, state, taskError); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := s.cleanupStoppedPublications(ctx, publicationCleanups); err != nil {
		return fmt.Errorf("center: record publication cleanup state: %w", err)
	}
	return nil
}

func validateReconciliationGeneratedSecrets(generated map[string]string) error {
	for key, value := range generated {
		if key != "api_token" || strings.TrimSpace(value) == "" || len(value) > 4096 {
			return errors.New("center: invalid Agent reconciliation secrets")
		}
	}
	return nil
}

func gatewayComponentTaskGeneration(taskID string) (int64, bool) {
	marker := strings.LastIndex(taskID, "-g")
	if !strings.HasPrefix(taskID, "gateway-component-") || marker < len("gateway-component-") {
		return 0, false
	}
	generation, err := strconv.ParseInt(taskID[marker+2:], 10, 64)
	return generation, err == nil && generation > 0
}

func gatewayComponentTaskID(agentID string, generation int64) string {
	return fmt.Sprintf("gateway-component-%s-g%d", agentID, generation)
}

func (s *Store) completeGatewayComponent(ctx context.Context, agentID string, generation, expectedAttempt int64, succeeded bool, taskError string) error {
	taskError = strings.TrimSpace(taskError)
	if len(taskError) > 1024 {
		taskError = taskError[:1024]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var desiredStatus, currentStatus string
	var desiredGeneration, appliedGeneration, attempt int64
	if err := tx.QueryRowContext(ctx, `SELECT desired_status, generation, applied_generation, status, attempt FROM gateway_components WHERE gateway_node_id = ?`, agentID).Scan(&desiredStatus, &desiredGeneration, &appliedGeneration, &currentStatus, &attempt); err != nil {
		return errors.New("center: gateway component task not found")
	}
	if generation < desiredGeneration || generation <= appliedGeneration {
		return nil
	}
	if generation != desiredGeneration || currentStatus != "applying" {
		return errors.New("center: gateway component task is not active")
	}
	if expectedAttempt <= 0 || expectedAttempt != attempt {
		return errors.New("center: stale task result")
	}
	status := "ready"
	if desiredStatus == "stopped" {
		status = "stopped"
	}
	if !succeeded {
		status = "failed"
	}
	if _, err := tx.ExecContext(ctx, `UPDATE gateway_components SET applied_generation = CASE WHEN ? THEN ? ELSE applied_generation END, status = ?, lease_expires_at = '', last_error = ?, updated_at = ? WHERE gateway_node_id = ? AND generation = ?`, succeeded, generation, status, taskError, s.now().UTC().Format(time.RFC3339Nano), agentID, generation); err != nil {
		return err
	}
	event := "succeeded"
	if !succeeded {
		event = "failed"
	}
	if err := s.recordTaskEvent(ctx, tx, gatewayComponentTaskID(agentID, generation), agentID, "gateway.component.apply", generation, event, taskError); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) authenticateAgent(ctx context.Context, id, credential string) error {
	var expected []byte
	err := s.db.QueryRowContext(ctx, `SELECT credential_hash FROM agents WHERE id = ? AND status = 'active' AND credential_revoked_at = ''`, id).Scan(&expected)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("center: agent authentication failed")
	}
	if err != nil {
		return fmt.Errorf("center: read agent: %w", err)
	}
	if subtle.ConstantTimeCompare(expected, tokenHash(credential)) != 1 {
		return errors.New("center: agent authentication failed")
	}
	return nil
}
