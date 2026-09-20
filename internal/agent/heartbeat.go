package agent

import (
	"context"

	"encoding/json"

	"errors"

	"fmt"

	"io"

	"net"

	"net/http"

	"net/url"

	"strconv"

	"strings"

	"time"

	"github.com/petauron/vastora/internal/controlplane"

	"github.com/petauron/vastora/internal/landing"

	"github.com/petauron/vastora/internal/networking"

	"github.com/petauron/vastora/internal/platform"
)

func (c Client) Heartbeat(ctx context.Context, store *Store) error {
	_, err := c.heartbeat(ctx, store)
	return err
}

// StartupHeartbeat reports a new Agent process before its task loop begins.
// Center uses this boundary to fence work leased to the previous process.
func (c Client) StartupHeartbeat(ctx context.Context, store *Store) error {
	_, err := c.heartbeatWithStartup(ctx, store, true)
	return err
}

func (c Client) heartbeat(ctx context.Context, store *Store) (error, error) {
	return c.heartbeatWithStartup(ctx, store, false)
}

func (c Client) heartbeatWithStartup(ctx context.Context, store *Store, startup bool) (observationErr, controlErr error) {
	defer func() {
		if controlErr != nil {
			store.stopActiveExecution(controlErr)
		}
	}()
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
	nodeListenerHealthy, nodeListenerRevision, nodeListenerConfigHash := nodeListenerRuntimeStatus(ctx, store, c.NodeListener)
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
		LandingLatencyTargets    []landing.LatencyTarget         `json:"landingLatencyTargets"`
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
	runtimeRecovery, runtimeRecoveryApplications := store.runtimeRecovery()
	payload := map[string]any{
		"publicKey": publicKey,
		"version":   Version, "appliedInstallations": len(states), "roles": c.Roles,
		"capabilities": c.Capabilities, "networkCandidates": candidates, "applicationEndpoints": endpoints, "applicationEndpointsObserved": endpointsObserved, "gatewayHealthy": gatewayHealthy,
		"gatewayRevision":              gatewayRevision,
		"gatewayConfigHash":            gatewayConfigHash,
		"nodeListenerHealthy":          nodeListenerHealthy,
		"landingHealth":                store.landingHealth(),
		"landingClientRuntime":         store.observeLandingClientRuntime(ctx),
		"nodeListenerRevision":         nodeListenerRevision,
		"nodeListenerConfigHash":       nodeListenerConfigHash,
		"applicationRuntimeGeneration": platform.ApplicationRuntimeGeneration,
		"remoteUpdateSupported":        c.Updater != nil,
		"tailscaleEnrolled":            c.TailscaleEnrolled,
		"tailscaleOwnership":           c.TailscaleOwnership,
		"startup":                      startup,
		"publicEgress":                 nil,
	}
	if runtimeRecovery != "" {
		payload["runtimeRecovery"] = runtimeRecovery
		payload["runtimeRecoveryApplications"] = runtimeRecoveryApplications
	}
	err = c.post(ctx, heartbeatURL, payload, connection.Credential, connection.CAFingerprint, connection.CACertificatePEM, &response)
	if err != nil {
		return observeErr, err
	}
	if err := c.applyDesiredCenterURL(ctx, store, connection, response.CenterURL); err != nil {
		return observeErr, err
	}
	store.setLandingLatencyTargets(response.LandingLatencyTargets)
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
		// A managed HY2 sibling belongs to the existing subscription-node row.
		if inbound.Protocol == "hysteria" && strings.HasSuffix(inbound.Tag, "-hy2") && managedThreeXUIRealityTag(inbound.Tag) {
			continue
		}
		if isLegacyRealityGuardInbound(threeXUIRealityInbound{Remark: inbound.Remark, Protocol: inbound.Protocol, Listen: inbound.Listen, Port: inbound.Port, Tag: inbound.Tag}) {
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
	go store.runLandingAccounts(ctx, report)
	go store.runLandingSubscriptions(ctx, report)
	if interval < time.Second {
		interval = 15 * time.Second
	}
	send := func() {
		requestContext, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		observeErr, err := c.heartbeat(requestContext, store)
		if observeErr != nil && report != nil {
			report(observeErr)
		}
		if err != nil && report != nil {
			report(err)
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
