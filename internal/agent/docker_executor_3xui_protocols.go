package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"time"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
)

var hy2DockerPort = dockernetwork.MustParsePort("443/udp")

func (e ApplicationExecutor) ConfigureHY2Port(ctx context.Context, store *Store, applicationID string, enabled bool) error {
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return err
	}
	defer docker.Close()
	if err := requireNoInterruptedThreeXUIDeploy(ctx, docker); err != nil {
		return uncertainTaskOutcome(err)
	}
	if err := validateThreeXUIOwnership(ctx, docker, applicationID); err != nil {
		return err
	}
	current, exists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return err
	}
	if !exists || current.Container.Config == nil || current.Container.HostConfig == nil {
		return errors.New("agent: local 3x-ui container is unavailable")
	}
	if current.Container.Config.Labels[xrayWorkerRuntimeLabel] == "xray" {
		state, err := store.loadXrayWorkerState(ctx)
		if err != nil || state.ApplicationID != applicationID || state.AppliedRevision != state.Revision {
			return errors.Join(errors.New("agent: managed Xray worker state is unavailable"), err)
		}
		if current.Container.Config.Image != state.ImageReference || current.Container.HostConfig.NetworkMode != "host" {
			return errors.New("agent: managed Xray worker runtime identity changed")
		}
		if !enabled || slices.ContainsFunc(state.Inbounds, func(raw json.RawMessage) bool {
			var inbound struct {
				Enable   bool   `json:"enable"`
				Protocol string `json:"protocol"`
				Port     int    `json:"port"`
			}
			return json.Unmarshal(raw, &inbound) == nil && inbound.Enable && inbound.Protocol == "hysteria" && inbound.Port == threeXUIRealityPort
		}) {
			return nil
		}
		probe, err := net.ListenPacket("udp4", ":443")
		if err != nil {
			return errors.New("agent: UDP 443 is already in use; stop the conflicting service before enabling HY2")
		}
		return probe.Close()
	}
	routes, err := store.localThreeXUILandingRoutes(ctx, applicationID)
	if err != nil {
		return err
	}
	config, host := current.Container.Config, current.Container.HostConfig
	config.Image = current.Container.Image
	bindings := host.PortBindings[hy2DockerPort]
	if enabled && len(bindings) == 1 && bindings[0].HostIP == netip.IPv4Unspecified() && bindings[0].HostPort == "443" || !enabled && len(bindings) == 0 {
		return nil
	}
	if enabled && len(bindings) == 0 {
		probe, err := net.ListenPacket("udp4", ":443")
		if err != nil {
			return errors.New("agent: UDP 443 is already in use; stop the conflicting service before enabling HY2")
		}
		probe.Close()
	}
	if host.PortBindings == nil {
		host.PortBindings = dockernetwork.PortMap{}
	}
	if config.ExposedPorts == nil {
		config.ExposedPorts = dockernetwork.PortSet{}
	}
	if enabled {
		host.PortBindings[hy2DockerPort] = []dockernetwork.PortBinding{{HostIP: netip.IPv4Unspecified(), HostPort: "443"}}
		config.ExposedPorts[hy2DockerPort] = struct{}{}
	} else {
		delete(host.PortBindings, hy2DockerPort)
		delete(config.ExposedPorts, hy2DockerPort)
	}
	options := client.ContainerCreateOptions{Name: threeXUICandidateContainer, Config: config, HostConfig: host, NetworkingConfig: dockerruntime.NetworkingConfig(dockerruntime.ThreeXUIAlias)}
	check := func() error {
		_, err := threeXUIRequest(ctx, http.MethodPost, routes.baseURL+"/panel/api/setting/all", routes.token, map[string]any{})
		return err
	}
	_, err = replaceThreeXUIContainer(ctx, docker, options, false, func(string) (string, error) {
		deadline := time.NewTimer(45 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if check() == nil {
				return routes.token, nil
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-deadline.C:
				return "", errors.New("agent: 3x-ui did not become ready after changing protocols")
			case <-ticker.C:
			}
		}
	}, func(string, string) error { return check() })
	if err != nil {
		return uncertainTaskOutcome(err)
	}
	return nil
}

// Application upgrades keep the explicitly configured UDP binding. Fresh
// installations do not expose UDP until the administrator enables HY2.
func preserveThreeXUIHY2Port(ctx context.Context, docker threeXUIContainerEngine, exposed dockernetwork.PortSet, bindings dockernetwork.PortMap) error {
	current, exists, err := inspectThreeXUIContainer(ctx, docker, threeXUIContainer)
	if err != nil {
		return err
	}
	if exists && current.Container.HostConfig != nil && len(current.Container.HostConfig.PortBindings[hy2DockerPort]) > 0 {
		exposed[hy2DockerPort] = struct{}{}
		bindings[hy2DockerPort] = current.Container.HostConfig.PortBindings[hy2DockerPort]
	}
	return nil
}
