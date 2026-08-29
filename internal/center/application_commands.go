package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/secret"
)

const (
	centerThreeXUIRealityPortFirst = 20000
	threeXUIRealityGuardPortFirst  = 21000
	realityCommandKind             = "3xui.reality.create"
	realityVerifyCommandKind       = "3xui.reality.verify"
	realityHardenCommandKind       = "3xui.reality.harden"
	realityRenameCommandKind       = "3xui.reality.rename"
	subscriptionCommandKind        = "3xui.subscription.configure"
	clientCommandKind              = "3xui.clients.manage"
	nodeCommandKind                = "3xui.node.reconcile"
	controllerCommandKind          = "3xui.controller.manage"
)

type RealityCommandInput struct {
	ApplicationID     string `json:"applicationId"`
	RegionCode        string `json:"regionCode"`
	Name              string `json:"name"`
	ClientName        string `json:"clientName"`
	GatewayNodeID     string `json:"gatewayNodeId"`
	Hostname          string `json:"hostname"`
	DNSProvider       string `json:"dnsProvider"`
	TargetHost        string `json:"targetHost"`
	ServerName        string `json:"serverName"`
	InboundTotalBytes int64  `json:"inboundTotalBytes"`
	InboundResetDays  int    `json:"inboundResetDays"`
	ClientTotalBytes  int64  `json:"clientTotalBytes"`
	ClientResetDays   int    `json:"clientResetDays"`
	ClientExpiryTime  int64  `json:"clientExpiryTime"`
}

type RealityRenameCommandInput struct {
	ServiceID  string `json:"serviceId"`
	RegionCode string `json:"regionCode"`
	Name       string `json:"name"`
}

type RealityTargetVerifyInput struct {
	TargetHost string `json:"targetHost"`
	ServerName string `json:"serverName"`
}

type RealityCommandTask struct {
	Action              string   `json:"action"`
	RegionCode          string   `json:"regionCode"`
	DisplayName         string   `json:"displayName"`
	ClientName          string   `json:"clientName,omitempty"`
	InboundID           int      `json:"inboundId,omitempty"`
	ConnectHostname     string   `json:"connectHostname"`
	DNSProvider         string   `json:"dnsProvider"`
	TargetHost          string   `json:"targetHost,omitempty"`
	ServerName          string   `json:"serverName,omitempty"`
	ExcludedSNI         []string `json:"excludedSni,omitempty"`
	TargetApplicationID string   `json:"targetApplicationId"`
	TargetAddress       string   `json:"targetAddress"`
	TargetPublicAddress string   `json:"targetPublicAddress"`
	TargetPanelPort     int      `json:"targetPanelPort"`
	TargetNodeID        int      `json:"targetNodeId,omitempty"`
	TargetAPIToken      string   `json:"targetApiToken,omitempty"`
	CreateInitialClient bool     `json:"createInitialClient"`
	InboundTag          string   `json:"inboundTag"`
	InboundTotalBytes   int64    `json:"inboundTotalBytes"`
	InboundResetDays    int      `json:"inboundResetDays"`
	ClientTotalBytes    int64    `json:"clientTotalBytes"`
	ClientResetDays     int      `json:"clientResetDays"`
	ClientExpiryTime    int64    `json:"clientExpiryTime"`
	ServiceID           string   `json:"serviceId,omitempty"`
	GuardRevision       int64    `json:"guardRevision,omitempty"`
}

type RealityCommandResult struct {
	Action             string `json:"action"`
	InboundID          int    `json:"inboundId"`
	DisplayName        string `json:"displayName"`
	ClientName         string `json:"clientName,omitempty"`
	Listen             string `json:"listen"`
	Port               int    `json:"port"`
	TargetHost         string `json:"targetHost"`
	TargetIP           string `json:"targetIp"`
	ServerName         string `json:"serverName"`
	NodeASN            int64  `json:"nodeAsn"`
	TargetASN          int64  `json:"targetAsn"`
	CDNProvider        string `json:"cdnProvider,omitempty"`
	TLS13              bool   `json:"tls13"`
	X25519             bool   `json:"x25519"`
	HTTP2              bool   `json:"http2"`
	CertificateValid   bool   `json:"certificateValid"`
	CompanionInboundID int    `json:"companionInboundId,omitempty"`
	CompanionTag       string `json:"companionTag,omitempty"`
	CompanionPort      int    `json:"companionPort,omitempty"`
	GuardStatus        string `json:"guardStatus"`
	ConnectHostname    string `json:"connectHostname"`
	ShareURI           string `json:"shareUri"`
	InboundTag         string `json:"inboundTag"`
	ClientCreated      bool   `json:"clientCreated"`
	InboundTotalBytes  int64  `json:"inboundTotalBytes"`
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
	ApplicationID     string `json:"applicationId"`
	Action            string `json:"action"`
	Email             string `json:"email,omitempty"`
	NewEmail          string `json:"newEmail,omitempty"`
	InboundID         int    `json:"inboundId,omitempty"`
	InboundIDs        []int  `json:"inboundIds,omitempty"`
	Enabled           bool   `json:"enabled"`
	TotalBytes        int64  `json:"totalBytes"`
	ResetDays         int    `json:"resetDays"`
	ExpiryTime        int64  `json:"expiryTime"`
	LimitIP           int    `json:"limitIp"`
	ServiceID         string `json:"serviceId,omitempty"`
	InboundTotalBytes int64  `json:"inboundTotalBytes"`
	InboundResetDays  int    `json:"inboundResetDays"`
}

type ThreeXUIClientInbound struct {
	ID              int    `json:"id"`
	ServiceID       string `json:"serviceId"`
	Name            string `json:"name"`
	DisplayName     string `json:"displayName,omitempty"`
	ApplicationID   string `json:"applicationId"`
	NodeID          string `json:"nodeId"`
	NodeName        string `json:"nodeName"`
	ConnectHostname string `json:"connectHostname,omitempty"`
	SNIHostname     string `json:"sniHostname,omitempty"`
	Enabled         bool   `json:"enabled"`
	TotalBytes      int64  `json:"totalBytes"`
	UsedBytes       int64  `json:"usedBytes"`
	ResetDays       int    `json:"resetDays"`
	NextResetAt     string `json:"nextResetAt,omitempty"`
	PlanStatus      string `json:"planStatus"`
	PlanError       string `json:"planError,omitempty"`
	PlanRevision    int64  `json:"-"`
	InboundTag      string `json:"inboundTag,omitempty"`
}

type ThreeXUIClientCommandTask struct {
	Action              string                  `json:"action"`
	Email               string                  `json:"email,omitempty"`
	NewEmail            string                  `json:"newEmail,omitempty"`
	InboundID           int                     `json:"inboundId,omitempty"`
	InboundIDs          []int                   `json:"inboundIds,omitempty"`
	Enabled             bool                    `json:"enabled"`
	TotalBytes          int64                   `json:"totalBytes"`
	ResetDays           int                     `json:"resetDays"`
	ExpiryTime          int64                   `json:"expiryTime"`
	LimitIP             int                     `json:"limitIp"`
	ServiceID           string                  `json:"serviceId,omitempty"`
	InboundTotalBytes   int64                   `json:"inboundTotalBytes"`
	InboundResetDays    int                     `json:"inboundResetDays"`
	ExpectedNextResetAt string                  `json:"expectedNextResetAt,omitempty"`
	PlanRevision        int64                   `json:"planRevision,omitempty"`
	OperationKey        string                  `json:"operationKey,omitempty"`
	InboundTag          string                  `json:"inboundTag,omitempty"`
	TargetApplicationID string                  `json:"targetApplicationId,omitempty"`
	TargetAddress       string                  `json:"targetAddress,omitempty"`
	TargetPanelPort     int                     `json:"targetPanelPort,omitempty"`
	TargetNodeID        int                     `json:"targetNodeId,omitempty"`
	TargetAPIToken      string                  `json:"targetApiToken,omitempty"`
	Inbounds            []ThreeXUIClientInbound `json:"inbounds"`
	SubscriptionBaseURI string                  `json:"subscriptionBaseUri,omitempty"`
}

type ThreeXUIClientView struct {
	Email           string `json:"email"`
	Enabled         bool   `json:"enabled"`
	TotalBytes      int64  `json:"totalBytes"`
	UsedBytes       int64  `json:"usedBytes"`
	ResetDays       int    `json:"resetDays"`
	ExpiryTime      int64  `json:"expiryTime"`
	LimitIP         int    `json:"limitIp"`
	InboundIDs      []int  `json:"inboundIds"`
	HasSubscription bool   `json:"hasSubscription"`
}

type ThreeXUIClientCommandResult struct {
	Clients          []ThreeXUIClientView    `json:"clients,omitempty"`
	ClientsObserved  bool                    `json:"clientsObserved"`
	Inbounds         []ThreeXUIClientInbound `json:"inbounds"`
	InboundsObserved bool                    `json:"inboundsObserved"`
	Secret           string                  `json:"secret,omitempty"`
	SecretKind       string                  `json:"secretKind,omitempty"`
}

type ThreeXUINodeCommandTask struct {
	Action              string `json:"action"`
	MigrationID         string `json:"migrationId,omitempty"`
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
	ID                     string                  `json:"id"`
	ApplicationID          string                  `json:"applicationId"`
	GatewayNodeID          string                  `json:"gatewayNodeId"`
	Kind                   string                  `json:"kind"`
	State                  string                  `json:"state"`
	ReconciliationRequired bool                    `json:"reconciliationRequired"`
	Hostname               string                  `json:"hostname"`
	DNSProvider            string                  `json:"dnsProvider"`
	TargetHost             string                  `json:"targetHost,omitempty"`
	TargetIP               string                  `json:"targetIp,omitempty"`
	ServerName             string                  `json:"serverName,omitempty"`
	NodeASN                int64                   `json:"nodeAsn,omitempty"`
	TargetASN              int64                   `json:"targetAsn,omitempty"`
	CDNProvider            string                  `json:"cdnProvider,omitempty"`
	TLS13                  bool                    `json:"tls13,omitempty"`
	X25519                 bool                    `json:"x25519,omitempty"`
	HTTP2                  bool                    `json:"h2,omitempty"`
	CertificateValid       bool                    `json:"certificateValid,omitempty"`
	GuardStatus            string                  `json:"guardStatus,omitempty"`
	PublicationID          string                  `json:"publicationId,omitempty"`
	Action                 string                  `json:"action,omitempty"`
	RegionCode             string                  `json:"regionCode,omitempty"`
	DisplayName            string                  `json:"displayName,omitempty"`
	InboundID              int                     `json:"inboundId,omitempty"`
	ClientCreated          bool                    `json:"clientCreated,omitempty"`
	InboundTotalBytes      int64                   `json:"inboundTotalBytes,omitempty"`
	InboundResetDays       int                     `json:"inboundResetDays,omitempty"`
	InboundNextResetAt     string                  `json:"inboundNextResetAt,omitempty"`
	Clients                []ThreeXUIClientView    `json:"clients,omitempty"`
	ClientsObserved        bool                    `json:"clientsObserved,omitempty"`
	Inbounds               []ThreeXUIClientInbound `json:"inbounds,omitempty"`
	InboundsObserved       bool                    `json:"inboundsObserved,omitempty"`
	SubscriptionAvailable  bool                    `json:"subscriptionAvailable,omitempty"`
	Error                  string                  `json:"error,omitempty"`
	ResultAvailable        bool                    `json:"resultAvailable"`
	CreatedAt              time.Time               `json:"createdAt"`
	UpdatedAt              time.Time               `json:"updatedAt"`
}

func normalizeRealityCommandInput(input RealityCommandInput) (RealityCommandInput, string, error) {
	input.ApplicationID = strings.TrimSpace(input.ApplicationID)
	region, name, displayName, err := composeRealityDisplayName(input.RegionCode, input.Name)
	if err != nil {
		return input, "", err
	}
	input.RegionCode = region
	input.Name = name
	input.ClientName = strings.TrimSpace(input.ClientName)
	input.GatewayNodeID = strings.TrimSpace(input.GatewayNodeID)
	input.Hostname = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.Hostname), "."))
	input.DNSProvider = strings.TrimSpace(input.DNSProvider)
	input.TargetHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.TargetHost), "."))
	input.ServerName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.ServerName), "."))
	if input.InboundTotalBytes < 0 || input.InboundResetDays < 0 || input.InboundResetDays > maxThreeXUIResetDays || input.ClientTotalBytes < 0 || input.ClientResetDays < 0 || input.ClientResetDays > maxThreeXUIResetDays || input.ClientExpiryTime < 0 {
		return input, "", errors.New("center: REALITY node or subscription traffic plan is invalid")
	}
	if input.ApplicationID == "" || input.GatewayNodeID == "" || !domainSuffixPattern.MatchString(input.Hostname) {
		return input, "", errors.New("center: application, region, node name, gateway, and a valid connection hostname are required")
	}
	if input.DNSProvider != "manual" && input.DNSProvider != "cloudflare" {
		return input, "", errors.New("center: REALITY DNS must be manual or Cloudflare")
	}
	if !domainSuffixPattern.MatchString(input.TargetHost) || !domainSuffixPattern.MatchString(input.ServerName) {
		return input, "", errors.New("center: REALITY targetHost and serverName are required valid hostnames; target port is fixed to 443")
	}
	return input, displayName, nil
}

func validateRealityCommandResult(input RealityCommandTask, result RealityCommandResult) error {
	expectedTag := input.InboundTag
	if input.TargetNodeID > 0 {
		expectedTag = "n" + strconv.Itoa(input.TargetNodeID) + "-" + input.InboundTag
	}
	if result.Action != "create" || result.InboundID < 1 || result.DisplayName != input.DisplayName || result.ClientName != input.ClientName || (result.InboundTag != input.InboundTag && result.InboundTag != expectedTag) || net.ParseIP(result.Listen) == nil || result.Listen != input.TargetAddress || result.Port < centerThreeXUIRealityPortFirst || result.Port > centerThreeXUIRealityPortFirst+31 || result.ConnectHostname != input.ConnectHostname || !domainSuffixPattern.MatchString(result.ServerName) || result.InboundTotalBytes != input.InboundTotalBytes {
		return errors.New("center: Agent returned an unsafe REALITY result")
	}
	expectedCompanionTag := input.InboundTag + "-guard"
	if input.TargetNodeID > 0 {
		expectedCompanionTag = "n" + strconv.Itoa(input.TargetNodeID) + "-" + expectedCompanionTag
	}
	if result.TargetHost != input.TargetHost || result.ServerName != input.ServerName || net.ParseIP(result.TargetIP) == nil || result.NodeASN <= 0 || result.TargetASN <= 0 || result.NodeASN != result.TargetASN || result.CDNProvider != "" || !result.TLS13 || !result.X25519 || !result.HTTP2 || !result.CertificateValid || result.CompanionInboundID < 1 || (result.CompanionTag != input.InboundTag+"-guard" && result.CompanionTag != expectedCompanionTag) || result.CompanionPort != 21000+(result.Port-centerThreeXUIRealityPortFirst) || result.GuardStatus != "ready" {
		return errors.New("center: Agent returned an invalid REALITY target")
	}
	if result.ClientCreated != input.CreateInitialClient {
		return errors.New("center: Agent changed the requested initial subscription client operation")
	}
	if !result.ClientCreated {
		if strings.TrimSpace(result.ShareURI) != "" {
			return errors.New("center: Agent returned an unexpected REALITY client link")
		}
		return nil
	}
	share, err := url.Parse(strings.TrimSpace(result.ShareURI))
	if err != nil || share.Scheme != "vless" || share.User == nil || share.User.Username() == "" || share.Hostname() != input.ConnectHostname || share.Port() != "443" || share.Fragment != input.DisplayName {
		return errors.New("center: Agent returned an invalid REALITY client link")
	}
	if _, hasPassword := share.User.Password(); hasPassword {
		return errors.New("center: Agent returned an invalid REALITY client link")
	}
	query := share.Query()
	if query.Get("type") != "tcp" || query.Get("security") != "reality" || query.Get("flow") != "xtls-rprx-vision" || query.Get("sni") != result.ServerName || query.Get("pbk") == "" || query.Get("sid") == "" {
		return errors.New("center: Agent returned an invalid REALITY client link")
	}
	return nil
}

func (s *Store) VerifyRealityTarget(ctx context.Context, applicationID string, input RealityTargetVerifyInput) (ApplicationCommandView, error) {
	applicationID = strings.TrimSpace(applicationID)
	input.TargetHost = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.TargetHost), "."))
	input.ServerName = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(input.ServerName), "."))
	if applicationID == "" || !domainSuffixPattern.MatchString(input.TargetHost) || !domainSuffixPattern.MatchString(input.ServerName) {
		return ApplicationCommandView{}, errors.New("center: application, targetHost, and serverName are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	var targetAgentID, siteID, appKey, status, role, targetAddress, targetPublicAddress string
	if err := tx.QueryRowContext(ctx, `SELECT application.node_id, application.site_id, application.app_key, application.status, application.role,
		COALESCE(profile.service_address, ''), COALESCE(profile.public_address, '')
		FROM applications application LEFT JOIN agent_network_profiles profile ON profile.agent_id = application.node_id
		WHERE application.id = ?`, applicationID).Scan(&targetAgentID, &siteID, &appKey, &status, &role, &targetAddress, &targetPublicAddress); errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: application not found")
	} else if err != nil {
		return ApplicationCommandView{}, err
	}
	if appKey != threeXUIAppKey || status != "running" || (role != threeXUIRoleMaster && role != threeXUIRoleWorker) {
		return ApplicationCommandView{}, errors.New("center: REALITY target verification requires a running official 3x-ui application")
	}
	if !networking.IsPrivateServiceAddress(targetAddress) || net.ParseIP(targetPublicAddress) == nil {
		return ApplicationCommandView{}, errors.New("center: target VLESS node needs confirmed private service and public addresses")
	}
	agentID := targetAgentID
	targetNodeID := 0
	if role == threeXUIRoleWorker {
		if err := tx.QueryRowContext(ctx, `SELECT master.node_id, node.remote_node_id
			FROM three_x_ui_nodes node JOIN applications master ON master.id = node.master_application_id
			WHERE node.worker_application_id = ? AND node.status = 'ready' AND master.status = 'running'`, applicationID).Scan(&agentID, &targetNodeID); err != nil {
			return ApplicationCommandView{}, errors.New("center: this VLESS node is not connected to the Site 3x-ui controller")
		}
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, controllerCommandKind).Scan(&active); err != nil {
		return ApplicationCommandView{}, err
	}
	if active != 0 {
		return ApplicationCommandView{}, errors.New("center: this 3x-ui controller already has an operation in progress")
	}
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	task := RealityCommandTask{Action: "verify", TargetHost: input.TargetHost, ServerName: input.ServerName,
		TargetApplicationID: applicationID, TargetAddress: targetAddress, TargetPublicAddress: targetPublicAddress, TargetNodeID: targetNodeID}
	encoded, _ := json.Marshal(task)
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, site_id, display_name, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at)
		VALUES(?, ?, ?, '', ?, ?, ?, ?, 'pending', ?, ?)`, id, applicationID, siteID, agentID, agentID, realityVerifyCommandKind, encoded, now, now); err != nil {
		return ApplicationCommandView{}, fmt.Errorf("center: queue REALITY target verification: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "queued", "REALITY target verification queued"); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}

func (s *Store) CreateRealityCommand(ctx context.Context, input RealityCommandInput) (ApplicationCommandView, error) {
	var err error
	var displayName string
	input, displayName, err = normalizeRealityCommandInput(input)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	var targetAgentID, siteID, appKey, status, role, targetAddress, targetPublicAddress string
	if err := tx.QueryRowContext(ctx, `SELECT a.node_id, a.site_id, a.app_key, a.status, a.role,
		COALESCE(p.service_address, ''), COALESCE(p.public_address, '')
		FROM applications a LEFT JOIN agent_network_profiles p ON p.agent_id = a.node_id WHERE a.id = ?`, input.ApplicationID).Scan(&targetAgentID, &siteID, &appKey, &status, &role, &targetAddress, &targetPublicAddress); errors.Is(err, sql.ErrNoRows) {
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
	if !networking.IsPrivateServiceAddress(targetAddress) {
		return ApplicationCommandView{}, errors.New("center: target VLESS node has no confirmed private service address")
	}
	if net.ParseIP(targetPublicAddress) == nil {
		return ApplicationCommandView{}, errors.New("center: target VLESS node has no confirmed public address for ASN validation")
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
	var duplicateDisplayName int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE site_id = ? AND app_protocol = 'vless/tcp/reality' AND status <> 'stopped' AND display_name = ? COLLATE NOCASE`, siteID, displayName).Scan(&duplicateDisplayName); err != nil {
		return ApplicationCommandView{}, err
	}
	if duplicateDisplayName != 0 {
		return ApplicationCommandView{}, errors.New("center: this Site already has a REALITY node with that display name")
	}
	if err := ensureRealityDisplayNameUnreserved(ctx, tx, siteID, displayName); err != nil {
		return ApplicationCommandView{}, err
	}
	var existingControllerRealityServices int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE application_id = ? AND app_protocol = 'vless/tcp/reality' AND status <> 'stopped'`, input.ApplicationID).Scan(&existingControllerRealityServices); err != nil {
		return ApplicationCommandView{}, err
	}
	createInitialClient := role == threeXUIRoleMaster && existingControllerRealityServices == 0
	if createInitialClient && !validThreeXUIClientName(input.ClientName) {
		return ApplicationCommandView{}, errors.New("center: the first REALITY node requires a valid subscription client name")
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, controllerCommandKind).Scan(&active); err != nil {
		return ApplicationCommandView{}, err
	}
	if active != 0 {
		return ApplicationCommandView{}, errors.New("center: this 3x-ui controller already has an operation in progress")
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
	if createInitialClient && input.ClientResetDays > 0 && input.ClientExpiryTime <= s.now().UTC().UnixMilli() {
		return ApplicationCommandView{}, errors.New("center: a renewable subscription traffic plan requires a future expiry time")
	}
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	task := RealityCommandTask{Action: "create", RegionCode: input.RegionCode, DisplayName: displayName, ConnectHostname: input.Hostname, DNSProvider: input.DNSProvider, TargetHost: input.TargetHost, ServerName: input.ServerName, ExcludedSNI: excluded,
		TargetApplicationID: input.ApplicationID, TargetAddress: targetAddress, TargetPublicAddress: targetPublicAddress, TargetPanelPort: targetSettings.PanelPort, TargetNodeID: targetNodeID,
		CreateInitialClient: createInitialClient,
		InboundTag:          realityCommandInboundTag(id),
		InboundTotalBytes:   input.InboundTotalBytes, InboundResetDays: input.InboundResetDays}
	if createInitialClient {
		task.ClientName = input.ClientName
		task.ClientTotalBytes = input.ClientTotalBytes
		task.ClientResetDays = input.ClientResetDays
		task.ClientExpiryTime = input.ClientExpiryTime
	}
	encoded, _ := json.Marshal(task)
	now := s.now().UTC()
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, site_id, display_name, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, input.ApplicationID, siteID, displayName, agentID, input.GatewayNodeID, realityCommandKind, encoded, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		if strings.Contains(err.Error(), "application_commands.site_id") {
			return ApplicationCommandView{}, errors.New("center: this Site already has a REALITY operation reserving that node name")
		}
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

func realityCommandInboundTag(commandID string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(commandID)))
	return "vastora-" + hex.EncodeToString(digest[:12])
}

func (s *Store) CreateRealityRenameCommand(ctx context.Context, input RealityRenameCommandInput) (ApplicationCommandView, error) {
	input.ServiceID = strings.TrimSpace(input.ServiceID)
	region, name, displayName, err := composeRealityDisplayName(input.RegionCode, input.Name)
	if err != nil || input.ServiceID == "" {
		return ApplicationCommandView{}, errors.New("center: REALITY service, region, and a valid node name are required")
	}
	input.RegionCode, input.Name = region, name
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	defer tx.Rollback()
	var applicationID, siteID, serviceName, serviceStatus, appProtocol, targetAgentID, appKey, applicationStatus, role string
	if err := tx.QueryRowContext(ctx, `SELECT s.application_id, s.site_id, s.name, s.status, s.app_protocol, a.node_id, a.app_key, a.status, a.role
		FROM services s JOIN applications a ON a.id = s.application_id WHERE s.id = ?`, input.ServiceID).Scan(&applicationID, &siteID, &serviceName, &serviceStatus, &appProtocol, &targetAgentID, &appKey, &applicationStatus, &role); errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, errors.New("center: REALITY service not found")
	} else if err != nil {
		return ApplicationCommandView{}, err
	}
	if appKey != threeXUIAppKey || applicationStatus != "running" || appProtocol != "vless/tcp/reality" || (serviceStatus != "running" && serviceStatus != "ready" && serviceStatus != "degraded") || !strings.HasPrefix(serviceName, "inbound-") {
		return ApplicationCommandView{}, errors.New("center: only an active official 3x-ui REALITY node can be renamed")
	}
	inboundID, err := strconv.Atoi(strings.TrimPrefix(serviceName, "inbound-"))
	if err != nil || inboundID < 1 {
		return ApplicationCommandView{}, errors.New("center: REALITY service has an invalid inbound identifier")
	}
	agentID := targetAgentID
	targetNodeID := 0
	if role == threeXUIRoleWorker {
		if err := tx.QueryRowContext(ctx, `SELECT master.node_id, n.remote_node_id
			FROM three_x_ui_nodes n JOIN applications master ON master.id = n.master_application_id
			WHERE n.worker_application_id = ? AND n.status = 'ready' AND master.status = 'running'`, applicationID).Scan(&agentID, &targetNodeID); errors.Is(err, sql.ErrNoRows) {
			return ApplicationCommandView{}, errors.New("center: this VLESS node is not connected to the Site 3x-ui controller")
		} else if err != nil {
			return ApplicationCommandView{}, err
		}
	} else if role != threeXUIRoleMaster {
		return ApplicationCommandView{}, errors.New("center: 3x-ui topology role is not configured")
	}
	var duplicateDisplayName int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM services WHERE site_id = ? AND id <> ? AND app_protocol = 'vless/tcp/reality' AND status <> 'stopped' AND display_name = ? COLLATE NOCASE`, siteID, input.ServiceID, displayName).Scan(&duplicateDisplayName); err != nil {
		return ApplicationCommandView{}, err
	}
	if duplicateDisplayName != 0 {
		return ApplicationCommandView{}, errors.New("center: this Site already has a REALITY node with that display name")
	}
	if err := ensureRealityDisplayNameUnreserved(ctx, tx, siteID, displayName); err != nil {
		return ApplicationCommandView{}, err
	}
	var connectHostname, sniHostname string
	err = tx.QueryRowContext(ctx, `SELECT hostname, sni_hostname FROM publications WHERE service_id = ? AND kind = 'public_shared_443' AND status <> 'stopped' ORDER BY updated_at DESC LIMIT 1`, input.ServiceID).Scan(&connectHostname, &sniHostname)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ApplicationCommandView{}, err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands WHERE agent_id = ? AND kind <> ? AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, agentID, controllerCommandKind).Scan(&active); err != nil {
		return ApplicationCommandView{}, err
	}
	if active != 0 {
		return ApplicationCommandView{}, errors.New("center: this 3x-ui controller already has an operation in progress")
	}
	task := RealityCommandTask{Action: "rename", RegionCode: input.RegionCode, DisplayName: displayName, InboundID: inboundID, ConnectHostname: connectHostname, ServerName: sniHostname, TargetApplicationID: applicationID, TargetNodeID: targetNodeID}
	encoded, _ := json.Marshal(task)
	token, err := randomToken(18)
	if err != nil {
		return ApplicationCommandView{}, err
	}
	id := "application-command-" + token
	now := s.now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `INSERT INTO application_commands(id, application_id, site_id, display_name, agent_id, gateway_node_id, kind, input_json, state, created_at, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`, id, applicationID, siteID, displayName, agentID, agentID, realityRenameCommandKind, encoded, now, now); err != nil {
		if strings.Contains(err.Error(), "application_commands.site_id") {
			return ApplicationCommandView{}, errors.New("center: this Site already has a REALITY operation reserving that node name")
		}
		return ApplicationCommandView{}, fmt.Errorf("center: rename REALITY node: %w", err)
	}
	if err := s.recordTaskEvent(ctx, tx, id, agentID, "application.command", 1, "queued", "3x-ui REALITY node rename queued"); err != nil {
		return ApplicationCommandView{}, err
	}
	if err := tx.Commit(); err != nil {
		return ApplicationCommandView{}, err
	}
	return s.ApplicationCommand(ctx, id)
}

func ensureRealityDisplayNameUnreserved(ctx context.Context, tx *sql.Tx, siteID, displayName string) error {
	var reserved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM application_commands
		WHERE site_id = ? AND display_name = ? COLLATE NOCASE
		AND kind IN (?, ?) AND (state IN ('pending', 'running') OR reconciliation_required = 1)`, siteID, displayName, realityCommandKind, realityRenameCommandKind).Scan(&reserved); err != nil {
		return err
	}
	if reserved != 0 {
		return errors.New("center: this Site already has a REALITY operation reserving that node name")
	}
	return nil
}

func (s *Store) ApplicationCommand(ctx context.Context, id string) (ApplicationCommandView, error) {
	var value ApplicationCommandView
	var inputJSON, resultJSON []byte
	var secretID sql.NullString
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT id, application_id, gateway_node_id, kind, input_json, result_json, result_secret_id, state, reconciliation_required, error, created_at, updated_at FROM application_commands WHERE id = ?`, id).Scan(&value.ID, &value.ApplicationID, &value.GatewayNodeID, &value.Kind, &inputJSON, &resultJSON, &secretID, &value.State, &value.ReconciliationRequired, &value.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return value, errors.New("center: application operation not found")
	}
	if err != nil {
		return value, err
	}
	switch value.Kind {
	case realityCommandKind, realityVerifyCommandKind, realityHardenCommandKind, realityRenameCommandKind:
		var input RealityCommandTask
		var result RealityCommandResult
		if json.Unmarshal(inputJSON, &input) != nil || json.Unmarshal(resultJSON, &result) != nil {
			return value, errors.New("center: stored application operation is invalid")
		}
		value.Action, value.RegionCode, value.DisplayName, value.InboundID = input.Action, input.RegionCode, input.DisplayName, input.InboundID
		value.InboundTotalBytes = input.InboundTotalBytes
		value.InboundResetDays = input.InboundResetDays
		if result.InboundID > 0 {
			value.InboundID = result.InboundID
			value.ClientCreated = result.ClientCreated
			value.InboundTotalBytes = result.InboundTotalBytes
			var nextResetAt string
			_ = s.db.QueryRowContext(ctx, `SELECT plan.next_reset_at FROM services service JOIN three_x_ui_inbound_plans plan ON plan.service_id = service.id WHERE service.application_id = ? AND service.name = ?`, value.ApplicationID, fmt.Sprintf("inbound-%d", result.InboundID)).Scan(&nextResetAt)
			value.InboundNextResetAt = nextResetAt
		}
		value.Hostname, value.DNSProvider = input.ConnectHostname, input.DNSProvider
		value.TargetHost, value.TargetIP, value.ServerName = result.TargetHost, result.TargetIP, result.ServerName
		value.NodeASN, value.TargetASN, value.CDNProvider, value.GuardStatus = result.NodeASN, result.TargetASN, result.CDNProvider, result.GuardStatus
		value.TLS13, value.X25519, value.HTTP2, value.CertificateValid = result.TLS13, result.X25519, result.HTTP2, result.CertificateValid
		if value.Kind == realityRenameCommandKind {
			value.DNSProvider = "manual"
		}
		if value.TargetHost == "" {
			value.TargetHost, value.ServerName = input.TargetHost, input.ServerName
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
		value.InboundsObserved = result.InboundsObserved
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
	if kind != realityCommandKind && kind != realityVerifyCommandKind && kind != realityHardenCommandKind && kind != realityRenameCommandKind && kind != subscriptionCommandKind && kind != clientCommandKind && kind != nodeCommandKind && kind != controllerCommandKind {
		return ApplicationCommandView{}, errors.New("center: unsupported application operation kind")
	}
	var id string
	condition := `(state IN ('pending', 'running') OR reconciliation_required = 1 OR result_secret_id IS NOT NULL)`
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
