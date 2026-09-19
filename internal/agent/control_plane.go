package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/ipquality"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/nodediagnostics"
	"github.com/petauron/vastora/internal/nodeprotocol"
	"github.com/petauron/vastora/internal/pulse"
	"github.com/petauron/vastora/internal/realitytarget"
)

var Version = "0.1.0-dev"

const (
	taskLeaseRenewInterval = time.Minute
	taskControlTimeout     = 15 * time.Second
)

type Client struct {
	executionAbort     func(error)
	executionSession   string
	execution          controlplane.ExecutionAuthorization
	HTTPClient         *http.Client
	Executor           Executor
	Roles              []string
	Capabilities       Capabilities
	GatewayDriver      GatewayDriver
	GatewayProvisioner GatewayProvisioner
	NodeListener       NodeListenerProvisioner
	TunnelProvisioner  TunnelProvisioner
	Decommissioner     HostDecommissioner
	Updater            HostUpdater
	TailscaleIsolation func(context.Context, TailscaleIsolationDesiredState) error
	TailscaleEnrolled  bool
	TailscaleOwnership string
	PublicEgress       PublicEgressObserver
	LandingServer      LandingServerProvisioner
}

type TailscaleIsolationDesiredState struct {
	ControlURL        string   `json:"controlUrl"`
	ControlAddresses  []string `json:"controlAddresses"`
	ControlAliases    []string `json:"controlAliases,omitempty"`
	StaticEndpoints   []string `json:"staticEndpoints"`
	RelayRegionID     int      `json:"relayRegionId,omitempty"`
	STUNOnlyRegionIDs []int    `json:"stunOnlyRegionIds,omitempty"`
}

type HostDecommissioner interface {
	ScheduleFinalRemoval(context.Context, HostDecommissionRequest) error
}

type HostDecommissionRequest struct {
	ExecutionID   string
	SessionID     string
	TaskID        string
	Attempt       int64
	DeleteData    bool
	CallbackURL   string
	CallbackToken string
	Connection    Connection
}

type HostUpdater interface {
	ScheduleUpdate(context.Context, HostUpdateRequest) error
}

type HostUpdateRequest struct {
	ExecutionID   string
	SessionID     string
	TaskID        string
	Attempt       int64
	TargetVersion string
	Connection    Connection
}

// uncertainTaskOutcomeError records possible partial effects. Every occurrence
// requires explicit disposition; attempts never authorize retries or rollback.
type uncertainTaskOutcomeError struct{ cause error }

type centerResponseError struct {
	status  int
	message string
}

func (e *centerResponseError) Error() string { return "agent: Center request failed: " + e.message }

func (e *uncertainTaskOutcomeError) Error() string { return e.cause.Error() }
func (e *uncertainTaskOutcomeError) Unwrap() error { return e.cause }

func uncertainTaskOutcome(cause error) error {
	if cause == nil {
		return nil
	}
	return &uncertainTaskOutcomeError{cause: cause}
}

func taskOutcomeIsUncertain(err error) bool {
	var uncertain *uncertainTaskOutcomeError
	return errors.As(err, &uncertain)
}

type Capabilities struct {
	IPQuality            bool `json:"ipQuality"`
	NetworkDiagnostics   bool `json:"networkDiagnostics"`
	ReturnRoute          bool `json:"returnRoute"`
	BandwidthDiagnostics bool `json:"bandwidthDiagnostics"`
	HostProfile          bool `json:"hostProfile"`
	Docker               bool `json:"docker"`
	Gateway              bool `json:"gateway"`
	Tunnel               bool `json:"tunnel"`
	Metrics              bool `json:"metrics"`
	Logs                 bool `json:"logs"`
}

type Enrollment struct {
	ID           string       `json:"id"`
	Credential   string       `json:"credential"`
	Name         string       `json:"name"`
	Roles        []string     `json:"roles"`
	Capabilities Capabilities `json:"capabilities"`
}

type DeploymentTask struct {
	IPQuality                 *ipquality.Task                     `json:"ipQuality,omitempty"`
	NodeDiagnostics           *nodediagnostics.Task               `json:"nodeDiagnostics,omitempty"`
	Authorization             controlplane.ExecutionAuthorization `json:"-"`
	PulseEnrollment           *pulse.EnrollmentTask               `json:"pulseEnrollment,omitempty"`
	ProtocolCommand           *nodeprotocol.Task                  `json:"protocolCommand,omitempty"`
	Kind                      string                              `json:"kind"`
	ID                        string                              `json:"id"`
	Attempt                   int64                               `json:"attempt"`
	AppKey                    string                              `json:"appKey"`
	Manifest                  catalog.AppManifest                 `json:"manifest"`
	Config                    json.RawMessage                     `json:"config"`
	Secrets                   json.RawMessage                     `json:"secrets"`
	Operation                 string                              `json:"operation"`
	DeleteData                bool                                `json:"deleteData"`
	DecommissionCallbackURL   string                              `json:"decommissionCallbackUrl,omitempty"`
	DecommissionCallbackToken string                              `json:"decommissionCallbackToken,omitempty"`
	Revision                  int64                               `json:"revision,omitempty"`
	ApplicationID             string                              `json:"applicationId,omitempty"`
	ApplicationRole           string                              `json:"applicationRole,omitempty"`
	ServiceAddress            string                              `json:"serviceAddress,omitempty"`
	GatewayState              *gateway.DesiredState               `json:"gatewayState,omitempty"`
	NodeListenerState         *gateway.NodeListenerState          `json:"nodeListenerState,omitempty"`
	LandingServerState        *landing.ServerState                `json:"landingServerState,omitempty"`
	LandingProxyState         *landing.DesiredState               `json:"landingProxyState,omitempty"`
	GatewayCertificates       []gateway.Certificate               `json:"gatewayCertificates,omitempty"`
	TunnelState               *TunnelDesiredState                 `json:"tunnelState,omitempty"`
	ApplicationCommand        *RealityCommandTask                 `json:"applicationCommand,omitempty"`
	SubscriptionCommand       *SubscriptionCommandTask            `json:"subscriptionCommand,omitempty"`
	ClientCommand             *ThreeXUIClientCommandTask          `json:"clientCommand,omitempty"`
	NodeCommand               *ThreeXUINodeCommandTask            `json:"nodeCommand,omitempty"`
	ControllerCommand         *ThreeXUIControllerCommandTask      `json:"controllerCommand,omitempty"`
	RegistryCredential        *RegistryCredential                 `json:"registryCredential,omitempty"`
	Reconcile                 bool                                `json:"reconcile,omitempty"`
	RequiredRuntimeGeneration int                                 `json:"requiredRuntimeGeneration,omitempty"`
	TargetVersion             string                              `json:"targetVersion,omitempty"`
}

type RegistryCredential struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type Executor interface {
	Deploy(context.Context, DeploymentTask) (ApplicationTaskResult, error)
}

type ApplicationServiceResult struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort"`
	Address       string `json:"address"`
}

type ApplicationTaskResult struct {
	IPQuality           *ipquality.Result                `json:"ipQuality,omitempty"`
	NodeDiagnostics     *nodediagnostics.Result          `json:"nodeDiagnostics,omitempty"`
	PulseEnrollment     *pulse.EnrollmentResult          `json:"pulseEnrollment,omitempty"`
	ProtocolCommand     *nodeprotocol.Result             `json:"protocolCommand,omitempty"`
	LandingPeer         *landing.PeerIdentity            `json:"landingPeer,omitempty"`
	Services            []ApplicationServiceResult       `json:"services"`
	GeneratedSecrets    map[string]string                `json:"generatedSecrets,omitempty"`
	ApplicationCommand  *RealityCommandResult            `json:"applicationCommand,omitempty"`
	SubscriptionCommand *SubscriptionCommandResult       `json:"subscriptionCommand,omitempty"`
	ClientCommand       *ThreeXUIClientCommandResult     `json:"clientCommand,omitempty"`
	NodeCommand         *ThreeXUINodeCommandResult       `json:"nodeCommand,omitempty"`
	ControllerCommand   *ThreeXUIControllerCommandResult `json:"controllerCommand,omitempty"`
}

type RealityCommandTask struct {
	TargetAgentPublicKey []byte                   `json:"targetAgentPublicKey,omitempty"`
	Recommend            bool                     `json:"recommend,omitempty"`
	VerifiedTarget       *realitytarget.Candidate `json:"verifiedTarget,omitempty"`
	Action               string                   `json:"action"`
	RegionCode           string                   `json:"regionCode"`
	DisplayName          string                   `json:"displayName"`
	ClientName           string                   `json:"clientName,omitempty"`
	InboundID            int                      `json:"inboundId,omitempty"`
	ConnectHostname      string                   `json:"connectHostname"`
	DNSProvider          string                   `json:"dnsProvider"`
	TargetHost           string                   `json:"targetHost,omitempty"`
	ServerName           string                   `json:"serverName,omitempty"`
	TargetApplicationID  string                   `json:"targetApplicationId"`
	TargetAddress        string                   `json:"targetAddress"`
	TargetPublicAddress  string                   `json:"targetPublicAddress"`
	TargetPanelPort      int                      `json:"targetPanelPort"`
	TargetNodeID         int                      `json:"targetNodeId,omitempty"`
	TargetAPIToken       string                   `json:"targetApiToken,omitempty"`
	CreateInitialClient  bool                     `json:"createInitialClient"`
	InboundTag           string                   `json:"inboundTag"`
	InboundTotalBytes    int64                    `json:"inboundTotalBytes"`
	InboundResetDay      int                      `json:"inboundResetDay"`
	ClientTotalBytes     int64                    `json:"clientTotalBytes"`
	ClientResetDays      int                      `json:"clientResetDays"`
	ClientExpiryTime     int64                    `json:"clientExpiryTime"`
	ServiceID            string                   `json:"serviceId,omitempty"`
	GuardRevision        int64                    `json:"guardRevision,omitempty"`
	RemoveHY2            bool                     `json:"removeHY2,omitempty"`
	HY2InboundID         int                      `json:"hy2InboundId,omitempty"`
}

type RealityCommandResult struct {
	Candidates        []realitytarget.Candidate `json:"candidates,omitempty"`
	LatencyMillis     int64                     `json:"latencyMillis,omitempty"`
	Samples           int                       `json:"samples,omitempty"`
	Action            string                    `json:"action"`
	InboundID         int                       `json:"inboundId"`
	DisplayName       string                    `json:"displayName"`
	ClientName        string                    `json:"clientName,omitempty"`
	Listen            string                    `json:"listen"`
	Port              int                       `json:"port"`
	TargetHost        string                    `json:"targetHost"`
	TargetIP          string                    `json:"targetIp"`
	ServerName        string                    `json:"serverName"`
	NodeASN           int64                     `json:"nodeAsn"`
	TargetASN         int64                     `json:"targetAsn"`
	CDNProvider       string                    `json:"cdnProvider,omitempty"`
	TLS13             bool                      `json:"tls13"`
	X25519            bool                      `json:"x25519"`
	HTTP2             bool                      `json:"http2"`
	CertificateValid  bool                      `json:"certificateValid"`
	GuardStatus       string                    `json:"guardStatus"`
	ProxyProtocol     bool                      `json:"proxyProtocol"`
	ConnectHostname   string                    `json:"connectHostname"`
	ShareURI          string                    `json:"shareUri"`
	InboundTag        string                    `json:"inboundTag"`
	ClientCreated     bool                      `json:"clientCreated"`
	InboundTotalBytes int64                     `json:"inboundTotalBytes"`
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

type ThreeXUIClientInbound struct {
	HY2InboundID    int    `json:"hy2InboundId,omitempty"`
	VLESSDisabled   bool   `json:"vlessDisabled,omitempty"`
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
	ResetDay        int    `json:"resetDay"`
	NextResetAt     string `json:"nextResetAt,omitempty"`
	PlanStatus      string `json:"planStatus"`
	PlanError       string `json:"planError,omitempty"`
	InboundTag      string `json:"inboundTag,omitempty"`
}

type ThreeXUIClientCommandTask struct {
	ManagedParentID     string                  `json:"managedParentId,omitempty"`
	GrantID             string                  `json:"grantId,omitempty"`
	GrantRevision       uint64                  `json:"grantRevision,omitempty"`
	GrantPhase          string                  `json:"grantPhase,omitempty"`
	Landing             *landing.ControllerTask `json:"landing,omitempty"`
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
	InboundResetDay     int                     `json:"inboundResetDay"`
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
	HasLanding      bool   `json:"hasLanding,omitempty"`
	ID              string `json:"id,omitempty"`
	Email           string `json:"email"`
	Enabled         bool   `json:"enabled"`
	TotalBytes      int64  `json:"totalBytes"`
	UsedBytes       int64  `json:"usedBytes"`
	ResetDays       int    `json:"resetDays"`
	ExpiryTime      int64  `json:"expiryTime"`
	LimitIP         int    `json:"limitIp"`
	InboundIDs      []int  `json:"inboundIds"`
	HasSubscription bool   `json:"hasSubscription"`
	TrafficObserved bool   `json:"-"`
	TrafficEnabled  bool   `json:"-"`
	TrafficTotal    int64  `json:"-"`
	TrafficExpiry   int64  `json:"-"`
	TrafficReset    int    `json:"-"`
}

type ThreeXUIClientCommandResult struct {
	Landing          *landing.ControllerResult `json:"landing,omitempty"`
	Clients          []ThreeXUIClientView      `json:"clients,omitempty"`
	ClientsObserved  bool                      `json:"clientsObserved"`
	Inbounds         []ThreeXUIClientInbound   `json:"inbounds"`
	InboundsObserved bool                      `json:"inboundsObserved"`
	Secret           string                    `json:"secret,omitempty"`
	SecretKind       string                    `json:"secretKind,omitempty"`
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

type ApplicationEndpointObservation struct {
	AppKey            string `json:"appKey"`
	Name              string `json:"name"`
	Protocol          string `json:"protocol"`
	AppProtocol       string `json:"appProtocol"`
	Listen            string `json:"listen"`
	Port              int    `json:"port"`
	Enabled           bool   `json:"enabled"`
	RemoteNodeID      int    `json:"remoteNodeId,omitempty"`
	InboundTag        string `json:"inboundTag,omitempty"`
	InboundTotalBytes int64  `json:"inboundTotalBytes,omitempty"`
}
