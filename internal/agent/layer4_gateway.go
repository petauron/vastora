package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/gatewayruntime"
)

const (
	// DefaultHAProxyImage is the multi-architecture runtime index used by every
	// Agent entrypoint. Keep the default here so CLI wiring cannot drift from
	// gateway reconciliation.
	DefaultHAProxyImage      = "docker.io/library/haproxy:3.2.7-alpine@sha256:3b80483d47e1c7d1fc7eb4b9104f33d9a51259769be299eb675524dca2bc8157"
	defaultHAProxyContainer  = gatewayruntime.HAProxyContainer
	haproxyConfigurationDir  = "/usr/local/etc/haproxy"
	haproxyConfigurationPath = haproxyConfigurationDir + "/haproxy.cfg"
	haproxyConfigurationEnv  = "VASTORA_HAPROXY_CONFIG"
	layer4ConfigurationLabel = "io.vastora.layer4.configuration-hash"
	haproxyBootstrapCommand  = "umask 077\nprintf '%s' \"$" + haproxyConfigurationEnv + "\" > " + haproxyConfigurationPath + "\nexec haproxy -W -db -f " + haproxyConfigurationPath
)

type Layer4Provisioner interface {
	Apply(context.Context, gateway.SharedHTTPS) error
	Remove(context.Context) error
	Health(context.Context) error
}

type Layer4ConfigurationReader interface {
	ConfigurationHash(context.Context) (string, error)
}

type NodeListenerProvisioner interface {
	Layer4Provisioner
	Layer4ConfigurationReader
	Absent(context.Context) error
}

type GatewayRuntimeProvisioner interface {
	Reconcile(context.Context, gateway.DesiredState) error
}

// ManagedGatewayDriver owns only Site Gateway Caddy state. On a dual-role node
// it coordinates Caddy's host binding with the independent node-listener
// controller that owns HAProxy.
type ManagedGatewayDriver struct {
	Caddy   *CaddyGatewayDriver
	Layer4  Layer4Provisioner
	Runtime GatewayRuntimeProvisioner

	mutationMu   sync.Mutex
	mu           sync.RWMutex
	state        gateway.DesiredState
	certificates []gateway.Certificate
	nodeListener bool
}

func (driver *ManagedGatewayDriver) ApplyConfiguration(ctx context.Context, desired gateway.DesiredState, certificates []gateway.Certificate) error {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	if driver.Caddy == nil || driver.Layer4 == nil || driver.Runtime == nil {
		return errors.New("agent: managed gateway is not configured")
	}
	if err := desired.Validate(); err != nil {
		return err
	}
	driver.mu.RLock()
	previous := driver.state.Sorted()
	previousCertificates := append([]gateway.Certificate(nil), driver.certificates...)
	driver.mu.RUnlock()
	if err := driver.apply(ctx, desired.Sorted(), certificates); err != nil {
		if previous.Revision > 0 {
			if rollbackErr := driver.apply(ctx, previous, previousCertificates); rollbackErr != nil {
				return errors.Join(err, fmt.Errorf("agent: restore previous gateway revision %d: %w", previous.Revision, rollbackErr))
			}
		}
		return err
	}
	driver.mu.Lock()
	driver.state = desired.Sorted()
	driver.certificates = append([]gateway.Certificate(nil), certificates...)
	driver.mu.Unlock()
	return nil
}

func (driver *ManagedGatewayDriver) apply(ctx context.Context, desired gateway.DesiredState, certificates []gateway.Certificate) error {
	if desired.SharedHTTPS == nil {
		runtimeDesired := desired
		driver.mu.RLock()
		nodeListener := driver.nodeListener
		driver.mu.RUnlock()
		if nodeListener {
			for _, listener := range desired.Listeners {
				if listener.Kind == "public" {
					runtimeDesired.SharedHTTPS = &gateway.SharedHTTPS{Address: listener.Address, Port: listener.HTTPSPort, CaddyAddress: dockerruntime.CaddyAlias, CaddyPort: 443}
					break
				}
			}
		} else if err := driver.Layer4.Remove(ctx); err != nil {
			return fmt.Errorf("agent: remove retired shared HTTPS frontend: %w", err)
		}
		if err := driver.Runtime.Reconcile(ctx, runtimeDesired); err != nil {
			return err
		}
		return driver.Caddy.ApplyConfiguration(ctx, desired, certificates)
	}
	// Move Caddy away from public 443 before HAProxy claims it.
	if err := driver.Runtime.Reconcile(ctx, desired); err != nil {
		return err
	}
	if err := driver.Caddy.ApplyConfiguration(ctx, desired, certificates); err != nil {
		return err
	}
	if err := driver.Layer4.Apply(ctx, *desired.SharedHTTPS); err != nil {
		fallback := desired
		fallback.SharedHTTPS = nil
		if runtimeErr := driver.Runtime.Reconcile(ctx, fallback); runtimeErr != nil {
			return errors.Join(fmt.Errorf("agent: apply shared 443 frontend: %w", err), fmt.Errorf("agent: restore Caddy port bindings: %w", runtimeErr))
		}
		if restoreErr := driver.Caddy.ApplyConfiguration(ctx, fallback, certificates); restoreErr != nil {
			return errors.Join(fmt.Errorf("agent: apply shared 443 frontend: %w", err), fmt.Errorf("agent: restore Caddy to public 443: %w", restoreErr))
		}
		return fmt.Errorf("agent: apply shared 443 frontend: %w", err)
	}
	return nil
}

func (driver *ManagedGatewayDriver) PrepareNodeListener(ctx context.Context) error {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	driver.mu.RLock()
	current := driver.state.Sorted()
	certificates := append([]gateway.Certificate(nil), driver.certificates...)
	driver.mu.RUnlock()
	if current.Revision == 0 {
		driver.setNodeListenerActive(true)
		return nil
	}
	var public *gateway.Listener
	for index := range current.Listeners {
		if current.Listeners[index].Kind == "public" {
			public = &current.Listeners[index]
			break
		}
	}
	if public == nil {
		driver.setNodeListenerActive(true)
		return nil
	}
	runtimeState := current
	runtimeState.SharedHTTPS = &gateway.SharedHTTPS{Address: public.Address, Port: public.HTTPSPort, CaddyAddress: dockerruntime.CaddyAlias, CaddyPort: 443}
	if err := driver.Runtime.Reconcile(ctx, runtimeState); err != nil {
		return err
	}
	if err := driver.Caddy.ApplyConfiguration(ctx, current, certificates); err != nil {
		return err
	}
	driver.setNodeListenerActive(true)
	return nil
}

func (driver *ManagedGatewayDriver) RestoreGatewayPublicBindings(ctx context.Context) error {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	driver.mu.RLock()
	current := driver.state.Sorted()
	certificates := append([]gateway.Certificate(nil), driver.certificates...)
	driver.mu.RUnlock()
	driver.setNodeListenerActive(false)
	if current.Revision == 0 {
		return nil
	}
	retiredLegacySharedHTTPS := current.SharedHTTPS != nil
	current.SharedHTTPS = nil
	if err := driver.Runtime.Reconcile(ctx, current); err != nil {
		return err
	}
	if err := driver.Caddy.ApplyConfiguration(ctx, current, certificates); err != nil {
		return err
	}
	if retiredLegacySharedHTTPS {
		driver.mu.Lock()
		driver.state = current
		driver.mu.Unlock()
	}
	return nil
}

// RestoreGatewayAfterNodeListenerFailure rolls back a failed first handoff to
// the exact previously applied Gateway state. Unlike an intentional empty
// node-listener revision, a failed cutover must retain legacy shared-443 routes
// until Center has verified their replacements.
func (driver *ManagedGatewayDriver) RestoreGatewayAfterNodeListenerFailure(ctx context.Context) error {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	driver.mu.RLock()
	current := driver.state.Sorted()
	certificates := append([]gateway.Certificate(nil), driver.certificates...)
	driver.mu.RUnlock()
	driver.setNodeListenerActive(false)
	if current.Revision == 0 {
		return nil
	}
	return driver.apply(ctx, current, certificates)
}

func (driver *ManagedGatewayDriver) setNodeListenerActive(active bool) {
	driver.mu.Lock()
	driver.nodeListener = active
	driver.mu.Unlock()
}

// RetainSystemRoutes removes application ingress while keeping the Center and
// bundled infrastructure reachable on a co-located control-plane host.
func (driver *ManagedGatewayDriver) RetainSystemRoutes(ctx context.Context) (bool, error) {
	driver.mutationMu.Lock()
	defer driver.mutationMu.Unlock()
	if driver.Caddy == nil {
		return false, errors.New("agent: managed gateway is not configured")
	}
	driver.mu.RLock()
	current := driver.state.Sorted()
	certificates := append([]gateway.Certificate(nil), driver.certificates...)
	driver.mu.RUnlock()
	listeners := make(map[string]gateway.Listener)
	referencedListeners := make(map[string]bool)
	hostnames := make(map[string]bool)
	next := gateway.DesiredState{Revision: current.Revision, Routes: []gateway.Route{}, Listeners: []gateway.Listener{}}
	for _, route := range current.Routes {
		if !route.System {
			continue
		}
		next.Routes = append(next.Routes, route)
		hostnames[route.Hostname] = true
		referencedListeners[route.ListenerKind] = true
	}
	if len(next.Routes) == 0 {
		return false, nil
	}
	for _, listener := range current.Listeners {
		listeners[listener.Kind] = listener
	}
	for kind := range referencedListeners {
		listener, exists := listeners[kind]
		if !exists {
			return false, fmt.Errorf("agent: system route references unavailable %s listener", kind)
		}
		next.Listeners = append(next.Listeners, listener)
	}
	if next.Revision < 1 {
		next.Revision = 1
	}
	keptCertificates := make([]gateway.Certificate, 0, len(certificates))
	for _, certificate := range certificates {
		for hostname := range hostnames {
			if gateway.CertificateCoversHostname(certificate, hostname) {
				keptCertificates = append(keptCertificates, certificate)
				break
			}
		}
	}
	if err := driver.apply(ctx, next.Sorted(), keptCertificates); err != nil {
		return false, fmt.Errorf("agent: retain system gateway routes: %w", err)
	}
	driver.mu.Lock()
	driver.state = next.Sorted()
	driver.certificates = keptCertificates
	driver.mu.Unlock()
	return true, nil
}

func (driver *ManagedGatewayDriver) ListRoutes(ctx context.Context) ([]gateway.Route, error) {
	return driver.Caddy.ListRoutes(ctx)
}

func (driver *ManagedGatewayDriver) CurrentConfiguration() (gateway.DesiredState, []gateway.Certificate) {
	driver.mu.RLock()
	defer driver.mu.RUnlock()
	return driver.state.Sorted(), append([]gateway.Certificate(nil), driver.certificates...)
}

func (driver *ManagedGatewayDriver) ApplyRoute(ctx context.Context, route gateway.Route) error {
	return driver.Caddy.ApplyRoute(ctx, route)
}

func (driver *ManagedGatewayDriver) DeleteRoute(ctx context.Context, routeID string) error {
	return driver.Caddy.DeleteRoute(ctx, routeID)
}

func (driver *ManagedGatewayDriver) GetRouteStatus(ctx context.Context, routeID string) (string, error) {
	return driver.Caddy.GetRouteStatus(ctx, routeID)
}

func (driver *ManagedGatewayDriver) Health(ctx context.Context) error {
	if driver.Caddy == nil || driver.Layer4 == nil || driver.Runtime == nil {
		return errors.New("agent: managed gateway is not configured")
	}
	if err := driver.Caddy.Health(ctx); err != nil {
		return err
	}
	driver.mu.RLock()
	shared := driver.state.SharedHTTPS != nil
	driver.mu.RUnlock()
	if shared {
		return driver.Layer4.Health(ctx)
	}
	return nil
}

type DockerLayer4Provisioner struct {
	Socket    string
	Image     string
	Container string
}

func (provisioner DockerLayer4Provisioner) Apply(ctx context.Context, desired gateway.SharedHTTPS) error {
	docker, err := provisioner.client()
	if err != nil {
		return err
	}
	defer docker.Close()
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return err
	}
	settings := provisioner.settings()
	configuration, err := haproxyConfiguration(desired)
	if err != nil {
		return err
	}
	configurationHash := haproxyConfigurationHash(configuration)
	pull, err := docker.ImagePull(ctx, settings.Image, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("agent: pull HAProxy image: %w", err)
	}
	_, _ = io.Copy(io.Discard, pull)
	_ = pull.Close()
	if _, err := docker.ContainerRemove(ctx, settings.Container, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: replace HAProxy container: %w", err)
	}
	created, err := docker.ContainerCreate(ctx, haproxyContainerCreateOptions(settings, desired, configuration, configurationHash))
	if err != nil {
		return fmt.Errorf("agent: create HAProxy container: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return fmt.Errorf("agent: start HAProxy container: %w", err)
	}
	return provisioner.waitHealthy(ctx, docker, created.ID)
}

func haproxyContainerCreateOptions(settings DockerLayer4Provisioner, desired gateway.SharedHTTPS, configuration []byte, configurationHash string) client.ContainerCreateOptions {
	port := dockernetwork.MustParsePort("443/tcp")
	return client.ContainerCreateOptions{
		Config: &container.Config{
			Image:      settings.Image,
			User:       "0:0",
			Entrypoint: []string{"/bin/sh", "-ec"},
			Cmd:        []string{haproxyBootstrapCommand},
			Env:        []string{haproxyConfigurationEnv + "=" + string(configuration)},
			Labels: map[string]string{
				gatewayruntime.ManagedLabel:   "true",
				gatewayruntime.ComponentLabel: gatewayruntime.Layer4ComponentLabel,
				layer4ConfigurationLabel:      configurationHash,
			},
			ExposedPorts: dockernetwork.PortSet{port: struct{}{}},
		},
		HostConfig: &container.HostConfig{
			RestartPolicy:  container.RestartPolicy{Name: container.RestartPolicyMode("unless-stopped")},
			NetworkMode:    container.NetworkMode(dockerruntime.NetworkName),
			PortBindings:   dockernetwork.PortMap{port: []dockernetwork.PortBinding{{HostIP: netip.MustParseAddr(desired.Address), HostPort: strconv.Itoa(desired.Port)}}},
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{"NET_BIND_SERVICE"},
			Tmpfs: map[string]string{
				"/run":                  "rw,noexec,nosuid,size=16m",
				haproxyConfigurationDir: "rw,noexec,nosuid,size=1m,mode=0700",
			},
			SecurityOpt: []string{"no-new-privileges:true"},
		},
		NetworkingConfig: dockerruntime.NetworkingConfig(dockerruntime.HAProxyAlias),
		Name:             settings.Container,
	}
}

func (provisioner DockerLayer4Provisioner) Remove(ctx context.Context) error {
	docker, err := provisioner.client()
	if err != nil {
		return err
	}
	defer docker.Close()
	if _, err := docker.ContainerRemove(ctx, provisioner.settings().Container, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("agent: remove HAProxy container: %w", err)
	}
	return nil
}

func (provisioner DockerLayer4Provisioner) Absent(ctx context.Context) error {
	docker, err := provisioner.client()
	if err != nil {
		return err
	}
	defer docker.Close()
	_, err = docker.ContainerInspect(ctx, provisioner.settings().Container, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: inspect removed HAProxy container: %w", err)
	}
	return errors.New("agent: HAProxy container still exists after removal")
}

func (provisioner DockerLayer4Provisioner) Health(ctx context.Context) error {
	docker, err := provisioner.client()
	if err != nil {
		return err
	}
	defer docker.Close()
	inspection, err := docker.ContainerInspect(ctx, provisioner.settings().Container, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("agent: inspect HAProxy container: %w", err)
	}
	if inspection.Container.State == nil || !inspection.Container.State.Running {
		return errors.New("agent: HAProxy gateway is not running")
	}
	return nil
}

func (provisioner DockerLayer4Provisioner) ConfigurationHash(ctx context.Context) (string, error) {
	docker, err := provisioner.client()
	if err != nil {
		return "", err
	}
	defer docker.Close()
	inspection, err := docker.ContainerInspect(ctx, provisioner.settings().Container, client.ContainerInspectOptions{})
	if err != nil {
		return "", fmt.Errorf("agent: inspect HAProxy configuration: %w", err)
	}
	if inspection.Container.Config == nil || inspection.Container.Config.Labels[gatewayruntime.ManagedLabel] != "true" || inspection.Container.Config.Labels[gatewayruntime.ComponentLabel] != gatewayruntime.Layer4ComponentLabel {
		return "", errors.New("agent: HAProxy container ownership is invalid")
	}
	expectedHash := strings.ToLower(strings.TrimSpace(inspection.Container.Config.Labels[layer4ConfigurationLabel]))
	decoded, err := hex.DecodeString(expectedHash)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("agent: HAProxy configuration read-back is invalid")
	}
	configuration, err := readLiveHAProxyConfiguration(ctx, docker, inspection.Container.ID)
	if err != nil {
		return "", fmt.Errorf("agent: read HAProxy configuration: %w", err)
	}
	actualHash := haproxyConfigurationHash(configuration)
	if actualHash != expectedHash {
		return "", errors.New("agent: live HAProxy configuration differs from its managed revision")
	}
	return actualHash, nil
}

func readLiveHAProxyConfiguration(ctx context.Context, docker *client.Client, containerID string) ([]byte, error) {
	execution, err := docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"/bin/cat", haproxyConfigurationPath},
		WorkingDir:   "/",
	})
	if err != nil {
		return nil, err
	}
	attached, err := docker.ExecAttach(ctx, execution.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, err
	}
	defer attached.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, io.LimitReader(attached.Reader, (1<<20)+(64<<10))); err != nil {
		return nil, err
	}
	inspection, err := docker.ExecInspect(ctx, execution.ID, client.ExecInspectOptions{})
	if err != nil {
		return nil, err
	}
	if inspection.Running || inspection.ExitCode != 0 {
		return nil, fmt.Errorf("configuration read exited with status %d", inspection.ExitCode)
	}
	if stdout.Len() < 1 || stdout.Len() > 1<<20 {
		return nil, errors.New("live configuration size is invalid")
	}
	return stdout.Bytes(), nil
}

func haproxyConfigurationHash(configuration []byte) string {
	digest := sha256.Sum256(configuration)
	return hex.EncodeToString(digest[:])
}

func (provisioner DockerLayer4Provisioner) waitHealthy(ctx context.Context, docker *client.Client, containerID string) error {
	ready, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspection, err := docker.ContainerInspect(ready, containerID, client.ContainerInspectOptions{})
		if err == nil && inspection.Container.State != nil {
			if inspection.Container.State.Running {
				return nil
			}
			if inspection.Container.State.Status == "exited" || inspection.Container.State.Status == "dead" {
				return fmt.Errorf("agent: HAProxy exited: %s", inspection.Container.State.Error)
			}
		}
		select {
		case <-ready.Done():
			return errors.New("agent: HAProxy did not become healthy")
		case <-ticker.C:
		}
	}
}

func (provisioner DockerLayer4Provisioner) client() (*client.Client, error) {
	socket := provisioner.Socket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return nil, fmt.Errorf("agent: connect Docker for HAProxy provisioning: %w", err)
	}
	return docker, nil
}

func (provisioner DockerLayer4Provisioner) settings() DockerLayer4Provisioner {
	if provisioner.Image == "" {
		provisioner.Image = DefaultHAProxyImage
	}
	if provisioner.Container == "" {
		provisioner.Container = defaultHAProxyContainer
	}
	return provisioner
}

func haproxyConfiguration(desired gateway.SharedHTTPS) ([]byte, error) {
	state := gateway.DesiredState{Revision: 1, Listeners: []gateway.Listener{{Kind: "public", Address: desired.Address, HTTPPort: 80, HTTPSPort: desired.Port}}, SharedHTTPS: &desired}
	if err := state.Validate(); err != nil {
		return nil, err
	}
	desired = *state.Sorted().SharedHTTPS
	var configuration strings.Builder
	configuration.WriteString("global\n  log stdout format raw local0\n  maxconn 4096\n\ndefaults\n  log global\n  mode tcp\n  option tcplog\n  timeout connect 10s\n  timeout client 1m\n  timeout server 1m\n\n")
	configuration.WriteString("frontend vastora-shared-https\n  bind ")
	configuration.WriteString(net.JoinHostPort("0.0.0.0", strconv.Itoa(desired.Port)))
	configuration.WriteString("\n  tcp-request inspect-delay 5s\n  tcp-request content accept if { req_ssl_hello_type 1 }\n")
	for index, route := range desired.Routes {
		configuration.WriteString(fmt.Sprintf("  use_backend vastora-raw-%d if { req.ssl_sni -i %s }\n", index, route.Hostname))
	}
	if desired.RejectUnmatched {
		configuration.WriteString("  default_backend vastora-reject\n\nbackend vastora-reject\n  server reject 127.0.0.1:1\n")
	} else {
		configuration.WriteString("  default_backend vastora-caddy\n\nbackend vastora-caddy\n")
		configuration.WriteString("  server caddy ")
		configuration.WriteString(net.JoinHostPort(desired.CaddyAddress, strconv.Itoa(desired.CaddyPort)))
		configuration.WriteString(" check\n")
	}
	for index, route := range desired.Routes {
		configuration.WriteString(fmt.Sprintf("\nbackend vastora-raw-%d\n", index))
		for upstreamIndex, upstream := range route.Upstreams {
			proxyProtocol := ""
			if route.ProxyProtocol == gateway.ProxyProtocolV2 {
				proxyProtocol = " send-proxy-v2"
			}
			configuration.WriteString(fmt.Sprintf("  server upstream-%d %s check%s\n", upstreamIndex, net.JoinHostPort(upstream.Address, strconv.Itoa(upstream.Port)), proxyProtocol))
		}
	}
	return []byte(configuration.String()), nil
}

type ManagedGatewayProvisioner struct {
	Caddy  GatewayProvisioner
	Layer4 Layer4Provisioner
	Driver *ManagedGatewayDriver
}

func (provisioner ManagedGatewayProvisioner) Ensure(ctx context.Context) error {
	if provisioner.Caddy == nil {
		return errors.New("agent: Caddy provisioner is not configured")
	}
	return provisioner.Caddy.Ensure(ctx)
}

func (provisioner ManagedGatewayProvisioner) Remove(ctx context.Context) error {
	if provisioner.Caddy == nil || provisioner.Layer4 == nil {
		return errors.New("agent: managed gateway provisioner is not configured")
	}
	if provisioner.Driver != nil {
		retained, err := provisioner.Driver.RetainSystemRoutes(ctx)
		if err != nil || retained {
			return err
		}
	}
	return provisioner.Caddy.Remove(ctx)
}
