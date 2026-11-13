package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/controlplane"
	"github.com/petauron/vastora/internal/hostservice"
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
	var response struct {
		TailscaleCheckGeneration int64                           `json:"tailscaleCheckGeneration"`
		TailscaleUpdateTaskID    string                          `json:"tailscaleUpdateTaskId"`
		CenterURL                string                          `json:"centerUrl"`
		TailscaleIsolation       *TailscaleIsolationDesiredState `json:"tailscaleIsolation,omitempty"`
		PublicAddressLookupURL   string                          `json:"publicAddressLookupUrl"`
		PublicHelperAllowPrivate bool                            `json:"publicHelperAllowPrivate"`
	}
	publicKey, err := controlplane.PublicKey(connection.PrivateKey)
	if err != nil {
		return nil, err
	}
	runtimeRecovery, runtimeRecoveryApplications := store.runtimeRecovery()
	if executor, ok := c.Executor.(interface {
		PackageCapabilities(bool) (map[string]int, []string)
	}); ok {
		c.Capabilities.ExecutorVersions, c.Capabilities.RuntimeCapabilities = executor.PackageCapabilities(c.Capabilities.Docker)
	} else if c.Capabilities.LandingOnly && runtime.GOOS == "linux" && os.Geteuid() == 0 {
		// Availability is observation, not a host-wide executor grant. The task
		// runner constructs an executor only for a reviewed native lifecycle.
		c.Capabilities.ExecutorVersions = map[string]int{}
		if kind, err := hostservice.Detect(); err == nil {
			c.Capabilities.ExecutorVersions["native"] = 2
			if kind == hostservice.Systemd {
				c.Capabilities.ExecutorVersions["systemd"] = 2
			}
		}
		c.Capabilities.RuntimeCapabilities = []string{"native-services", "root"}
		if store.sharedCaddy != nil {
			c.Capabilities.RuntimeCapabilities = append(c.Capabilities.RuntimeCapabilities, "shared-caddy")
		}
	}
	if c.TailscaleEnrolled {
		store.scheduleTailscaleUpdateCheck(0, connection.AgentID)
		c.Capabilities.TailscaleUpdate = store.tailscaleUpdateStatus()
	}
	// The host ownership journal uses "none"; the wire schema represents
	// absence with the empty string, not a third ownership type.
	tailscaleOwnership := c.TailscaleOwnership
	if tailscaleOwnership == "none" {
		tailscaleOwnership = ""
	}
	payload := map[string]any{
		"publicKey": publicKey,
		"version":   Version, "appliedInstallations": len(states), "roles": c.Roles,
		"capabilities": c.Capabilities, "networkCandidates": candidates, "gatewayHealthy": gatewayHealthy,
		"gatewayRevision":              gatewayRevision,
		"gatewayConfigHash":            gatewayConfigHash,
		"nodeListenerHealthy":          nodeListenerHealthy,
		"privateNetworkPeer":           store.observePrivateNetworkPeer(ctx, candidates, c.TailscaleOwnership),
		"nodeListenerRevision":         nodeListenerRevision,
		"nodeListenerConfigHash":       nodeListenerConfigHash,
		"applicationRuntimeGeneration": platform.ApplicationRuntimeGeneration,
		"remoteUpdateSupported":        c.Updater != nil,
		"tailscaleEnrolled":            c.TailscaleEnrolled,
		"tailscaleOwnership":           tailscaleOwnership,
		"tailscaleUpdateObservation":   store.observeTailscaleUpdateTask(ctx),
		"startup":                      startup,
		"publicEgress":                 nil,
	}
	if runtimeRecovery != "" {
		payload["runtimeRecovery"] = runtimeRecovery
		payload["runtimeRecoveryApplications"] = runtimeRecoveryApplications
	}
	err = c.callAgentRPC(ctx, connection, rpcHeartbeat, "", payload, &response, controlplane.MaxJSONPayload, false)
	if err != nil {
		return nil, err
	}
	if err := c.applyDesiredCenterURL(ctx, store, connection, response.CenterURL); err != nil {
		return nil, err
	}
	store.setTailscaleUpdateTask(response.TailscaleUpdateTaskID)
	if c.TailscaleEnrolled && response.TailscaleCheckGeneration > 0 {
		store.scheduleTailscaleUpdateCheck(response.TailscaleCheckGeneration, connection.AgentID)
	}
	if response.TailscaleIsolation != nil && c.TailscaleIsolation != nil {
		if err := c.TailscaleIsolation(ctx, *response.TailscaleIsolation); err != nil {
			return nil, fmt.Errorf("agent: apply Tailscale isolation: %w", err)
		}
	}
	var publicEgressErr error
	if c.PublicEgress != nil && strings.TrimSpace(response.PublicAddressLookupURL) != "" {
		publicEgress, err := c.PublicEgress(ctx, response.PublicAddressLookupURL, response.PublicHelperAllowPrivate, candidates, now)
		if err != nil {
			publicEgressErr = fmt.Errorf("agent: observe public egress: %w", err)
		} else if publicEgress != nil {
			payload["publicEgress"] = publicEgress
			if err := c.callAgentRPC(ctx, connection, rpcHeartbeat, "", payload, &struct{}{}, controlplane.MaxJSONPayload, false); err != nil {
				return publicEgressErr, err
			}
		}
	}
	return publicEgressErr, nil
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

func (c Client) RunHeartbeats(ctx context.Context, store *Store, interval time.Duration, report func(error)) {
	// Glivela owns proxy account enforcement and subscription serving.
	// Keep the retained source data without launching those old writers.
	interval = heartbeatInterval(interval)
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

func heartbeatInterval(configured time.Duration) time.Duration {
	if configured < time.Second {
		return 15 * time.Second
	}
	return configured
}
