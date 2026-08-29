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
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/platform"
)

var Version = "0.1.0-dev"

const maxDeferredTaskAttempts int64 = 4

type Client struct {
	HTTPClient         *http.Client
	Executor           Executor
	Roles              []string
	Capabilities       Capabilities
	GatewayDriver      GatewayDriver
	GatewayProvisioner GatewayProvisioner
	TunnelProvisioner  TunnelProvisioner
	Decommissioner     HostDecommissioner
	TailscaleIsolation func(context.Context, TailscaleIsolationDesiredState) error
	TailscaleEnrolled  bool
	TailscaleOwnership string
}

type TailscaleIsolationDesiredState struct {
	ControlURL       string   `json:"controlUrl"`
	ControlAddresses []string `json:"controlAddresses"`
	ControlAliases   []string `json:"controlAliases,omitempty"`
	StaticEndpoints  []string `json:"staticEndpoints"`
}

type HostDecommissioner interface {
	Prepare(context.Context, bool) error
	ScheduleFinalRemoval(context.Context, bool) error
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
	Kind                string                         `json:"kind"`
	ID                  string                         `json:"id"`
	Attempt             int64                          `json:"attempt"`
	AppKey              string                         `json:"appKey"`
	Manifest            catalog.AppManifest            `json:"manifest"`
	Config              json.RawMessage                `json:"config"`
	Secrets             json.RawMessage                `json:"secrets"`
	Operation           string                         `json:"operation"`
	DeleteData          bool                           `json:"deleteData"`
	Revision            int64                          `json:"revision,omitempty"`
	ApplicationID       string                         `json:"applicationId,omitempty"`
	ApplicationRole     string                         `json:"applicationRole,omitempty"`
	ServiceAddress      string                         `json:"serviceAddress,omitempty"`
	GatewayState        *gateway.DesiredState          `json:"gatewayState,omitempty"`
	GatewayCertificates []gateway.Certificate          `json:"gatewayCertificates,omitempty"`
	TunnelState         *TunnelDesiredState            `json:"tunnelState,omitempty"`
	ApplicationCommand  *RealityCommandTask            `json:"applicationCommand,omitempty"`
	SubscriptionCommand *SubscriptionCommandTask       `json:"subscriptionCommand,omitempty"`
	ClientCommand       *ThreeXUIClientCommandTask     `json:"clientCommand,omitempty"`
	NodeCommand         *ThreeXUINodeCommandTask       `json:"nodeCommand,omitempty"`
	ControllerCommand   *ThreeXUIControllerCommandTask `json:"controllerCommand,omitempty"`
}

type Executor interface {
	Deploy(context.Context, DeploymentTask) (ApplicationTaskResult, error)
}

type executorMaintainer interface {
	Maintain(context.Context) error
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
	ResetDays       int    `json:"resetDays"`
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

func (c Client) Enroll(ctx context.Context, store *Store, centerURL, enrollmentToken string) (Enrollment, error) {
	return c.enroll(ctx, store, centerURL, enrollmentToken, false)
}

// MigrateEnrollment explicitly replaces an existing Center identity while
// preserving all workload state held by the Agent store.
func (c Client) MigrateEnrollment(ctx context.Context, store *Store, centerURL, enrollmentToken string) (Enrollment, error) {
	return c.enroll(ctx, store, centerURL, enrollmentToken, true)
}

func (c Client) enroll(ctx context.Context, store *Store, centerURL, enrollmentToken string, replace bool) (Enrollment, error) {
	baseURL, err := normalizeCenterURL(centerURL)
	if err != nil {
		return Enrollment{}, err
	}
	if strings.TrimSpace(enrollmentToken) == "" {
		return Enrollment{}, errors.New("agent: enrollment token is required")
	}
	var response Enrollment
	if err := c.post(ctx, baseURL+"/api/v1/agents/enroll", map[string]string{
		"token": enrollmentToken, "version": Version, "operatingSystem": runtime.GOOS, "architecture": runtime.GOARCH,
	}, "", &response); err != nil {
		return Enrollment{}, err
	}
	if response.ID == "" || response.Credential == "" || strings.TrimSpace(response.Name) == "" || len(response.Roles) == 0 {
		return Enrollment{}, errors.New("agent: Center returned an incomplete enrollment response")
	}
	connection := Connection{AgentID: response.ID, Name: response.Name, CenterURL: baseURL, Credential: response.Credential}
	if replace {
		err = store.ReplaceConnection(ctx, connection)
	} else {
		err = store.SaveConnection(ctx, connection)
	}
	if err != nil {
		return Enrollment{}, err
	}
	return response, nil
}

func (c Client) Heartbeat(ctx context.Context, store *Store) error {
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
	states, err := store.ListApplied(ctx)
	if err != nil {
		return nil, err
	}
	gatewayHealthy := false
	if c.GatewayDriver != nil {
		gatewayHealthy = c.GatewayDriver.Health(ctx) == nil
	}
	candidates, err := networking.Discover(time.Now())
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
		CenterURL          string                          `json:"centerUrl"`
		TailscaleIsolation *TailscaleIsolationDesiredState `json:"tailscaleIsolation,omitempty"`
	}
	err = c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/heartbeat", map[string]any{
		"version": Version, "appliedInstallations": len(states), "roles": c.Roles,
		"capabilities": c.Capabilities, "networkCandidates": candidates, "applicationEndpoints": endpoints, "applicationEndpointsObserved": endpointsObserved, "gatewayHealthy": gatewayHealthy,
		"applicationRuntimeGeneration": platform.ApplicationRuntimeGeneration,
		"tailscaleEnrolled":            c.TailscaleEnrolled,
		"tailscaleOwnership":           c.TailscaleOwnership,
		"startup":                      startup,
	}, connection.Credential, &response)
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
	return observeErr, nil
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
	if _, err := c.VerifyCenterURL(ctx, normalized); err != nil {
		return fmt.Errorf("agent: verify new Center URL before switching: %w", err)
	}
	previous := connection
	connection.CenterURL = normalized
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
func (c Client) VerifyCenterURL(ctx context.Context, desired string) (string, error) {
	normalized, err := normalizeCenterURL(desired)
	if err != nil {
		return "", err
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := c.get(ctx, normalized+"/healthz", "", &health); err != nil {
		return "", err
	}
	if health.Status != "ok" {
		return "", errors.New("health check is not OK")
	}
	return normalized, nil
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
	restoreContext, restoreCancel := context.WithTimeout(ctx, 5*time.Minute)
	if gatewayErr := restoreGatewayState(restoreContext, store, c.GatewayDriver); gatewayErr != nil && report != nil {
		report(gatewayErr)
	}
	restoreCancel()
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
	var lastMaintenance time.Time
	for {
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
		requestContext, requestCancel := context.WithTimeout(ctx, 5*time.Minute)
		c.processTask(requestContext, store, *task, report)
		requestCancel()
	}
}

func (c Client) processTask(ctx context.Context, store *Store, task DeploymentTask, report func(error)) {
	var result ApplicationTaskResult
	var err error
	switch task.Kind {
	case "application.apply":
		if c.Executor == nil {
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
			commandResult, err = c.applyThreeXUIControllerCommand(ctx, store, *task.ControllerCommand)
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
		} else if task.Operation == "running" {
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
	case "tunnel.state.apply":
		if c.TunnelProvisioner == nil || !c.Capabilities.Tunnel || task.TunnelState == nil {
			err = errors.New("agent: tunnel task received without tunnel capability")
		} else {
			err = c.TunnelProvisioner.Apply(ctx, *task.TunnelState)
		}
	case "agent.decommission":
		if c.Decommissioner == nil {
			err = errors.New("agent: host decommission capability is not configured")
		} else if err = c.Decommissioner.Prepare(ctx, task.DeleteData); err == nil {
			err = c.Decommissioner.ScheduleFinalRemoval(ctx, task.DeleteData)
		}
	default:
		err = errors.New("agent: unsupported task kind")
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
		_, err = store.RecordApplied(ctx, AppliedInstallation{InstanceID: task.ID, AppKey: task.AppKey, Version: task.Manifest.Version, Config: task.Config, Secrets: task.Secrets, ServiceAddress: task.ServiceAddress})
		if err != nil && committedThreeXUI {
			err = deferTaskUntilReconciled(err)
		}
	}
	if err == nil && task.Kind == "application.apply" && task.Operation == "uninstall" {
		err = store.RemoveApplied(ctx, task.AppKey)
	}
	if taskCompletionShouldBeDeferred(err, task.Attempt) {
		// Do not turn an uncertain compensation into a terminal failure. Center's
		// task lease recovery requeues the same command ID, allowing its
		// deterministic 3x-ui tag/client identifiers to converge safely.
		if report != nil {
			report(err)
		}
		return
	}
	reconciliationRequired := taskCompletionRequiresReconciliation(err, task.Attempt)
	if completeErr := c.completeTask(ctx, store, task.ID, task.Attempt, result, err, reconciliationRequired); completeErr != nil && report != nil {
		report(completeErr)
	}
	if err != nil && report != nil {
		report(err)
	}
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

func (c Client) claimNextTask(ctx context.Context, store *Store, wait time.Duration) (*DeploymentTask, error) {
	connection, err := store.Connection(ctx)
	if err != nil {
		return nil, err
	}
	var response struct {
		Task *DeploymentTask `json:"task"`
	}
	endpoint := connection.CenterURL + "/api/v1/agents/" + url.PathEscape(connection.AgentID) + "/tasks/next?wait=" + url.QueryEscape(wait.String())
	if err := c.get(ctx, endpoint, connection.Credential, &response); err != nil {
		return nil, err
	}
	return response.Task, nil
}

func (c Client) completeTask(ctx context.Context, store *Store, taskID string, attempt int64, result ApplicationTaskResult, deploymentErr error, reconciliationRequired bool) error {
	connection, err := store.Connection(ctx)
	if err != nil {
		return err
	}
	payload := map[string]any{"attempt": attempt, "succeeded": deploymentErr == nil, "error": "", "result": result, "reconciliationRequired": reconciliationRequired}
	if deploymentErr != nil {
		payload["error"] = deploymentErr.Error()
	}
	return c.post(ctx, connection.CenterURL+"/api/v1/agents/"+url.PathEscape(connection.AgentID)+"/tasks/"+url.PathEscape(taskID)+"/result", payload, connection.Credential, nil)
}

func (c Client) post(ctx context.Context, endpoint string, payload any, credential string, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("agent: encode Center request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agent: create Center request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent: request Center: %w", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
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

func (c Client) get(ctx context.Context, endpoint, credential string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("agent: create Center request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("agent: request Center: %w", err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
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
