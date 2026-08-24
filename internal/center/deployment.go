package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"golang.org/x/mod/semver"
)

type DeploymentRequest struct {
	AgentID    string          `json:"agentId"`
	AppKey     string          `json:"appKey"`
	Role       string          `json:"role,omitempty"`
	Config     json.RawMessage `json:"config"`
	Operation  string          `json:"operation"`
	DeleteData bool            `json:"deleteData"`
}

type DeploymentView struct {
	ID                     string              `json:"id"`
	AgentID                string              `json:"agentId"`
	AppKey                 string              `json:"appKey"`
	AppVersion             string              `json:"appVersion"`
	State                  string              `json:"state"`
	ReconciliationRequired bool                `json:"reconciliationRequired"`
	Operation              string              `json:"operation"`
	DeleteData             bool                `json:"deleteData"`
	AccessURL              string              `json:"accessUrl,omitempty"`
	Error                  string              `json:"error,omitempty"`
	CreatedAt              time.Time           `json:"createdAt"`
	UpdatedAt              time.Time           `json:"updatedAt"`
	ApplicationID          string              `json:"applicationId,omitempty"`
	OneTimeCredentials     *OneTimeCredentials `json:"oneTimeCredentials,omitempty"`
}

type OneTimeCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

const applicationTaskRevision int64 = 1
const cpaAppKey = "vastora-official/cpa"
const threeXUIAppKey = "vastora-official/3x-ui"

func (s *Store) CreateDeployment(ctx context.Context, request DeploymentRequest) (DeploymentView, error) {
	request.Role = strings.TrimSpace(request.Role)
	if strings.TrimSpace(request.AgentID) == "" || strings.TrimSpace(request.AppKey) == "" {
		return DeploymentView{}, errors.New("center: agent and app are required")
	}
	if request.Operation == "" {
		request.Operation = "install"
	}
	if request.Operation != "install" && request.Operation != "upgrade" && request.Operation != "configure" && request.Operation != "uninstall" {
		return DeploymentView{}, errors.New("center: invalid deployment operation")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ? AND status = 'active'`, request.AgentID).Scan(&exists); err != nil {
		return DeploymentView{}, fmt.Errorf("center: read agent: %w", err)
	}
	if exists == 0 {
		return DeploymentView{}, errors.New("center: agent not found")
	}
	if request.AppKey == threeXUIAppKey && request.Operation == "install" {
		if err := s.validateThreeXUIInstallRole(ctx, request.AgentID, request.Role); err != nil {
			return DeploymentView{}, err
		}
	} else if request.Role != "" {
		return DeploymentView{}, errors.New("center: application role is only valid while installing 3x-ui")
	}
	var activeTasks int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployments WHERE agent_id = ? AND app_key = ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, request.AgentID, request.AppKey).Scan(&activeTasks); err != nil {
		return DeploymentView{}, fmt.Errorf("center: inspect active deployment task: %w", err)
	}
	if activeTasks != 0 {
		return DeploymentView{}, errors.New("center: this app already has an active deployment task on the target Agent")
	}
	active, err := s.activeDeployment(ctx, request.AgentID, request.AppKey)
	if err != nil {
		return DeploymentView{}, err
	}
	if request.Operation == "install" && active.Installed {
		return DeploymentView{}, errors.New("center: app is already installed; use upgrade or configure")
	}
	if (request.Operation == "upgrade" || request.Operation == "configure" || request.Operation == "uninstall") && !active.Installed {
		return DeploymentView{}, errors.New("center: app is not installed on the target Agent")
	}
	if request.AppKey == threeXUIAppKey && request.Operation == "uninstall" {
		if err := s.validateThreeXUIUninstall(ctx, request.AgentID); err != nil {
			return DeploymentView{}, err
		}
	}
	var manifest catalog.AppManifest
	if request.Operation == "install" || request.Operation == "upgrade" {
		apps, err := s.ListApps(ctx)
		if err != nil {
			return DeploymentView{}, err
		}
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
	} else if len(active.Manifest) == 0 || json.Unmarshal(active.Manifest, &manifest) != nil || catalog.ValidateApp(manifest) != nil {
		return DeploymentView{}, errors.New("center: installed application manifest is invalid")
	}
	if request.Operation == "upgrade" {
		comparison := semver.Compare(canonicalAppVersion(manifest.Version), canonicalAppVersion(active.Version))
		if comparison == 0 {
			return DeploymentView{}, fmt.Errorf("center: app is already at version %s", active.Version)
		}
		if comparison < 0 {
			return DeploymentView{}, fmt.Errorf("center: catalog version %s is older than installed version %s; downgrade is not allowed", manifest.Version, active.Version)
		}
	}
	if request.Operation == "configure" {
		var changed map[string]json.RawMessage
		if len(request.Config) == 0 || json.Unmarshal(request.Config, &changed) != nil || len(changed) == 0 {
			return DeploymentView{}, errors.New("center: configure requires at least one changed setting")
		}
	}
	var config, secrets []byte
	if request.Operation == "uninstall" {
		config, secrets = []byte(`{}`), []byte(`{}`)
	} else {
		deploymentConfig := request.Config
		if request.Operation == "upgrade" || request.Operation == "configure" {
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
		if request.Operation == "install" && request.Role == threeXUIRoleWorker {
			oneTimeCredentials = nil
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
	var serviceAddress string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(service_address, '') FROM agent_network_profiles WHERE agent_id = ?`, request.AgentID).Scan(&serviceAddress); errors.Is(err, sql.ErrNoRows) {
		if request.Operation != "uninstall" {
			return DeploymentView{}, errors.New("center: confirm the Agent private service address before installing or changing applications")
		}
	} else if err != nil {
		return DeploymentView{}, fmt.Errorf("center: capture deployment service address: %w", err)
	}
	if request.Operation != "uninstall" && net.ParseIP(serviceAddress) == nil {
		return DeploymentView{}, errors.New("center: confirm a valid Agent private service address before installing or changing applications")
	}
	deployment.ApplicationID = applicationID
	var secretID any
	if len(secrets) != 0 && string(secrets) != "{}" {
		secretID, err = s.putSecret(ctx, tx, secrets, "deployment:"+deployment.ID)
		if err != nil {
			return DeploymentView{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id, agent_id, app_key, app_version, manifest_json, config_json, service_address, secret_id, operation, delete_data, state, error, created_at, updated_at, application_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?)`, deployment.ID, deployment.AgentID, deployment.AppKey, deployment.AppVersion, serializedManifest, config, serviceAddress, secretID, deployment.Operation, deployment.DeleteData, deployment.State, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), applicationID); err != nil {
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

func canonicalAppVersion(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "v") {
		return value
	}
	return "v" + value
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
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.agent_id, d.app_key, d.app_version, d.operation, d.delete_data, d.state, d.reconciliation_required, d.error, d.created_at, d.updated_at, d.application_id
		FROM deployments AS d WHERE d.state IN ('pending', 'running') OR d.reconciliation_required = 1 OR d.id IN (
			SELECT recent.id FROM deployments recent ORDER BY recent.created_at DESC, recent.rowid DESC LIMIT 200
		)
		ORDER BY d.created_at DESC, d.rowid DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("center: list deployments: %w", err)
	}
	deployments := make([]DeploymentView, 0)
	for rows.Next() {
		var deployment DeploymentView
		var createdAt, updatedAt string
		if err := rows.Scan(&deployment.ID, &deployment.AgentID, &deployment.AppKey, &deployment.AppVersion, &deployment.Operation, &deployment.DeleteData, &deployment.State, &deployment.ReconciliationRequired, &deployment.Error, &createdAt, &updatedAt, &deployment.ApplicationID); err != nil {
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
	var hostname string
	var tlsEnabled int
	err := s.db.QueryRowContext(ctx, `SELECT p.hostname, p.tls_enabled FROM services s
		JOIN publications p ON p.service_id = s.id
		WHERE s.application_id = ? AND s.name = ? AND s.status IN ('ready', 'publishing') AND p.status = 'ready'
		ORDER BY CASE p.kind WHEN 'headscale_gateway' THEN 0 WHEN 'lan_gateway' THEN 1 WHEN 'public_direct' THEN 2 WHEN 'cloudflare_tunnel' THEN 3 ELSE 4 END, p.updated_at DESC LIMIT 1`, applicationID, homepage.Service).Scan(&hostname, &tlsEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("center: read application homepage publication: %w", err)
	}
	scheme := "http"
	if tlsEnabled == 1 {
		scheme = "https"
	}
	return (&url.URL{Scheme: scheme, Host: hostname, Path: homepage.Path}).String(), nil
}
