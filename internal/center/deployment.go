package center

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/secret"
)

type DeploymentRequest struct {
	AgentID    string          `json:"agentId"`
	AppKey     string          `json:"appKey"`
	Config     json.RawMessage `json:"config"`
	Operation  string          `json:"operation"`
	DeleteData bool            `json:"deleteData"`
}

type DeploymentView struct {
	ID                 string              `json:"id"`
	AgentID            string              `json:"agentId"`
	AppKey             string              `json:"appKey"`
	AppVersion         string              `json:"appVersion"`
	State              string              `json:"state"`
	Operation          string              `json:"operation"`
	DeleteData         bool                `json:"deleteData"`
	AccessURL          string              `json:"accessUrl,omitempty"`
	Error              string              `json:"error,omitempty"`
	CreatedAt          time.Time           `json:"createdAt"`
	UpdatedAt          time.Time           `json:"updatedAt"`
	ApplicationID      string              `json:"applicationId,omitempty"`
	OneTimeCredentials *OneTimeCredentials `json:"oneTimeCredentials,omitempty"`
}

type OneTimeCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type AgentTask struct {
	Kind           string                `json:"kind"`
	ID             string                `json:"id"`
	Attempt        int64                 `json:"attempt"`
	AppKey         string                `json:"appKey"`
	Manifest       catalog.AppManifest   `json:"manifest"`
	Config         json.RawMessage       `json:"config"`
	Secrets        json.RawMessage       `json:"secrets"`
	Operation      string                `json:"operation"`
	DeleteData     bool                  `json:"deleteData"`
	Revision       int64                 `json:"revision,omitempty"`
	ApplicationID  string                `json:"applicationId,omitempty"`
	ServiceAddress string                `json:"serviceAddress,omitempty"`
	GatewayState   *gateway.DesiredState `json:"gatewayState,omitempty"`
	TunnelState    *TunnelTaskState      `json:"tunnelState,omitempty"`
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

const applicationTaskRevision int64 = 1
const cpaAppKey = "vastora-official/cpa"
const threeXUIAppKey = "vastora-official/3x-ui"

func (s *Store) CreateDeployment(ctx context.Context, request DeploymentRequest) (DeploymentView, error) {
	if strings.TrimSpace(request.AgentID) == "" || strings.TrimSpace(request.AppKey) == "" {
		return DeploymentView{}, errors.New("center: agent and app are required")
	}
	if request.Operation == "" {
		request.Operation = "install"
	}
	if request.Operation != "install" && request.Operation != "upgrade" && request.Operation != "uninstall" {
		return DeploymentView{}, errors.New("center: invalid deployment operation")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND status = 'active'`, request.AgentID).Scan(&exists); err != nil {
		return DeploymentView{}, fmt.Errorf("center: read agent: %w", err)
	}
	if exists == 0 {
		return DeploymentView{}, errors.New("center: agent not found")
	}
	var activeTasks int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE agent_id = ? AND app_key = ? AND state IN ('pending', 'running')`, request.AgentID, request.AppKey).Scan(&activeTasks); err != nil {
		return DeploymentView{}, fmt.Errorf("center: inspect active deployment task: %w", err)
	}
	if activeTasks != 0 {
		return DeploymentView{}, errors.New("center: this app already has an active deployment task on the target Agent")
	}
	installed, err := s.HasActiveDeployment(ctx, request.AgentID, request.AppKey)
	if err != nil {
		return DeploymentView{}, err
	}
	if request.Operation == "install" && installed {
		return DeploymentView{}, errors.New("center: app is already installed; use upgrade")
	}
	if (request.Operation == "upgrade" || request.Operation == "uninstall") && !installed {
		return DeploymentView{}, errors.New("center: app is not installed on the target Agent")
	}
	apps, err := s.ListApps(ctx)
	if err != nil {
		return DeploymentView{}, err
	}
	var manifest catalog.AppManifest
	found := false
	for _, app := range apps {
		if app.Key == request.AppKey {
			manifest = app.App
			found = true
			break
		}
	}
	if !found {
		return DeploymentView{}, errors.New("center: app not found in a verified catalog")
	}
	var config, secrets []byte
	if request.Operation == "uninstall" {
		config, secrets = []byte(`{}`), []byte(`{}`)
	} else {
		deploymentConfig := request.Config
		if request.Operation == "upgrade" {
			deploymentConfig, err = s.mergePreviousDeploymentConfig(ctx, request.AgentID, request.AppKey, request.Config)
			if err != nil {
				return DeploymentView{}, err
			}
			if request.AppKey == threeXUIAppKey {
				deploymentConfig, err = removeJSONObjectKeys(deploymentConfig, "username", "password", "api_token")
				if err != nil {
					return DeploymentView{}, err
				}
			}
		}
		config, secrets, err = normalizeDeploymentConfig(manifest, deploymentConfig)
		if err != nil {
			return DeploymentView{}, err
		}
	}
	if request.AppKey == "vastora-official/keeper" && request.Operation != "uninstall" {
		secrets, err = s.withCPASecret(ctx, request.AgentID, secrets)
		if err != nil {
			return DeploymentView{}, err
		}
	}
	var oneTimeCredentials *OneTimeCredentials
	if request.AppKey == threeXUIAppKey && request.Operation != "uninstall" {
		secrets, oneTimeCredentials, err = s.withThreeXUISecrets(ctx, request.AgentID, request.Operation, secrets)
		if err != nil {
			return DeploymentView{}, err
		}
	}
	serializedManifest, err := json.Marshal(manifest)
	if err != nil {
		return DeploymentView{}, fmt.Errorf("center: encode deployment manifest: %w", err)
	}
	id, err := randomToken(18)
	if err != nil {
		return DeploymentView{}, err
	}
	now := s.now().UTC()
	deployment := DeploymentView{ID: id, AgentID: request.AgentID, AppKey: request.AppKey, AppVersion: manifest.Version, Operation: request.Operation, DeleteData: request.DeleteData, State: "pending", CreatedAt: now, UpdatedAt: now, OneTimeCredentials: oneTimeCredentials}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeploymentView{}, fmt.Errorf("center: create deployment: %w", err)
	}
	defer tx.Rollback()
	applicationID, err := s.prepareApplication(ctx, tx, request, manifest, now)
	if err != nil {
		return DeploymentView{}, err
	}
	deployment.ApplicationID = applicationID
	var secretID any
	if len(secrets) != 0 && string(secrets) != "{}" {
		secretID, err = s.putSecret(ctx, tx, secrets, "deployment:"+deployment.ID)
		if err != nil {
			return DeploymentView{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, secret_id, operation, delete_data, state, error, created_at, updated_at, application_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`, deployment.ID, deployment.AgentID, deployment.AppKey, deployment.AppVersion, serializedManifest, config, secretID, deployment.Operation, deployment.DeleteData, deployment.State, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), applicationID); err != nil {
		return DeploymentView{}, fmt.Errorf("center: create deployment: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, deployment.ID, deployment.AgentID, "application.apply", applicationTaskRevision, "queued", deployment.Operation+" "+deployment.AppKey); err != nil {
		return DeploymentView{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeploymentView{}, fmt.Errorf("center: create deployment: %w", err)
	}
	return deployment, nil
}

func removeJSONObjectKeys(raw json.RawMessage, keys ...string) (json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	if json.Unmarshal(raw, &values) != nil {
		return nil, errors.New("center: deployment configuration must be a JSON object")
	}
	for _, key := range keys {
		delete(values, key)
	}
	return json.Marshal(values)
}

func (s *Store) mergePreviousDeploymentConfig(ctx context.Context, agentID, appKey string, updates json.RawMessage) (json.RawMessage, error) {
	var deploymentID string
	var configJSON []byte
	var secretID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT d.id, d.config_json, d.secret_id FROM deployments d
		WHERE d.agent_id = ? AND d.app_key = ? AND d.state = 'succeeded' AND d.operation IN ('install', 'upgrade')
		AND NOT EXISTS (
			SELECT 1 FROM deployments removed
			WHERE removed.agent_id = d.agent_id AND removed.app_key = d.app_key
			AND removed.state = 'succeeded' AND removed.operation = 'uninstall' AND removed.created_at > d.created_at
		)
		ORDER BY d.updated_at DESC, d.rowid DESC LIMIT 1`, agentID, appKey).Scan(&deploymentID, &configJSON, &secretID); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: upgrade requires an existing successful installation")
	} else if err != nil {
		return nil, fmt.Errorf("center: read previous deployment configuration: %w", err)
	}
	merged := map[string]json.RawMessage{}
	if json.Unmarshal(configJSON, &merged) != nil {
		return nil, errors.New("center: stored deployment configuration is invalid")
	}
	if secretID.Valid {
		secretJSON, err := s.getSecret(ctx, secretID.String, "deployment:"+deploymentID)
		if err != nil {
			return nil, err
		}
		var secretValues map[string]json.RawMessage
		if json.Unmarshal(secretJSON, &secretValues) != nil {
			return nil, errors.New("center: stored deployment secrets are invalid")
		}
		for key, value := range secretValues {
			merged[key] = value
		}
	}
	if len(updates) != 0 {
		var changed map[string]json.RawMessage
		if json.Unmarshal(updates, &changed) != nil {
			return nil, errors.New("center: deployment configuration must be a JSON object")
		}
		for key, value := range changed {
			merged[key] = value
		}
	}
	result, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("center: merge deployment configuration: %w", err)
	}
	return result, nil
}

func (s *Store) withCPASecret(ctx context.Context, agentID string, raw json.RawMessage) (json.RawMessage, error) {
	installed, err := s.HasActiveDeployment(ctx, agentID, cpaAppKey)
	if err != nil {
		return nil, err
	}
	if !installed {
		return nil, errors.New("center: Keeper requires a successful CPA installation on this Agent")
	}
	var deploymentID, secretID string
	if err := s.db.QueryRowContext(ctx, `SELECT id, secret_id FROM deployments WHERE agent_id = ? AND app_key = ? AND state = 'succeeded' AND operation IN ('install', 'upgrade') AND secret_id IS NOT NULL ORDER BY updated_at DESC, rowid DESC LIMIT 1`, agentID, cpaAppKey).Scan(&deploymentID, &secretID); errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("center: Keeper requires a successful CPA installation on this Agent")
	} else if err != nil {
		return nil, fmt.Errorf("center: read CPA deployment: %w", err)
	}
	cpaSecrets, err := s.getSecret(ctx, secretID, "deployment:"+deploymentID)
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	var cpa map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || json.Unmarshal(cpaSecrets, &cpa) != nil || cpa["management_key"] == nil {
		return nil, errors.New("center: CPA management key is unavailable")
	}
	values["cpa_management_key"] = cpa["management_key"]
	return json.Marshal(values)
}

func (s *Store) withThreeXUISecrets(ctx context.Context, agentID, operation string, encoded []byte) ([]byte, *OneTimeCredentials, error) {
	values := map[string]string{}
	if len(encoded) != 0 && json.Unmarshal(encoded, &values) != nil {
		return nil, nil, errors.New("center: invalid 3x-ui secret configuration")
	}
	if operation == "upgrade" {
		var deploymentID, secretID string
		err := s.db.QueryRowContext(ctx, `SELECT id, secret_id FROM deployments WHERE agent_id = ? AND app_key = ? AND state = 'succeeded' AND operation IN ('install', 'upgrade') AND secret_id IS NOT NULL ORDER BY updated_at DESC, rowid DESC LIMIT 1`, agentID, threeXUIAppKey).Scan(&deploymentID, &secretID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, errors.New("center: previous 3x-ui credentials were not found")
		}
		if err != nil {
			return nil, nil, err
		}
		previous, err := s.getSecret(ctx, secretID, "deployment:"+deploymentID)
		if err != nil {
			return nil, nil, err
		}
		if json.Unmarshal(previous, &values) != nil || values["username"] == "" || values["password"] == "" {
			return nil, nil, errors.New("center: stored 3x-ui credentials are invalid")
		}
		result, err := json.Marshal(values)
		return result, nil, err
	}
	usernameToken, err := randomToken(6)
	if err != nil {
		return nil, nil, err
	}
	password, err := randomToken(24)
	if err != nil {
		return nil, nil, err
	}
	values["username"] = "vastora-" + strings.ToLower(usernameToken)
	values["password"] = password
	result, err := json.Marshal(values)
	if err != nil {
		return nil, nil, err
	}
	return result, &OneTimeCredentials{Username: values["username"], Password: values["password"]}, nil
}

func (s *Store) storeApplicationSecrets(ctx context.Context, tx *sql.Tx, deploymentID, applicationID string, generated map[string]string, now time.Time) error {
	var secretID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT secret_id FROM deployments WHERE id = ?`, deploymentID).Scan(&secretID); err != nil {
		return err
	}
	values := map[string]string{}
	if secretID.Valid {
		var sealed []byte
		if err := tx.QueryRowContext(ctx, `SELECT sealed FROM secrets WHERE id = ?`, secretID.String).Scan(&sealed); err != nil {
			return err
		}
		plain, err := secret.Open(s.key, sealed, []byte("deployment:"+deploymentID))
		if err != nil {
			return err
		}
		if json.Unmarshal(plain, &values) != nil {
			return errors.New("center: stored application secrets are invalid")
		}
	}
	for key, value := range generated {
		values[key] = value
	}
	if len(values) == 0 {
		return nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	applicationSecretID, err := s.putSecret(ctx, tx, encoded, "application:"+applicationID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO application_secrets(application_id, secret_id, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(application_id) DO UPDATE SET secret_id = excluded.secret_id, updated_at = excluded.updated_at`, applicationID, applicationSecretID, now.Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListDeployments(ctx context.Context) ([]DeploymentView, error) {
	apps, err := s.ListApps(ctx)
	if err != nil {
		return nil, err
	}
	homepages := make(map[string]*catalog.Homepage, len(apps))
	for _, app := range apps {
		homepages[app.Key] = app.App.Homepage
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.agent_id, d.app_key, d.app_version, d.operation, d.delete_data, d.state, d.error, d.created_at, d.updated_at, d.application_id
		FROM deployments AS d
		ORDER BY d.created_at DESC, d.rowid DESC
		LIMIT 200`)
	if err != nil {
		return nil, fmt.Errorf("center: list deployments: %w", err)
	}
	deployments := make([]DeploymentView, 0)
	for rows.Next() {
		var deployment DeploymentView
		var createdAt, updatedAt string
		if err := rows.Scan(&deployment.ID, &deployment.AgentID, &deployment.AppKey, &deployment.AppVersion, &deployment.Operation, &deployment.DeleteData, &deployment.State, &deployment.Error, &createdAt, &updatedAt, &deployment.ApplicationID); err != nil {
			return nil, fmt.Errorf("center: scan deployment: %w", err)
		}
		var err error
		deployment.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("center: parse deployment time: %w", err)
		}
		deployment.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
		if err != nil {
			return nil, fmt.Errorf("center: parse deployment time: %w", err)
		}
		deployments = append(deployments, deployment)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range deployments {
		if deployments[index].State != "succeeded" || deployments[index].Operation == "uninstall" {
			continue
		}
		deployments[index].AccessURL, err = s.applicationHomepageURL(ctx, deployments[index].ApplicationID, homepages[deployments[index].AppKey])
		if err != nil {
			return nil, err
		}
	}
	return deployments, nil
}

func (s *Store) applicationHomepageURL(ctx context.Context, applicationID string, homepage *catalog.Homepage) (string, error) {
	if homepage == nil {
		return "", nil
	}
	var protocol, endpoint string
	err := s.db.QueryRowContext(ctx, `SELECT protocol, endpoint FROM services
		WHERE application_id = ? AND name = ? AND status IN ('ready', 'publishing')`, applicationID, homepage.Service).Scan(&protocol, &endpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("center: read application homepage service: %w", err)
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", errors.New("center: stored homepage endpoint is invalid")
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return "", nil
	}
	if protocol != "http" && protocol != "https" {
		return "", errors.New("center: stored homepage protocol is invalid")
	}
	return (&url.URL{Scheme: protocol, Host: endpoint, Path: homepage.Path}).String(), nil
}

func (s *Store) ClaimNextTask(ctx context.Context, agentID, credential string) (*AgentTask, error) {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return nil, err
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
	var attempt int64
	err = tx.QueryRowContext(ctx, `SELECT d.id, d.app_key, d.manifest_json, d.config_json, d.secret_id, d.operation, d.delete_data, d.application_id, COALESCE(p.service_address, ''), d.attempt
		FROM deployments d LEFT JOIN agent_network_profiles p ON p.agent_id = d.agent_id WHERE d.agent_id = ? AND d.state = 'pending' ORDER BY d.created_at, d.rowid LIMIT 1`, agentID).Scan(&task.ID, &task.AppKey, &manifest, &task.Config, &secretID, &task.Operation, &task.DeleteData, &task.ApplicationID, &task.ServiceAddress, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
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
				return nil, nil
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
		task = AgentTask{Kind: "gateway.routes.apply", ID: gatewayRouteTaskID(agentID, revision), Attempt: attempt + 1, Revision: revision, GatewayState: &state}
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

func (s *Store) CompleteTask(ctx context.Context, agentID, credential, taskID string, expectedAttempt int64, succeeded bool, taskError string, rawResult json.RawMessage) error {
	if err := s.authenticateAgent(ctx, agentID, credential); err != nil {
		return err
	}
	if revision, gatewayTask := gatewayTaskRevision(taskID); gatewayTask {
		return s.CompleteGatewayState(ctx, agentID, credential, revision, expectedAttempt, succeeded, taskError)
	}
	if revision, tunnelTask := tunnelTaskRevision(taskID); tunnelTask {
		return s.completeTunnelState(ctx, agentID, revision, expectedAttempt, succeeded, taskError)
	}
	if generation, componentTask := gatewayComponentTaskGeneration(taskID); componentTask {
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
	var applicationID, operation, currentState string
	var attempt int64
	var manifestJSON, configJSON []byte
	var serviceAddress string
	if err := tx.QueryRowContext(ctx, `SELECT d.application_id, d.operation, d.state, d.manifest_json, d.config_json, COALESCE(p.service_address, ''), d.attempt
		FROM deployments d LEFT JOIN agent_network_profiles p ON p.agent_id = d.agent_id WHERE d.id = ? AND d.agent_id = ?`, taskID, agentID).Scan(&applicationID, &operation, &currentState, &manifestJSON, &configJSON, &serviceAddress, &attempt); err != nil {
		return errors.New("center: task not found")
	}
	if currentState == "succeeded" || currentState == "failed" {
		return nil
	}
	if currentState != "running" {
		return errors.New("center: task is not active")
	}
	if expectedAttempt <= 0 || expectedAttempt != attempt {
		return errors.New("center: stale task result")
	}
	now := s.now().UTC()
	if succeeded {
		var taskResult ApplicationTaskResult
		if len(rawResult) != 0 && string(rawResult) != "null" && json.Unmarshal(rawResult, &taskResult) != nil {
			return errors.New("center: invalid Agent task result")
		}
		if operation != "uninstall" {
			var manifest catalog.AppManifest
			if json.Unmarshal(manifestJSON, &manifest) != nil || catalog.ValidateApp(manifest) != nil {
				return errors.New("center: stored deployment manifest is invalid")
			}
			if err := validateApplicationResult(manifest, configJSON, serviceAddress, taskResult); err != nil {
				state = "failed"
				taskError = err.Error()
				succeeded = false
			}
		}
		if succeeded {
			if err := s.completeApplication(ctx, tx, taskID, applicationID, operation, taskResult, now); err != nil {
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
	result, err := tx.ExecContext(ctx, `UPDATE deployments SET state = ?, lease_expires_at = '', error = ?, updated_at = ? WHERE id = ? AND agent_id = ? AND state = 'running'`, state, taskError, now.Format(time.RFC3339Nano), taskID, agentID)
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
	return tx.Commit()
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

func validateApplicationResult(manifest catalog.AppManifest, configJSON []byte, serviceAddress string, result ApplicationTaskResult) error {
	if len(result.Services) != len(manifest.Services) {
		return errors.New("center: Agent service result does not match the signed manifest")
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(configJSON, &config) != nil {
		return errors.New("center: stored deployment configuration is invalid")
	}
	seen := make(map[string]bool, len(result.Services))
	for _, reported := range result.Services {
		declared, err := selectedCatalogService(manifest, reported.Name)
		if err != nil || seen[reported.Name] || reported.Protocol != declared.Protocol || reported.ContainerPort != declared.ContainerPort {
			return errors.New("center: Agent service result does not match the signed manifest")
		}
		seen[reported.Name] = true
		expectedPort := declared.DefaultHostPort
		if declared.HostPortField != "" && json.Unmarshal(config[declared.HostPortField], &expectedPort) != nil {
			return errors.New("center: stored service port is invalid")
		}
		if reported.HostPort != expectedPort {
			return errors.New("center: Agent reported an unexpected service port")
		}
		expectedAddress := "127.0.0.1"
		if serviceAddress != "" {
			expectedAddress = serviceAddress
		}
		if reported.Address != expectedAddress {
			return errors.New("center: Agent reported a service outside its confirmed private service address")
		}
	}
	return nil
}

func (s *Store) authenticateAgent(ctx context.Context, id, credential string) error {
	var expected []byte
	err := s.db.QueryRowContext(ctx, `SELECT credential_hash FROM agents WHERE id = ? AND status = 'active'`, id).Scan(&expected)
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

func normalizeDeploymentConfig(manifest catalog.AppManifest, raw json.RawMessage) ([]byte, json.RawMessage, error) {
	values := make(map[string]json.RawMessage, len(manifest.Config))
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, nil, errors.New("center: deployment configuration must be a JSON object")
		}
	}
	fields := make(map[string]catalog.ConfigField, len(manifest.Config))
	for _, field := range manifest.Config {
		fields[field.Key] = field
		if _, exists := values[field.Key]; !exists && field.Default != nil {
			values[field.Key] = *field.Default
		}
	}
	for key := range values {
		if _, exists := fields[key]; !exists {
			return nil, nil, fmt.Errorf("center: unknown configuration field %q", key)
		}
	}
	for _, field := range manifest.Config {
		value, exists := values[field.Key]
		if !exists {
			if field.Required {
				return nil, nil, fmt.Errorf("center: configuration field %q is required", field.Key)
			}
			continue
		}
		if err := validateConfigValue(field, value); err != nil {
			return nil, nil, err
		}
	}
	configuration := make(map[string]json.RawMessage, len(values))
	secrets := make(map[string]json.RawMessage)
	for key, value := range values {
		if fields[key].Secret {
			secrets[key] = value
			continue
		}
		configuration[key] = value
	}
	configJSON, err := json.Marshal(configuration)
	if err != nil {
		return nil, nil, fmt.Errorf("center: encode deployment configuration: %w", err)
	}
	secretJSON, err := json.Marshal(secrets)
	if err != nil {
		return nil, nil, fmt.Errorf("center: encode deployment secrets: %w", err)
	}
	return configJSON, secretJSON, nil
}

func validateConfigValue(field catalog.ConfigField, raw json.RawMessage) error {
	switch field.Type {
	case "string":
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("center: configuration field %q must be a string", field.Key)
		}
	case "boolean":
		var value bool
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("center: configuration field %q must be a boolean", field.Key)
		}
	case "integer":
		var value int
		if json.Unmarshal(raw, &value) != nil {
			return fmt.Errorf("center: configuration field %q must be an integer", field.Key)
		}
	default:
		return fmt.Errorf("center: unsupported configuration field %q", field.Key)
	}
	return nil
}
