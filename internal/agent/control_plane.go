package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

var Version = "0.1.0-dev"

const (
	maxDeferredTaskAttempts int64 = 4
	taskLeaseRenewInterval        = time.Minute
	taskControlTimeout            = 15 * time.Second
)

type Client struct {
	HTTPClient         *http.Client
	Executor           Executor
	Roles              []string
	Capabilities       Capabilities
	GatewayDriver      GatewayDriver
	GatewayProvisioner GatewayProvisioner
	TunnelProvisioner  TunnelProvisioner
	Decommissioner     HostDecommissioner
	Updater            HostUpdater
	TailscaleIsolation func(context.Context, TailscaleIsolationDesiredState) error
	TailscaleEnrolled  bool
	TailscaleOwnership string
	PublicEgress       PublicEgressObserver
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
	TaskID        string
	Attempt       int64
	TargetVersion string
	Connection    Connection
}

// deferredTaskCompletionError leaves the Center task lease active so the same
// deterministic operation is retried after lease recovery. It is reserved for
// cases where reporting a terminal failure could strand an external resource
// whose cleanup outcome is still unknown.
type deferredTaskCompletionError struct {
	cause error
}

func (e *deferredTaskCompletionError) Error() string { return e.cause.Error() }
func (e *deferredTaskCompletionError) Unwrap() error { return e.cause }

func deferTaskCompletion(cause error) error {
	if cause == nil {
		return nil
	}
	return &deferredTaskCompletionError{cause: cause}
}

// reconciliationTaskCompletionError represents an operation whose external
// commit may already be visible. It must keep the same Center task alive until
// deterministic reconciliation succeeds; converting it to a terminal failure
// would allow a second command to create a duplicate external resource.
type reconciliationTaskCompletionError struct {
	cause error
}

func (e *reconciliationTaskCompletionError) Error() string { return e.cause.Error() }
func (e *reconciliationTaskCompletionError) Unwrap() error { return e.cause }

func deferTaskUntilReconciled(cause error) error {
	if cause == nil {
		return nil
	}
	return &reconciliationTaskCompletionError{cause: cause}
}

func taskCompletionIsDeferred(err error) bool {
	var deferred *deferredTaskCompletionError
	return errors.As(err, &deferred)
}

func taskCompletionShouldBeDeferred(err error, attempt int64) bool {
	var reconciliation *reconciliationTaskCompletionError
	if errors.As(err, &reconciliation) {
		return attempt < maxDeferredTaskAttempts
	}
	return taskCompletionIsDeferred(err) && attempt < maxDeferredTaskAttempts
}

func taskCompletionRequiresReconciliation(err error, attempt int64) bool {
	var reconciliation *reconciliationTaskCompletionError
	return attempt >= maxDeferredTaskAttempts && errors.As(err, &reconciliation)
}

type Capabilities struct {
	Docker  bool `json:"docker"`
	Gateway bool `json:"gateway"`
	Tunnel  bool `json:"tunnel"`
	Metrics bool `json:"metrics"`
	Logs    bool `json:"logs"`
}

type Enrollment struct {
	ID           string       `json:"id"`
	Credential   string       `json:"credential"`
	Name         string       `json:"name"`
	Roles        []string     `json:"roles"`
	Capabilities Capabilities `json:"capabilities"`
}

type DeploymentTask struct {
	Kind                      string                         `json:"kind"`
	ID                        string                         `json:"id"`
	Attempt                   int64                          `json:"attempt"`
	AppKey                    string                         `json:"appKey"`
	Manifest                  catalog.AppManifest            `json:"manifest"`
	Config                    json.RawMessage                `json:"config"`
	Secrets                   json.RawMessage                `json:"secrets"`
	Operation                 string                         `json:"operation"`
	DeleteData                bool                           `json:"deleteData"`
	DecommissionCallbackURL   string                         `json:"decommissionCallbackUrl,omitempty"`
	DecommissionCallbackToken string                         `json:"decommissionCallbackToken,omitempty"`
	Revision                  int64                          `json:"revision,omitempty"`
	ApplicationID             string                         `json:"applicationId,omitempty"`
	ApplicationRole           string                         `json:"applicationRole,omitempty"`
	ServiceAddress            string                         `json:"serviceAddress,omitempty"`
	GatewayState              *gateway.DesiredState          `json:"gatewayState,omitempty"`
	GatewayCertificates       []gateway.Certificate          `json:"gatewayCertificates,omitempty"`
	TunnelState               *TunnelDesiredState            `json:"tunnelState,omitempty"`
	ApplicationCommand        *RealityCommandTask            `json:"applicationCommand,omitempty"`
	SubscriptionCommand       *SubscriptionCommandTask       `json:"subscriptionCommand,omitempty"`
	ClientCommand             *ThreeXUIClientCommandTask     `json:"clientCommand,omitempty"`
	NodeCommand               *ThreeXUINodeCommandTask       `json:"nodeCommand,omitempty"`
	ControllerCommand         *ThreeXUIControllerCommandTask `json:"controllerCommand,omitempty"`
	RegistryCredential        *RegistryCredential            `json:"registryCredential,omitempty"`
	Reconcile                 bool                           `json:"reconcile,omitempty"`
	RequiredRuntimeGeneration int                            `json:"requiredRuntimeGeneration,omitempty"`
	OfflineRestore            bool                           `json:"-"`
	TargetVersion             string                         `json:"targetVersion,omitempty"`
}

type RegistryCredential struct {
	Host     string `json:"host"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type Executor interface {
	Deploy(context.Context, DeploymentTask) (ApplicationTaskResult, error)
}

type executorMaintainer interface {
	Maintain(context.Context) error
}

type executorRestorer interface {
	Restore(context.Context, *Store) error
}

type ApplicationServiceResult struct {
	Name          string `json:"name"`
	Protocol      string `json:"protocol"`
	ContainerPort int    `json:"containerPort"`
	HostPort      int    `json:"hostPort"`
	Address       string `json:"address"`
}

type ApplicationTaskResult struct {
	Services            []ApplicationServiceResult       `json:"services"`
	GeneratedSecrets    map[string]string                `json:"generatedSecrets,omitempty"`
	ApplicationCommand  *RealityCommandResult            `json:"applicationCommand,omitempty"`
	SubscriptionCommand *SubscriptionCommandResult       `json:"subscriptionCommand,omitempty"`
	ClientCommand       *ThreeXUIClientCommandResult     `json:"clientCommand,omitempty"`
	NodeCommand         *ThreeXUINodeCommandResult       `json:"nodeCommand,omitempty"`
	ControllerCommand   *ThreeXUIControllerCommandResult `json:"controllerCommand,omitempty"`
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
	InboundResetDay     int      `json:"inboundResetDay"`
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
	ProxyProtocol      bool   `json:"proxyProtocol"`
	ConnectHostname    string `json:"connectHostname"`
	ShareURI           string `json:"shareUri"`
	InboundTag         string `json:"inboundTag"`
	ClientCreated      bool   `json:"clientCreated"`
	InboundTotalBytes  int64  `json:"inboundTotalBytes"`
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

func (c Client) Enroll(ctx context.Context, store *Store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM string) (Enrollment, error) {
	return c.enroll(ctx, store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM, false)
}

// MigrateEnrollment explicitly replaces an existing Center identity while
// preserving all workload state held by the Agent store.
func (c Client) MigrateEnrollment(ctx context.Context, store *Store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM string) (Enrollment, error) {
	return c.enroll(ctx, store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM, true)
}

func (c Client) enroll(ctx context.Context, store *Store, centerURL, enrollmentToken, caFingerprint, caCertificatePEM string, replace bool) (Enrollment, error) {
	baseURL, err := normalizeCenterURL(centerURL)
	if err != nil {
		return Enrollment{}, err
	}
	if strings.TrimSpace(enrollmentToken) == "" {
		return Enrollment{}, errors.New("agent: enrollment token is required")
	}
	caFingerprint, caCertificatePEM, err = normalizeCenterTrust(baseURL, caFingerprint, caCertificatePEM)
	if err != nil {
		return Enrollment{}, err
	}
	if caFingerprint == "" && !loopbackCenterURL(baseURL) {
		caFingerprint, err = c.probeCenterCAFingerprint(ctx, baseURL)
		if err != nil {
			return Enrollment{}, err
		}
	}
	if err := validateCAFingerprint(baseURL, caFingerprint); err != nil {
		return Enrollment{}, err
	}
	operation, err := store.BeginEnrollmentOperation(ctx, baseURL, enrollmentToken, caFingerprint, caCertificatePEM, replace)
	if err != nil {
		return Enrollment{}, err
	}
	if operation.Phase != "enrollment_pending" {
		return store.EnrollmentForInstallOperation(ctx)
	}
	publicKey, err := controlplane.PublicKey(operation.PrivateKey)
	if err != nil {
		return Enrollment{}, errors.New("agent: stored enrollment identity is invalid")
	}
	var response Enrollment
	if err := c.post(ctx, baseURL+"/api/v1/agents/enroll", map[string]any{
		"token": operation.Token, "operationId": operation.OperationID, "version": Version, "operatingSystem": runtime.GOOS, "architecture": runtime.GOARCH, "publicKey": publicKey,
	}, "", caFingerprint, caCertificatePEM, &response); err != nil {
		return Enrollment{}, err
	}
	if response.ID == "" || response.Credential == "" || strings.TrimSpace(response.Name) == "" || len(response.Roles) == 0 {
		return Enrollment{}, errors.New("agent: Center returned an incomplete enrollment response")
	}
	if err := store.CompleteEnrollmentOperation(ctx, operation, response); err != nil {
		return Enrollment{}, err
	}
	return response, nil
}

func (c Client) Heartbeat(ctx context.Context, store *Store) error {
	if c.GatewayDriver != nil {
		if err := store.requireGatewayStartup(); err != nil {
			return err
		}
	}
	_, err := c.heartbeat(ctx, store)
	return err
}

func (c Client) heartbeat(ctx context.Context, store *Store) (error, error) {
	return c.heartbeatWithStartup(ctx, store, false)
}

func (c Client) heartbeatWithStartup(ctx context.Context, store *Store, startup bool) (error, error) {
	connection, err := store.Connection(ctx)
	if err != nil {
		return nil, err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return nil, err
	}
	states, err := store.ListApplied(ctx)
	if err != nil {
		return nil, err
	}
	gatewayHealthy, gatewayRevision, gatewayConfigHash := gatewayRuntimeStatus(ctx, store, c.GatewayDriver)
	now := time.Now()
	candidates, err := networking.Discover(now)
	if err != nil {
		return nil, fmt.Errorf("agent: discover network addresses: %w", err)
	}
	endpoints, observeErr := observeThreeXUI(ctx, store)
	endpointsObserved := observeErr == nil || errors.Is(observeErr, errApplicationNotInstalled)
	if errors.Is(observeErr, errApplicationNotInstalled) {
		observeErr = nil
	} else if observeErr != nil {
		observeErr = fmt.Errorf("agent: observe 3x-ui: %w", observeErr)
	}
	var response struct {
		CenterURL                string                          `json:"centerUrl"`
		TailscaleIsolation       *TailscaleIsolationDesiredState `json:"tailscaleIsolation,omitempty"`
		PublicAddressLookupURL   string                          `json:"publicAddressLookupUrl"`
		PublicHelperAllowPrivate bool                            `json:"publicHelperAllowPrivate"`
	}
	publicKey, err := controlplane.PublicKey(connection.PrivateKey)
	if err != nil {
		return observeErr, err
	}
	heartbeatURL := connection.CenterURL + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/heartbeat"
	payload := map[string]any{
		"publicKey": publicKey,
		"version":   Version, "appliedInstallations": len(states), "roles": c.Roles,
		"capabilities": c.Capabilities, "networkCandidates": candidates, "applicationEndpoints": endpoints, "applicationEndpointsObserved": endpointsObserved, "gatewayHealthy": gatewayHealthy,
		"gatewayRevision":              gatewayRevision,
		"gatewayConfigHash":            gatewayConfigHash,
		"applicationRuntimeGeneration": platform.ApplicationRuntimeGeneration,
		"remoteUpdateSupported":        c.Updater != nil,
		"tailscaleEnrolled":            c.TailscaleEnrolled,
		"tailscaleOwnership":           c.TailscaleOwnership,
		"startup":                      startup,
		"publicEgress":                 nil,
	}
	err = c.post(ctx, heartbeatURL, payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &response)
	if err != nil {
		return observeErr, err
	}
	if err := c.applyDesiredCenterURL(ctx, store, connection, response.CenterURL); err != nil {
		return observeErr, err
	}
	if response.TailscaleIsolation != nil && c.TailscaleIsolation != nil {
		if err := c.TailscaleIsolation(ctx, *response.TailscaleIsolation); err != nil {
			return observeErr, fmt.Errorf("agent: apply Tailscale isolation: %w", err)
		}
	}
	var publicEgressErr error
	if startup && c.PublicEgress != nil && strings.TrimSpace(response.PublicAddressLookupURL) != "" {
		publicEgress, err := c.PublicEgress(ctx, response.PublicAddressLookupURL, response.PublicHelperAllowPrivate, candidates, now)
		if err != nil {
			publicEgressErr = fmt.Errorf("agent: observe public egress: %w", err)
		} else if publicEgress != nil {
			payload["publicEgress"] = publicEgress
			if err := c.post(ctx, heartbeatURL, payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &struct{}{}); err != nil {
				return errors.Join(observeErr, publicEgressErr), err
			}
		}
	}
	return errors.Join(observeErr, publicEgressErr), nil
}

func (c Client) applyDesiredCenterURL(ctx context.Context, store *Store, connection Connection, desired string) error {
	desired = strings.TrimSpace(desired)
	if desired == "" {
		return nil
	}
	normalized, err := normalizeCenterURL(desired)
	if err != nil {
		return fmt.Errorf("agent: reject Center-directed URL update: %w", err)
	}
	if normalized == connection.CenterURL {
		if loopbackCenterURL(normalized) {
			return store.SetLocalCenterChannel(normalized)
		}
		return nil
	}
	localChannel, err := store.LocalCenterChannel(connection.CenterURL)
	if err != nil {
		return err
	}
	if localChannel {
		// A co-located Agent uses the host-only bootstrap listener so its
		// control channel does not depend on the Caddy instance it manages.
		return nil
	}
	_, fingerprint, err := c.verifyCenterURL(ctx, normalized)
	if err != nil {
		return fmt.Errorf("agent: verify new Center URL before switching: %w", err)
	}
	previous := connection
	connection.CenterURL = normalized
	connection.CAFingerprint = fingerprint
	connection.CACertificatePEM = ""
	if err := store.ReplaceConnection(ctx, connection); err != nil {
		return fmt.Errorf("agent: save new Center URL: %w", err)
	}
	if loopbackCenterURL(normalized) {
		if err := store.SetLocalCenterChannel(normalized); err != nil {
			if restoreErr := store.ReplaceConnection(ctx, previous); restoreErr != nil {
				return errors.Join(err, fmt.Errorf("agent: restore previous Center URL: %w", restoreErr))
			}
			return err
		}
	} else if err := store.SetLocalCenterChannel(""); err != nil {
		if restoreErr := store.ReplaceConnection(ctx, previous); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("agent: restore previous Center URL: %w", restoreErr))
		}
		return err
	}
	return nil
}

func loopbackCenterURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" {
		return false
	}
	host := net.ParseIP(parsed.Hostname())
	return host != nil && host.IsLoopback()
}

// VerifyCenterURL validates a user- or Center-supplied control-plane address
// and confirms that it serves a healthy Center before local state is changed.
type VerifiedCenter struct {
	URL              string
	CAFingerprint    string
	CACertificatePEM string
}

func (c Client) VerifyCenterURL(ctx context.Context, desired, expectedCAFingerprint, caCertificatePEM string) (VerifiedCenter, error) {
	normalized, err := normalizeCenterURL(desired)
	if err != nil {
		return VerifiedCenter{}, err
	}
	fingerprint, caCertificatePEM, err := normalizeCenterTrust(normalized, expectedCAFingerprint, caCertificatePEM)
	if err != nil {
		return VerifiedCenter{}, err
	}
	if fingerprint == "" && !loopbackCenterURL(normalized) {
		fingerprint, err = c.probeCenterCAFingerprint(ctx, normalized)
		if err != nil {
			return VerifiedCenter{}, err
		}
	}
	if err := validateCAFingerprint(normalized, fingerprint); err != nil {
		return VerifiedCenter{}, err
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := c.get(ctx, normalized+"/healthz", "", fingerprint, caCertificatePEM, &health); err != nil {
		return VerifiedCenter{}, err
	}
	if health.Status != "ok" {
		return VerifiedCenter{}, errors.New("health check is not OK")
	}
	return VerifiedCenter{URL: normalized, CAFingerprint: fingerprint, CACertificatePEM: caCertificatePEM}, err
}

func (c Client) verifyCenterURL(ctx context.Context, desired string) (string, string, error) {
	normalized, err := normalizeCenterURL(desired)
	if err != nil {
		return "", "", err
	}
	fingerprint, err := c.probeCenterCAFingerprint(ctx, normalized)
	if err != nil {
		return "", "", err
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := c.get(ctx, normalized+"/healthz", "", fingerprint, "", &health); err != nil {
		return "", "", err
	}
	if health.Status != "ok" {
		return "", "", errors.New("health check is not OK")
	}
	return normalized, fingerprint, nil
}

func observeThreeXUI(ctx context.Context, store *Store) ([]ApplicationEndpointObservation, error) {
	installation, err := store.AppliedInstallation(ctx, threeXUIKey)
	if err != nil {
		return nil, err
	}
	config, err := decodeThreeXUIConfig(installation.Config)
	if err != nil {
		return nil, err
	}
	var secretValues map[string]string
	if json.Unmarshal(installation.Secrets, &secretValues) != nil || strings.TrimSpace(secretValues["api_token"]) == "" {
		return nil, errors.New("agent: 3x-ui API token is unavailable")
	}
	address := installation.ServiceAddress
	if ip := net.ParseIP(address); ip == nil || ip.To4() == nil {
		address = "127.0.0.1"
	}
	endpoint := "http://" + net.JoinHostPort(address, strconv.Itoa(config.PanelPort)) + "/panel/api/inbounds/list"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+secretValues["api_token"])
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	var payload struct {
		Success bool `json:"success"`
		Object  []struct {
			ID         int             `json:"id"`
			Remark     string          `json:"remark"`
			Protocol   string          `json:"protocol"`
			Port       int             `json:"port"`
			Listen     string          `json:"listen"`
			Enable     bool            `json:"enable"`
			NodeID     *int            `json:"nodeId,omitempty"`
			Tag        string          `json:"tag"`
			TotalBytes int64           `json:"total"`
			Stream     json.RawMessage `json:"streamSettings"`
		} `json:"obj"`
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&payload) != nil || !payload.Success {
		return nil, errors.New("agent: 3x-ui inbound API request failed")
	}
	result := make([]ApplicationEndpointObservation, 0, len(payload.Object))
	for _, inbound := range payload.Object {
		if inbound.Remark == realityGuardRemark && inbound.Protocol == "tunnel" && inbound.Listen == "127.0.0.1" && inbound.Port == threeXUIRealityGuardPort && strings.HasSuffix(strings.TrimSpace(inbound.Tag), "-guard") {
			continue
		}
		transport := "tcp"
		var stream struct {
			Network  string `json:"network"`
			Security string `json:"security"`
		}
		if json.Unmarshal(inbound.Stream, &stream) == nil && stream.Network != "" {
			transport = strings.ToLower(stream.Network)
		}
		name := "inbound-" + strconv.Itoa(inbound.ID)
		appProtocol := strings.ToLower(inbound.Protocol) + "/" + transport
		if security := strings.ToLower(strings.TrimSpace(stream.Security)); security != "" && security != "none" {
			appProtocol += "/" + security
		}
		remoteNodeID := 0
		if inbound.NodeID != nil {
			remoteNodeID = *inbound.NodeID
		}
		result = append(result, ApplicationEndpointObservation{
			AppKey: threeXUIKey, Name: name, Protocol: "tcp", AppProtocol: appProtocol,
			Listen: strings.TrimSpace(inbound.Listen), Port: inbound.Port, Enabled: inbound.Enable,
			RemoteNodeID: remoteNodeID, InboundTag: strings.TrimSpace(inbound.Tag), InboundTotalBytes: inbound.TotalBytes,
		})
	}
	return result, nil
}

func (c Client) RunHeartbeats(ctx context.Context, store *Store, interval time.Duration, report func(error)) {
	if interval < time.Second {
		interval = 15 * time.Second
	}
	if c.GatewayDriver != nil {
		if err := store.requireGatewayStartup(); err != nil {
			if report != nil {
				report(err)
			}
			return
		}
	}
	startup := true
	send := func() {
		requestContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		observeErr, err := c.heartbeatWithStartup(requestContext, store, startup)
		if observeErr != nil && report != nil {
			report(observeErr)
		}
		if err != nil && report != nil {
			report(err)
		}
		if err == nil {
			startup = false
		}
	}
	send()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

func (c Client) RunTasks(ctx context.Context, store *Store, report func(error)) {
	if c.GatewayDriver != nil {
		if err := store.requireGatewayStartup(); err != nil {
			if report != nil {
				report(err)
			}
			return
		}
	}
	var lastMaintenance time.Time
	var lastRestore time.Time
	restorePending := true
	for {
		completionContext, completionCancel := freshTaskControlContext(ctx)
		pendingCompletion, completionErr := store.PendingTaskCompletion(completionContext)
		completionCancel()
		if completionErr != nil {
			if report != nil && ctx.Err() == nil {
				report(completionErr)
			}
			if !waitForTaskRetry(ctx) {
				return
			}
			continue
		}
		if pendingCompletion != nil {
			if err := c.deliverTaskCompletion(ctx, store, *pendingCompletion); err != nil {
				if report != nil && ctx.Err() == nil {
					report(err)
				}
				if !waitForTaskRetry(ctx) {
					return
				}
			}
			continue
		}
		if restorePending {
			receiptTaskID, receiptKind, receiptErr := store.UnresolvedApplicationTaskReceipt(ctx)
			if receiptErr != nil {
				if report != nil && ctx.Err() == nil {
					report(receiptErr)
				}
				if !waitForTaskRetry(ctx) {
					return
				}
				continue
			}
			if receiptTaskID != "" {
				if receiptKind == "legacy" {
					if report != nil {
						report(errors.New("agent: legacy unresolved task receipt requires operator reconciliation"))
					}
					if !waitForTaskRetry(ctx) {
						return
					}
					continue
				}
				claimContext, cancel := context.WithTimeout(ctx, 15*time.Second)
				task, err := c.claimNextTask(claimContext, store, 10*time.Second, receiptTaskID)
				cancel()
				if err != nil {
					if report != nil && ctx.Err() == nil {
						report(err)
					}
				} else if task != nil {
					c.processTaskWithLease(ctx, store, *task, report)
				}
				continue
			}
		}
		if restorePending && (lastRestore.IsZero() || time.Since(lastRestore) >= time.Minute) {
			lastRestore = time.Now()
			if restorer, ok := c.Executor.(executorRestorer); ok {
				restoreContext, restoreCancel := context.WithTimeout(ctx, 5*time.Minute)
				restoreErr := restorer.Restore(restoreContext, store)
				restoreCancel()
				if restoreErr != nil {
					if report != nil && ctx.Err() == nil {
						report(restoreErr)
					}
				} else {
					restorePending = false
				}
			} else {
				restorePending = false
			}
		}
		if restorePending {
			if !waitForTaskRetry(ctx) {
				return
			}
			continue
		}
		if maintainer, ok := c.Executor.(executorMaintainer); ok && (lastMaintenance.IsZero() || time.Since(lastMaintenance) >= time.Minute) {
			maintenanceContext, maintenanceCancel := context.WithTimeout(ctx, 15*time.Second)
			maintenanceErr := maintainer.Maintain(maintenanceContext)
			maintenanceCancel()
			lastMaintenance = time.Now()
			if maintenanceErr != nil && report != nil && ctx.Err() == nil {
				report(maintenanceErr)
			}
		}
		claimContext, cancel := context.WithTimeout(ctx, 15*time.Second)
		task, err := c.claimNextTask(claimContext, store, 10*time.Second)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if report != nil {
				report(err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if task == nil {
			continue
		}
		c.processTaskWithLease(ctx, store, *task, report)
	}
}

func freshTaskControlContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), taskControlTimeout)
}

func (c Client) deliverTaskCompletion(parent context.Context, store *Store, completion TaskCompletion) error {
	deliveryContext, deliveryCancel := freshTaskControlContext(parent)
	err := c.sendTaskCompletion(deliveryContext, store, completion)
	deliveryCancel()
	if err != nil {
		return err
	}
	acknowledgeContext, acknowledgeCancel := freshTaskControlContext(parent)
	err = store.AcknowledgeTaskCompletion(acknowledgeContext, completion.TaskID)
	acknowledgeCancel()
	return err
}

func (c Client) processTaskWithLease(parent context.Context, store *Store, task DeploymentTask, report func(error)) {
	c.processTaskWithLeaseInterval(parent, store, task, report, taskLeaseRenewInterval)
}

func (c Client) processTaskWithLeaseInterval(parent context.Context, store *Store, task DeploymentTask, report func(error), interval time.Duration) {
	executionContext, cancelExecution := context.WithCancel(parent)
	renewalResult := make(chan error, 1)
	go func() {
		err := c.renewTaskLeaseLoop(executionContext, store, task, interval)
		if err != nil {
			cancelExecution()
		}
		renewalResult <- err
	}()
	c.processTask(executionContext, store, task, report)
	cancelExecution()
	if err := <-renewalResult; err != nil && parent.Err() == nil && report != nil {
		report(err)
	}
}

func (c Client) renewTaskLeaseLoop(ctx context.Context, store *Store, task DeploymentTask, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("agent: task lease renewal interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			requestContext, cancel := context.WithTimeout(ctx, taskControlTimeout)
			err := c.renewTaskLease(requestContext, store, task.ID, task.Attempt)
			cancel()
			if err != nil {
				return fmt.Errorf("agent: renew task lease: %w", err)
			}
		}
	}
}

func waitForTaskRetry(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Second):
		return true
	}
}

func (c Client) processTask(ctx context.Context, store *Store, task DeploymentTask, report func(error)) {
	receiptContext, receiptCancel := freshTaskControlContext(ctx)
	storedCompletion, receiptErr := store.PrepareTaskReceipt(receiptContext, task)
	receiptCancel()
	if receiptErr != nil {
		if report != nil {
			report(receiptErr)
		}
		return
	}
	if storedCompletion != nil {
		if err := c.deliverTaskCompletion(ctx, store, *storedCompletion); err != nil {
			if report != nil {
				report(err)
			}
		}
		return
	}
	var result ApplicationTaskResult
	var err error
	decommissionHandedOff := false
	updateHandedOff := false
	switch task.Kind {
	case "application.apply":
		if task.RequiredRuntimeGeneration < 0 || task.RequiredRuntimeGeneration > platform.ApplicationRuntimeGeneration {
			err = fmt.Errorf("agent: application task requires runtime generation %d, executor is generation %d", task.RequiredRuntimeGeneration, platform.ApplicationRuntimeGeneration)
		} else if c.Executor == nil {
			err = errors.New("agent: application capability is not configured")
		} else if task.AppKey != komariKey && !c.Capabilities.Docker {
			err = errors.New("agent: Docker capability is not configured")
		} else {
			result, err = c.Executor.Deploy(ctx, task)
		}
	case "application.command":
		commands := 0
		if task.ApplicationCommand != nil {
			commands++
		}
		if task.SubscriptionCommand != nil {
			commands++
		}
		if task.ClientCommand != nil {
			commands++
		}
		if task.NodeCommand != nil {
			commands++
		}
		if task.ControllerCommand != nil {
			commands++
		}
		if !c.Capabilities.Docker || commands != 1 {
			err = errors.New("agent: application command received without Docker capability")
		} else if task.ApplicationCommand != nil {
			var commandResult RealityCommandResult
			commandResult, err = applyRealityCommand(ctx, store, task.ID, task.Attempt, *task.ApplicationCommand)
			if err == nil {
				result.ApplicationCommand = &commandResult
			}
		} else if task.SubscriptionCommand != nil {
			var commandResult SubscriptionCommandResult
			commandResult, err = applySubscriptionCommand(ctx, store, *task.SubscriptionCommand)
			if err == nil {
				result.SubscriptionCommand = &commandResult
			}
		} else if task.ClientCommand != nil {
			var commandResult ThreeXUIClientCommandResult
			commandResult, err = applyThreeXUIClientCommand(ctx, store, *task.ClientCommand)
			if err == nil {
				result.ClientCommand = &commandResult
			}
		} else if task.NodeCommand != nil {
			var commandResult ThreeXUINodeCommandResult
			commandResult, err = applyThreeXUINodeCommand(ctx, store, *task.NodeCommand)
			if err == nil {
				result.NodeCommand = &commandResult
			}
		} else {
			var commandResult ThreeXUIControllerCommandResult
			commandResult, err = c.applyThreeXUIControllerCommand(ctx, store, task.ID, *task.ControllerCommand)
			if err == nil {
				result.ControllerCommand = &commandResult
			}
		}
	case "gateway.routes.apply":
		if task.GatewayState == nil || !c.Capabilities.Gateway {
			err = errors.New("agent: gateway task received without gateway capability")
		} else {
			err = applyGatewayDesiredState(ctx, store, c.GatewayDriver, *task.GatewayState, task.GatewayCertificates)
		}
	case "gateway.component.apply":
		if c.GatewayProvisioner == nil || !c.Capabilities.Gateway {
			err = errors.New("agent: gateway provisioning capability is not configured")
		} else {
			store.gatewayMutationMu.Lock()
			if task.Operation == "running" {
				err = c.GatewayProvisioner.Ensure(ctx)
				if err == nil {
					err = waitForGateway(ctx, c.GatewayDriver)
				}
			} else if task.Operation == "stopped" {
				err = c.GatewayProvisioner.Remove(ctx)
				if err == nil {
					err = store.ClearGatewayState(ctx)
				}
			} else {
				err = errors.New("agent: invalid gateway component operation")
			}
			store.gatewayMutationMu.Unlock()
		}
	case "tunnel.state.apply":
		if c.TunnelProvisioner == nil || !c.Capabilities.Tunnel || task.TunnelState == nil {
			err = errors.New("agent: tunnel task received without tunnel capability")
		} else {
			err = c.TunnelProvisioner.Apply(ctx, *task.TunnelState)
		}
	case "agent.decommission":
		if c.Decommissioner == nil {
			err = errors.New("agent: host decommission capability is not configured")
		} else {
			var callbackURL string
			callbackURL, err = normalizeHostDecommissionCallbackURL(task.DecommissionCallbackURL, task.ID)
			if err == nil && strings.TrimSpace(task.DecommissionCallbackToken) == "" {
				err = errors.New("agent: host decommission callback token is missing")
			}
			var connection Connection
			if err == nil {
				connection, err = store.Connection(ctx)
			}
			if err == nil {
				err = c.Decommissioner.ScheduleFinalRemoval(ctx, HostDecommissionRequest{TaskID: task.ID, Attempt: task.Attempt, DeleteData: task.DeleteData, CallbackURL: callbackURL, CallbackToken: task.DecommissionCallbackToken, Connection: connection})
				decommissionHandedOff = err == nil
			}
		}
	case "agent.update":
		if c.Updater == nil {
			err = errors.New("agent: host update capability is not configured")
		} else if strings.TrimSpace(task.TargetVersion) == "" {
			err = errors.New("agent: update target version is missing")
		} else {
			var connection Connection
			connection, err = store.Connection(ctx)
			if err == nil {
				err = c.Updater.ScheduleUpdate(ctx, HostUpdateRequest{TaskID: task.ID, Attempt: task.Attempt, TargetVersion: task.TargetVersion, Connection: connection})
				updateHandedOff = err == nil
			}
		}
	default:
		err = errors.New("agent: unsupported task kind")
	}
	if decommissionHandedOff {
		// The persistent host helper owns the terminal result. A successful
		// schedule is not evidence that host cleanup itself has completed.
		return
	}
	if updateHandedOff {
		// The persistent updater owns binary replacement, rollback, restart, and
		// terminal reporting. A successful schedule is not an update result.
		return
	}
	if task.Kind == "application.apply" && task.Operation != "uninstall" && len(result.GeneratedSecrets) != 0 {
		merged, mergeErr := mergeGeneratedSecrets(task.Secrets, result.GeneratedSecrets)
		if mergeErr != nil {
			err = errors.Join(err, mergeErr)
		} else {
			task.Secrets = merged
		}
	}
	committedThreeXUI := task.Kind == "application.apply" && task.Operation != "uninstall" && task.AppKey == threeXUIKey && strings.TrimSpace(result.GeneratedSecrets["api_token"]) != ""
	if err != nil && committedThreeXUI {
		err = deferTaskUntilReconciled(err)
	}
	if err == nil && task.Kind == "application.apply" && task.Operation != "uninstall" {
		persistContext, persistCancel := freshTaskControlContext(ctx)
		_, err = store.RecordApplied(persistContext, AppliedInstallation{InstanceID: task.ID, ApplicationID: task.ApplicationID, AppKey: task.AppKey, Version: task.Manifest.Version, Config: task.Config, Secrets: task.Secrets, ServiceAddress: task.ServiceAddress, Manifest: task.Manifest, ApplicationRole: task.ApplicationRole})
		persistCancel()
		if err != nil && committedThreeXUI {
			err = deferTaskUntilReconciled(err)
		}
	}
	if err == nil && task.Kind == "application.apply" && task.Operation == "uninstall" {
		persistContext, persistCancel := freshTaskControlContext(ctx)
		err = store.RemoveApplied(persistContext, task.AppKey)
		persistCancel()
	}
	if taskCompletionShouldBeDeferred(err, task.Attempt) {
		// Do not turn an uncertain compensation into a terminal failure. Center's
		// task lease recovery requeues the same command ID, allowing its
		// deterministic 3x-ui tag/client identifiers to converge safely.
		if report != nil {
			report(errors.New(safeTaskError(err)))
		}
		return
	}
	reconciliationRequired := taskCompletionRequiresReconciliation(err, task.Attempt)
	completion := TaskCompletion{TaskID: task.ID, Attempt: task.Attempt, Result: result, Error: safeTaskError(err), ReconciliationRequired: reconciliationRequired}
	if task.Kind == "application.apply" {
		completion.ApplicationRuntimeGeneration = platform.ApplicationRuntimeGeneration
	}
	completionContext, completionCancel := freshTaskControlContext(ctx)
	completionErr := store.RecordTaskCompletion(completionContext, completion)
	completionCancel()
	if completionErr != nil {
		if report != nil {
			report(completionErr)
		}
		return
	}
	if completeErr := c.deliverTaskCompletion(ctx, store, completion); completeErr != nil {
		if report != nil {
			report(completeErr)
		}
	}
	if err != nil && report != nil {
		report(errors.New(safeTaskError(err)))
	}
}

func (c Client) sendTaskCompletion(ctx context.Context, store *Store, completion TaskCompletion) error {
	var deploymentErr error
	if completion.Error != "" {
		deploymentErr = errors.New(completion.Error)
	}
	return c.completeTask(ctx, store, completion.TaskID, completion.Attempt, completion.Result, deploymentErr, completion.ReconciliationRequired, completion.ApplicationRuntimeGeneration)
}

func safeTaskError(err error) string {
	if err == nil {
		return ""
	}
	return controlplane.SafeError(err.Error())
}

func mergeGeneratedSecrets(raw json.RawMessage, generated map[string]string) (json.RawMessage, error) {
	values := map[string]any{}
	if len(raw) != 0 && json.Unmarshal(raw, &values) != nil {
		return nil, errors.New("agent: stored task secrets are invalid")
	}
	for key, value := range generated {
		values[key] = value
	}
	return json.Marshal(values)
}

func waitForGateway(ctx context.Context, driver GatewayDriver) error {
	if driver == nil {
		return errors.New("agent: gateway driver is not configured")
	}
	readyContext, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := driver.Health(readyContext); err == nil {
			return nil
		}
		select {
		case <-readyContext.Done():
			return errors.New("agent: Caddy gateway did not become healthy")
		case <-ticker.C:
		}
	}
}

func (c Client) claimNextTask(ctx context.Context, store *Store, wait time.Duration, requiredTaskIDs ...string) (*DeploymentTask, error) {
	connection, err := store.Connection(ctx)
	if err != nil {
		return nil, err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return nil, err
	}
	var response struct {
		Task *struct {
			ID       string                `json:"id"`
			Attempt  int64                 `json:"attempt"`
			Envelope controlplane.Envelope `json:"envelope"`
		} `json:"task"`
	}
	endpoint := connection.CenterURL + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/tasks/next?wait=" + url.QueryEscape(wait.String())
	if len(requiredTaskIDs) != 0 && strings.TrimSpace(requiredTaskIDs[0]) != "" {
		endpoint += "&taskId=" + url.QueryEscape(strings.TrimSpace(requiredTaskIDs[0]))
	}
	if err := c.get(ctx, endpoint, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &response); err != nil {
		return nil, err
	}
	if response.Task == nil {
		return nil, nil
	}
	aad := controlplane.TaskAdditionalData(connection.AgentID, response.Task.ID, response.Task.Attempt)
	plaintext, err := controlplane.Open(connection.PrivateKey, response.Task.Envelope, aad)
	if err != nil {
		return nil, err
	}
	var task DeploymentTask
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&task); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("agent: Center returned an invalid encrypted task")
	}
	if task.ID != response.Task.ID || task.Attempt != response.Task.Attempt || task.ID == "" || task.Attempt <= 0 {
		return nil, errors.New("agent: encrypted task identity does not match its envelope")
	}
	return &task, nil
}

func (c Client) completeTask(ctx context.Context, store *Store, taskID string, attempt int64, result ApplicationTaskResult, deploymentErr error, reconciliationRequired bool, applicationRuntimeGeneration int) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return err
	}
	payload := map[string]any{"attempt": attempt, "succeeded": deploymentErr == nil, "error": "", "result": result, "reconciliationRequired": reconciliationRequired, "applicationRuntimeGeneration": applicationRuntimeGeneration}
	if deploymentErr != nil {
		payload["error"] = deploymentErr.Error()
	}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/"+url.PathEscape(taskID)+"/result", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}

// BeginHostDecommission transfers responsibility for a claimed cleanup from
// the short Agent task lease to the persistent host helper.
func (c Client) BeginHostDecommission(ctx context.Context, connection Connection, taskID string, attempt int64) error {
	if strings.TrimSpace(taskID) == "" || attempt <= 0 || strings.TrimSpace(connection.AgentID) == "" || strings.TrimSpace(connection.Credential) == "" {
		return errors.New("agent: invalid host decommission handoff")
	}
	payload := map[string]any{"taskId": taskID, "attempt": attempt}
	var response struct {
		Started bool `json:"started"`
	}
	if err := c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/decommission/start", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &response); err != nil {
		return err
	}
	if !response.Started {
		return errors.New("agent: Center did not acknowledge host cleanup handoff")
	}
	return nil
}

// CompleteHostDecommission reports successful local cleanup through the
// task-bound public callback after the Agent's private network is gone.
func (c Client) CompleteHostDecommission(ctx context.Context, callbackURL, callbackToken, taskID string, attempt int64) error {
	callbackURL, err := normalizeHostDecommissionCallbackURL(callbackURL, taskID)
	if err != nil || attempt <= 0 || strings.TrimSpace(callbackToken) == "" {
		return errors.New("agent: invalid host decommission completion")
	}
	payload := map[string]any{"attempt": attempt}
	var response struct {
		Completed bool `json:"completed"`
	}
	if err := c.post(ctx, callbackURL, payload, callbackToken, "", "", &response); err != nil {
		return err
	}
	if !response.Completed {
		return errors.New("agent: Center did not acknowledge host cleanup completion")
	}
	return nil
}

func normalizeHostDecommissionCallbackURL(raw, taskID string) (string, error) {
	taskID = strings.TrimSpace(taskID)
	callbackURL, err := normalizeCenterURL(raw)
	if err != nil || taskID == "" {
		return "", errors.New("agent: invalid host decommission callback URL")
	}
	parsed, err := url.Parse(callbackURL)
	if err != nil || parsed.Path != "/api/v1/agent-decommission-results/"+taskID {
		return "", errors.New("agent: invalid host decommission callback URL")
	}
	return callbackURL, nil
}

// BeginHostUpdate transfers a claimed update from the Agent lease to the
// persistent systemd helper before the Agent process is restarted.
func (c Client) BeginHostUpdate(ctx context.Context, connection Connection, taskID string, attempt int64) error {
	if strings.TrimSpace(taskID) == "" || attempt <= 0 || strings.TrimSpace(connection.AgentID) == "" || strings.TrimSpace(connection.Credential) == "" {
		return errors.New("agent: invalid host update handoff")
	}
	payload := map[string]any{"attempt": attempt}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/updates/"+url.PathEscape(taskID)+"/start", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}

// CompleteHostUpdate reports the persistent helper outcome. Center accepts a
// success only after the target Agent version has reconnected by heartbeat.
func (c Client) CompleteHostUpdate(ctx context.Context, connection Connection, taskID string, attempt int64, updateErr error) error {
	if strings.TrimSpace(taskID) == "" || attempt <= 0 || strings.TrimSpace(connection.AgentID) == "" || strings.TrimSpace(connection.Credential) == "" {
		return errors.New("agent: invalid host update completion")
	}
	payload := map[string]any{"attempt": attempt, "succeeded": updateErr == nil, "error": safeTaskError(updateErr), "result": ApplicationTaskResult{}, "reconciliationRequired": false}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/"+url.PathEscape(taskID)+"/result", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}

func (c Client) renewTaskLease(ctx context.Context, store *Store, taskID string, attempt int64) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	connection, err = c.ensureConnectionPinned(ctx, store, connection)
	if err != nil {
		return err
	}
	payload := map[string]any{"attempt": attempt}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/"+url.PathEscape(taskID)+"/lease", payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, nil)
}

func (c Client) post(ctx context.Context, endpoint string, payload any, credential, caFingerprint, caCertificatePEM string, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("agent: encode Center request: %w", err)
	}
	if len(body) > controlplane.MaxJSONPayload {
		return errors.New("agent: Center request exceeds the allowed size")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agent: create Center request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	client, err := c.clientFor(caFingerprint, caCertificatePEM, 15*time.Second)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent: request Center: %w", err)
	}
	defer response.Body.Close()
	content, err := readBoundedResponse(response.Body, controlplane.MaxEnvelopeWire)
	if err != nil {
		return fmt.Errorf("agent: read Center response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(content, &failure)
		if failure.Error == "" {
			failure.Error = response.Status
		}
		return fmt.Errorf("agent: Center request failed: %s", failure.Error)
	}
	if target != nil && json.Unmarshal(content, target) != nil {
		return errors.New("agent: Center returned invalid JSON")
	}
	return nil
}

func (c Client) get(ctx context.Context, endpoint, credential, caFingerprint, caCertificatePEM string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("agent: create Center request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	client, err := c.clientFor(caFingerprint, caCertificatePEM, 15*time.Second)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent: request Center: %w", err)
	}
	defer response.Body.Close()
	content, err := readBoundedResponse(response.Body, controlplane.MaxEnvelopeWire)
	if err != nil {
		return fmt.Errorf("agent: read Center response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("agent: Center request failed: %s", response.Status)
	}
	if err := json.Unmarshal(content, target); err != nil {
		return errors.New("agent: Center returned invalid JSON")
	}
	return nil
}

func readBoundedResponse(reader io.Reader, limit int64) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > limit {
		return nil, errors.New("agent: Center response exceeds the allowed size")
	}
	return content, nil
}

func normalizeCenterURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("agent: Center URL must be an absolute URL without credentials, query, or fragment")
	}
	if parsed.Scheme == "https" {
		return strings.TrimRight(parsed.String(), "/"), nil
	}
	if parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return "", errors.New("agent: Center URL must use HTTPS unless it is loopback HTTP")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil && ip.IsLoopback()
}
