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

	"github.com/petauron/vastora/internal/secret"
)

const (
	realityCommandKind      = "3xui.reality.create"
	subscriptionCommandKind = "3xui.subscription.configure"
	clientCommandKind       = "3xui.clients.manage"
	nodeCommandKind         = "3xui.node.reconcile"
	controllerCommandKind   = "3xui.controller.manage"
)

type RealityCommandInput struct {
	ApplicationID string `json:"applicationId"`
	Name          string `json:"name"`
	GatewayNodeID string `json:"gatewayNodeId"`
	Hostname      string `json:"hostname"`
	DNSProvider   string `json:"dnsProvider"`
	Target        string `json:"target,omitempty"`
	SNIHostname   string `json:"sniHostname,omitempty"`
}

type RealityCommandTask struct {
	Name                string   `json:"name"`
	ConnectHostname     string   `json:"connectHostname"`
	DNSProvider         string   `json:"dnsProvider"`
	Target              string   `json:"target,omitempty"`
	SNIHostname         string   `json:"sniHostname,omitempty"`
	ExcludedSNI         []string `json:"excludedSni,omitempty"`
	TargetApplicationID string   `json:"targetApplicationId"`
	TargetAddress       string   `json:"targetAddress"`
	TargetPanelPort     int      `json:"targetPanelPort"`
	TargetNodeID        int      `json:"targetNodeId,omitempty"`
	TargetAPIToken      string   `json:"targetApiToken,omitempty"`
}

type RealityCommandResult struct {
	InboundID       int    `json:"inboundId"`
	Name            string `json:"name"`
	Listen          string `json:"listen"`
	Port            int    `json:"port"`
	Target          string `json:"target"`
	SNIHostname     string `json:"sniHostname"`
	ConnectHostname string `json:"connectHostname"`
	ShareURI        string `json:"shareUri"`
}

type SubscriptionCommandInput struct {
	ApplicationID string `json:"applicationId"`
	GatewayNodeID string `json:"gatewayNodeId"`
	Hostname      string `json:"hostname"`
	Kind          string `json:"kind"`
	DNSProvider   string `json:"dnsProvider"`
}

type SubscriptionCommandTask struct {
	Domain        string `json:"domain"`
	BaseURI       string `json:"baseUri"`
	PublicationID string `json:"publicationId"`
}

type SubscriptionCommandResult struct {
	Domain  string `json:"domain"`
	BaseURI string `json:"baseUri"`
}

type ThreeXUIClientCommandInput struct {
	ApplicationID string `json:"applicationId"`
	Action        string `json:"action"`
	Email         string `json:"email,omitempty"`
	NewEmail      string `json:"newEmail,omitempty"`
	InboundID     int    `json:"inboundId,omitempty"`
	Enabled       bool   `json:"enabled"`
	TotalBytes    int64  `json:"totalBytes"`
	ExpiryTime    int64  `json:"expiryTime"`
	LimitIP       int    `json:"limitIp"`
}

type ThreeXUIClientInbound struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	ApplicationID   string `json:"applicationId"`
	NodeID          string `json:"nodeId"`
	NodeName        string `json:"nodeName"`
	ConnectHostname string `json:"connectHostname,omitempty"`
	SNIHostname     string `json:"sniHostname,omitempty"`
}

type ThreeXUIClientCommandTask struct {
	Action              string                  `json:"action"`
	Email               string                  `json:"email,omitempty"`
	NewEmail            string                  `json:"newEmail,omitempty"`
	InboundID           int                     `json:"inboundId,omitempty"`
	Enabled             bool                    `json:"enabled"`
	TotalBytes          int64                   `json:"totalBytes"`
	ExpiryTime          int64                   `json:"expiryTime"`
	LimitIP             int                     `json:"limitIp"`
	Inbounds            []ThreeXUIClientInbound `json:"inbounds"`
	SubscriptionBaseURI string                  `json:"subscriptionBaseUri,omitempty"`
}

type ThreeXUIClientView struct {
	Email           string `json:"email"`
	Enabled         bool   `json:"enabled"`
	TotalBytes      int64  `json:"totalBytes"`
	UsedBytes       int64  `json:"usedBytes"`
	ExpiryTime      int64  `json:"expiryTime"`
	LimitIP         int    `json:"limitIp"`
	InboundIDs      []int  `json:"inboundIds"`
	HasSubscription bool   `json:"hasSubscription"`
}

type ThreeXUIClientCommandResult struct {
	Clients         []ThreeXUIClientView    `json:"clients,omitempty"`
	ClientsObserved bool                    `json:"clientsObserved"`
	Inbounds        []ThreeXUIClientInbound `json:"inbounds"`
	Secret          string                  `json:"secret,omitempty"`
	SecretKind      string                  `json:"secretKind,omitempty"`
}

type ThreeXUINodeCommandTask struct {
	Action              string `json:"action"`
	WorkerApplicationID string `json:"workerApplicationId"`
	Name                string `json:"name"`
	Address             string `json:"address"`
	Port                int    `json:"port"`
	RemoteNodeID        int    `json:"remoteNodeId,omitempty"`
	APIToken            string `json:"apiToken,omitempty"`
}

type ThreeXUINodeCommandResult struct {
	RemoteNodeID int    `json:"remoteNodeId"`
	Status       string `json:"status"`
}

type ThreeXUIControllerCommandTask struct {
	Action              string `json:"action"`
	MigrationID         string `json:"migrationId,omitempty"`
	ApplicationID       string `json:"applicationId"`
	SourceApplicationID string `json:"sourceApplicationId,omitempty"`
	SourceName          string `json:"sourceName,omitempty"`
	SourceAddress       string `json:"sourceAddress,omitempty"`
	SourcePanelPort     int    `json:"sourcePanelPort,omitempty"`
	SourceRemoteNodeID  int    `json:"sourceRemoteNodeId,omitempty"`
	BackupRevision      int64  `json:"backupRevision,omitempty"`
	SourceAPIToken      string `json:"sourceApiToken,omitempty"`
}

type ThreeXUIControllerCommandResult struct {
	Action             string `json:"action"`
	BackupRevision     int64  `json:"backupRevision,omitempty"`
	BackupSHA256       string `json:"backupSha256,omitempty"`
	BackupSize         int64  `json:"backupSize,omitempty"`
	SourceRemoteNodeID int    `json:"sourceRemoteNodeId,omitempty"`
}

type ApplicationCommandView struct {
	ID                    string                  `json:"id"`
	ApplicationID         string                  `json:"applicationId"`
	GatewayNodeID         string                  `json:"gatewayNodeId"`
	Kind                  string                  `json:"kind"`
	State                 string                  `json:"state"`
	Hostname              string                  `json:"hostname"`
	DNSProvider           string                  `json:"dnsProvider"`
	Target                string                  `json:"target,omitempty"`
	SNIHostname           string                  `json:"sniHostname,omitempty"`
	PublicationID         string                  `json:"publicationId,omitempty"`
	Action                string                  `json:"action,omitempty"`
	Clients               []ThreeXUIClientView    `json:"clients,omitempty"`
	ClientsObserved       bool                    `json:"clientsObserved,omitempty"`
	Inbounds              []ThreeXUIClientInbound `json:"inbounds,omitempty"`
	SubscriptionAvailable bool                    `json:"subscriptionAvailable,omitempty"`
	Error                 string                  `json:"error,omitempty"`
	ResultAvailable       bool                    `json:"resultAvailable"`
	CreatedAt             time.Time               `json:"createdAt"`
	UpdatedAt             time.Time               `json:"updatedAt"`
}

func normalizeRealityCommandInput(input RealityCommandInput) (RealityCommandInput, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	input.Name = strings.TrimSpace(input.Name)
	input.GatewayNodeID = strings.TrimSpace(input.GatewayNodeID)
	input.Hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.Hostname), "."))
	input.DNSProvider = strings.TrimSpace(input.DNSProvider)
	input.Target = strings.ToLower(strings.TrimSpace(input.Target))
	input.SNIHostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.SNIHostname), "."))
	if input.ApplicationID == "" || input.GatewayNodeID == "" || input.Name == "" || len(input.Name) > 64 || !domainSuffixPattern.MatchString(input.Hostname) {
		return input, errors.New("center: application, name, gateway, and a valid connection hostname are required")
	}
	if input.DNSProvider != "manual" && input.DNSProvider != "cloudflare" {
		return input, errors.New("center: REALITY DNS must be manual or Cloudflare")
	}
	if (input.Target == "") != (input.SNIHostname == "") {
		return input, errors.New("center: custom REALITY target and SNI must be provided together")
	}
	if input.Target != "" {
		if !strings.Contains(input.Target, ":") {
			input.Target += ":443"
		}
		host, port, err := net.SplitHostPort(input.Target)
		if err != nil || !domainSuffixPattern.MatchString(strings.TrimSuffix(host, ".")) || port != "443" || !domainSuffixPattern.MatchString(input.SNIHostname) {
			return input, errors.New("center: custom REALITY target must be a hostname on port 443 with a valid SNI")
		}
	}
	return input, nil
}

func validateRealityCommandResult(input RealityCommandTask, result RealityCommandResult) error {
	if result.InboundID < 1 || result.Name != input.Name || net.ParseIP(result.Listen) == nil || result.Port < 1024 || result.Port > 65535 || result.Port == 443 || result.ConnectHostname != input.ConnectHostname || !domainSuffixPattern.MatchString(result.SNIHostname) {
		return errors.New("center: Agent returned an unsafe REALITY result")
	}
	targetHost, targetPort, err := net.SplitHostPort(strings.ToLower(strings.TrimSpace(result.Target)))
	if err != nil || targetPort != "443" || !domainSuffixPattern.MatchString(strings.TrimSuffix(targetHost, ".")) {
		return errors.New("center: Agent returned an invalid REALITY target")
	}
	if input.Target != "" && (result.Target != input.Target || result.SNIHostname != input.SNIHostname) {
		return errors.New("center: Agent changed the requested REALITY target")
	}
	share, err := url.Parse(strings.TrimSpace(result.ShareURI))
	if err != nil || share.Scheme != "vless" || share.User == nil || share.User.Username() == "" || share.Hostname() != input.ConnectHostname || share.Port() != "443" {
		return errors.New("center: Agent returned an invalid REALITY client link")
	}
	if _, hasPassword := share.User.Password(); hasPassword {
		return errors.New("center: Agent returned an invalid REALITY client link")
	}
	query := share.Query()
	if query.Get("type") != "tcp" || query.Get("security") != "reality" || query.Get("flow") != "xtls-rprx-vision" || query.Get("sni") != result.SNIHostname || query.Get("pbk") == "" || query.Get("sid") == "" {
		return errors.New("center: Agent returned an invalid REALITY client link")
	}
	return nil
}

func (s *Store) CreateRealityCommand(ctx context.Context, input RealityCommandInput) (ApplicationCommandView, error) {
	var err error
	input, err = normalizeRealityCommandInput(input)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	var targetAgentID, siteID, appKey, status, role, targetAddress string
	if err := tx.QueryRowContext(ctx, `SELECT a.node_id, a.site_id, a.app_key, a.status, a.role,
		COALESCE(p.service_address, '')
		FROM applications a LEFT JOIN agent_network_profiles p ON p.agent_id = a.node_id WHERE a.id = ?`, input.ApplicationID).Scan(&targetAgentID, &siteID, &appKey, &status, &role, &targetAddress); errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: application not found")
	} else if err != nil {
		return ApplicationCommandView{}, err
	}
	if appKey != threeXUIAppKey || status != "running" {
		return ApplicationCommandView{}, errors.New("center: REALITY requires a running official 3x-ui application")
	}
	if role != threeXUIRoleMaster && role != threeXUIRoleWorker {
		return ApplicationCommandView{}, errors.New("center: 3x-ui topology role is not configured")
	}
	if net.ParseIP(targetAddress) == nil {
		return ApplicationCommandView{}, errors.New("center: target VLESS node has no confirmed private service address")
	}
	var agentID string
	var targetNodeID int
	if role == threeXUIRoleMaster {
		agentID = targetAgentID
	} else {
		if err := tx.QueryRowContext(ctx, `SELECT master.node_id, n.remote_node_id
			FROM three_x_ui_nodes n JOIN applications master ON master.id = n.master_application_id
			WHERE n.worker_application_id = ? AND n.status = 'ready' AND master.status = 'running'`, input.ApplicationID).Scan(&agentID, &targetNodeID); errors.Is(err, sql.ErrNoRows) {
			return ApplicationCommandView{}, errors.New("center: this VLESS node is not connected to the Site 3x-ui controller")
		} else if err != nil {
			return ApplicationCommandView{}, err
		}
	}
	var targetConfig []byte
	if err := tx.QueryRowContext(ctx, `SELECT config_json FROM deployments WHERE application_id = ? AND operation IN ('install', 'upgrade', 'configure') AND state = 'succeeded' ORDER BY created_at DESC, rowid DESC LIMIT 1`, input.ApplicationID).Scan(&targetConfig); err != nil {
		return ApplicationCommandView{}, errors.New("center: target VLESS node configuration is unavailable")
	}
	var targetSettings struct {
		PanelPort int `json:"panel_port"`
	}
	if json.Unmarshal(targetConfig, &targetSettings) != nil || targetSettings.PanelPort < 1024 || targetSettings.PanelPort > 65535 {
		return ApplicationCommandView{}, errors.New("center: target VLESS node configuration is invalid")
	}
	if err := validateGatewayForPublication(ctx, tx, siteID, input.GatewayNodeID, publicationShared443); err != nil {
		return ApplicationCommandView{}, err
	}
	if input.DNSProvider == "cloudflare" {
		var zoneName string
		if err := tx.QueryRowContext(ctx, `SELECT endpoint FROM network_integrations WHERE kind = 'cloudflare' AND status = 'configured'`).Scan(&zoneName); errors.Is(err, sql.ErrNoRows) {
			return ApplicationCommandView{}, errors.New("center: configure Cloudflare before using managed DNS")
		} else if err != nil {
			return ApplicationCommandView{}, err
		}
		if input.Hostname != zoneName && !strings.HasSuffix(input.Hostname, "."+zoneName) {
			return ApplicationCommandView{}, errors.New("center: connection hostname must belong to the configured Cloudflare Zone")
		}
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE application_id = ? AND kind <> ? AND state IN ('pending', 'running')`, input.ApplicationID, controllerCommandKind).Scan(&active); err != nil {
		return ApplicationCommandView{}, err
	}
	if active != 0 {
		return ApplicationCommandView{}, errors.New("center: this 3x-ui application already has a REALITY operation in progress")
	}
	excluded := []string{}
	rows, err := tx.QueryContext(ctx, `SELECT sni_hostname FROM publications WHERE gateway_node_id = ? AND kind = 'public_shared_443' AND status <> 'stopped' ORDER BY sni_hostname`, input.GatewayNodeID)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	for rows.Next() {
		var hostname string
		if err := rows.Scan(&hostname); err != nil {
			rows.Close()
			return ApplicationCommandView{}, err
		}
		excluded = append(excluded, hostname)
	}
	if err := rows.Close(); err != nil {
		return ApplicationCommandView{}, err
	}
	task := RealityCommandTask{Name: input.Name, ConnectHostname: input.Hostname, DNSProvider: input.DNSProvider, Target: input.Target, SNIHostname: input.SNIHostname, ExcludedSNI: excluded,
		TargetApplicationID: input.ApplicationID, TargetAddress: targetAddress, TargetPanelPort: targetSettings.PanelPort, TargetNodeID: targetNodeID}
	encoded, _ := json.Marshal(task)
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, input.ApplicationID, agentID, input.GatewayNodeID, realityCommandKind, encoded, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		return ApplicationCommandView{}, fmt.Errorf("center: create REALITY operation: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "queued", "3x-ui REALITY creation queued"); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}

func (s *Store) ApplicationCommand(ctx context.Context, id string) (ApplicationCommandView, error) {
	var value ApplicationCommandView
	var inputJSON, resultJSON []byte
	var secretID sql.NullString
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, application_id, gateway_node_id, kind, input_json, result_json, result_secret_id, state, error, created_at, updated_at FROM application_commands WHERE id = ?`, id).Scan(&value.ID, &value.ApplicationID, &value.GatewayNodeID, &value.Kind, &inputJSON, &resultJSON, &secretID, &value.State, &value.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return value, errors.New("center: application operation not found")
	}
	if err != nil {
		return value, err
	}
	switch value.Kind {
	case realityCommandKind:
		var input RealityCommandTask
		var result RealityCommandResult
		if json.Unmarshal(inputJSON, &input) != nil || json.Unmarshal(resultJSON, &result) != nil {
			return value, errors.New("center: stored application operation is invalid")
		}
		value.Hostname, value.DNSProvider, value.Target, value.SNIHostname = input.ConnectHostname, input.DNSProvider, result.Target, result.SNIHostname
		if value.Target == "" {
			value.Target, value.SNIHostname = input.Target, input.SNIHostname
		}
	case subscriptionCommandKind:
		var input SubscriptionCommandTask
		if json.Unmarshal(inputJSON, &input) != nil || input.Domain == "" || input.BaseURI == "" {
			return value, errors.New("center: stored subscription operation is invalid")
		}
		value.Hostname = input.Domain
		value.PublicationID = input.PublicationID
		publication, publicationErr := s.Publication(ctx, input.PublicationID)
		if publicationErr != nil {
			return value, errors.New("center: stored subscription publication is invalid")
		}
		value.DNSProvider = publication.DNSProvider
	case clientCommandKind:
		var input ThreeXUIClientCommandTask
		var result ThreeXUIClientCommandResult
		if json.Unmarshal(inputJSON, &input) != nil || input.Action == "" || json.Unmarshal(resultJSON, &result) != nil {
			return value, errors.New("center: stored 3x-ui client operation is invalid")
		}
		value.Action = input.Action
		value.Clients = result.Clients
		value.ClientsObserved = result.ClientsObserved
		value.Inbounds = result.Inbounds
		value.SubscriptionAvailable = input.SubscriptionBaseURI != ""
	case nodeCommandKind:
		var input ThreeXUINodeCommandTask
		if json.Unmarshal(inputJSON, &input) != nil || input.WorkerApplicationID == "" || (input.Action != "reconcile" && input.Action != "remove") {
			return value, errors.New("center: stored 3x-ui node operation is invalid")
		}
	case controllerCommandKind:
		var input ThreeXUIControllerCommandTask
		if json.Unmarshal(inputJSON, &input) != nil || input.ApplicationID == "" {
			return value, errors.New("center: stored 3x-ui controller operation is invalid")
		}
		value.Action = input.Action
	default:
		return value, errors.New("center: stored application operation kind is invalid")
	}
	value.ResultAvailable = secretID.Valid
	value.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if value.Kind == realityCommandKind {
		var publicationID sql.NullString
		_ = s.db.QueryRowContext(ctx, `SELECT p.id FROM publications p JOIN services sv ON sv.id = p.service_id WHERE sv.application_id = ? AND p.kind = 'public_shared_443' AND p.hostname = ? AND p.status <> 'stopped' ORDER BY p.updated_at DESC LIMIT 1`, value.ApplicationID, value.Hostname).Scan(&publicationID)
		value.PublicationID = publicationID.String
	}
	return value, nil
}

func (s *Store) LatestApplicationCommand(ctx context.Context, applicationID, kind string) (ApplicationCommandView, error) {
	if kind != realityCommandKind && kind != subscriptionCommandKind && kind != clientCommandKind && kind != nodeCommandKind && kind != controllerCommandKind {
		return ApplicationCommandView{}, errors.New("center: unsupported application operation kind")
	}
	var id string
	condition := `(state IN ('pending', 'running') OR result_secret_id IS NOT NULL)`
	if kind == subscriptionCommandKind {
		condition = `(state IN ('pending', 'running', 'failed') OR (state = 'succeeded' AND EXISTS (
			SELECT 1 FROM publications p JOIN services s ON s.id = p.service_id
			WHERE s.application_id = application_commands.application_id AND p.status <> 'stopped' AND s.name = 'subscription'
		)))`
	}
	err := s.db.QueryRowContext(ctx, `SELECT id FROM application_commands WHERE application_id = ? AND kind = ? AND `+condition+` ORDER BY created_at DESC, rowid DESC LIMIT 1`, strings.TrimSpace(applicationID), kind).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: resumable application operation not found")
	}
	if err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}

func (s *Store) ConsumeApplicationCommandResult(ctx context.Context, id string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var secretID string
	var sealed []byte
	if err := tx.QueryRowContext(ctx, `SELECT c.result_secret_id, s.sealed FROM application_commands c JOIN secrets s ON s.id = c.result_secret_id WHERE c.id = ? AND c.state = 'succeeded'`, id).Scan(&secretID, &sealed); errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("center: one-time application result is unavailable")
	} else if err != nil {
		return "", err
	}
	plain, err := secret.Open(s.key, sealed, []byte("application-command:"+id))
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE application_commands SET result_secret_id = NULL WHERE id = ?`, id); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM secrets WHERE id = ?`, secretID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return string(plain), nil
}
