package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/networking"
	"gopkg.in/yaml.v3"
)

const cpaConfigurationPath = "/CLIProxyAPI/config.yaml"

// Only identity/locations, never CPA configuration or credentials, are kept in
// the ownership receipt. A proxy IP alone is not a durable connector identity.
type cpaIngress struct {
	Address string             `json:"address"`
	Port    int                `json:"port"`
	Tunnel  *cpaTunnelIdentity `json:"tunnel,omitempty"`
	// Exact instance that must resume after a verified trust transition.
	ResumeContainerID string `json:"resumeContainerId,omitempty"`
}

type cpaTunnelIdentity struct {
	ContainerID string `json:"containerId"`
	NetworkID   string `json:"networkId"`
	EndpointID  string `json:"endpointId"`
	Address     string `json:"address"`
}

func (b *DockerPackageBackend) prepareCPAIngress(task DeploymentTask, receipt *InstanceResources, persist func() error) error {
	if task.AppKey != cpaKey || task.Manifest.Runtime == nil || !slices.Contains(task.Manifest.Runtime.RequiredCapabilities, "cpa-tunnel-identity") {
		return nil
	}
	if !networking.IsPrivateServiceAddress(task.ServiceAddress) {
		return errors.New("agent: CPA requires a confirmed private listener")
	}
	ip, err := netip.ParseAddr(task.ServiceAddress)
	if err != nil || !ip.Is4() || ip.IsLoopback() || ip.IsUnspecified() {
		return errors.New("agent: CPA listener cannot be wildcard or loopback")
	}
	if len(b.plans) != 1 || b.plans[0].logical != "cpa" || b.plans[0].options.HostConfig.NetworkMode != "host" {
		return errors.New("agent: CPA trusted ingress requires the reviewed host-network recipe")
	}
	service, err := runtimeService(task, "api")
	if err != nil {
		return err
	}
	port, err := serviceHostPort(task.Config, service)
	if err != nil {
		return err
	}
	policy := &cpaIngress{Address: task.ServiceAddress, Port: port}
	// An opaque upgrade snapshot may contain old/wide proxy settings. Replace
	// only network-owned fields, before any replacement process can start.
	b.plans[0].files, err = rewriteCPAArchive(b.plans[0].files, policy, nil)
	if err != nil {
		return err
	}
	receipt.CPAIngress = policy
	return persist()
}

func keeperPrivateEndpoint(retained, desired []string, address string) ([]string, error) {
	var next string
	for _, entry := range desired {
		if strings.HasPrefix(entry, "CPA_BASE_URL=") {
			next = entry
		}
	}
	u, err := url.Parse(strings.TrimPrefix(next, "CPA_BASE_URL="))
	if err != nil || next == "" || u.Scheme != "http" && u.Scheme != "https" || u.Hostname() != address || !networking.IsPrivateServiceAddress(address) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, errors.New("agent: Keeper requires its confirmed same-node private CPA endpoint")
	}
	out := slices.DeleteFunc(slices.Clone(retained), func(s string) bool { return strings.HasPrefix(s, "CPA_BASE_URL=") })
	return append(out, next), nil
}

func rewriteCPAConfiguration(raw []byte, policy *cpaIngress, tunnel *cpaTunnelIdentity) ([]byte, error) {
	if policy == nil || !networking.IsPrivateServiceAddress(policy.Address) || policy.Port < 1 || policy.Port > 65535 {
		return nil, errors.New("agent: invalid CPA ingress policy")
	}
	var cfg map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if len(raw) > packageConfigurationLimit || decoder.Decode(&cfg) != nil || cfg == nil {
		return nil, errors.New("agent: invalid CPA configuration")
	}
	var extra any
	if !errors.Is(decoder.Decode(&extra), io.EOF) {
		return nil, errors.New("agent: CPA configuration must contain one document")
	}
	// Preserve the application's existing supported file format and opaque
	// business settings; this is not a replacement initializer on upgrade.
	server := cfg
	if value, exists := cfg["server"]; exists {
		var ok bool
		server, ok = value.(map[string]any)
		if !ok || server == nil {
			return nil, errors.New("agent: invalid CPA server configuration")
		}
		// Do not leave conflicting legacy listener/proxy overrides behind.
		delete(cfg, "host")
		delete(cfg, "port")
		delete(cfg, "trusted-proxies")
	}
	proxies := []string{}
	if tunnel != nil {
		ip, err := netip.ParseAddr(tunnel.Address)
		if err != nil || !ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || tunnel.ContainerID == "" || tunnel.NetworkID == "" || tunnel.EndpointID == "" {
			return nil, errors.New("agent: invalid CPA tunnel identity")
		}
		proxies = append(proxies, netip.PrefixFrom(ip, ip.BitLen()).String())
	}
	server["host"], server["port"], server["trusted-proxies"] = policy.Address, policy.Port, proxies
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, errors.New("agent: CPA ingress configuration could not be encoded")
	}
	return data, nil
}

func rewriteCPAArchive(raw []byte, policy *cpaIngress, tunnel *cpaTunnelIdentity) ([]byte, error) {
	reader := tar.NewReader(bytes.NewReader(raw))
	var out bytes.Buffer
	writer := tar.NewWriter(&out)
	found := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header.Size < 0 || header.Size > packageConfigurationLimit || out.Len() > packageConfigurationLimit {
			return nil, errors.New("agent: invalid CPA configuration archive")
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, errors.New("agent: truncated CPA configuration archive")
		}
		copy := *header
		if strings.TrimPrefix(header.Name, "/") == strings.TrimPrefix(cpaConfigurationPath, "/") {
			if found || header.Typeflag != tar.TypeReg {
				return nil, errors.New("agent: invalid CPA configuration member")
			}
			found = true
			data, err = rewriteCPAConfiguration(data, policy, tunnel)
			if err != nil {
				return nil, err
			}
			copy.Size, copy.Mode = int64(len(data)), 0600
		}
		if err := writer.WriteHeader(&copy); err != nil {
			return nil, err
		}
		if _, err := writer.Write(data); err != nil {
			return nil, err
		}
	}
	if !found {
		return nil, errors.New("agent: CPA configuration is missing; refusing to initialize over an upgrade")
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

type cpaProxyManager struct {
	Docker         packageDockerEngine
	StateDirectory string
}

// The old Docker DNS origin disappears when CPA leaves bridge networking.
// Require the separately authorized Keeper update first, while the old CPA
// private published endpoint is still available. Never upgrade another app as
// a side effect of a CPA task.
func (m cpaProxyManager) checkKeeperOrigins(ctx context.Context, task DeploymentTask) error {
	address, addressErr := netip.ParseAddr(task.ServiceAddress)
	if addressErr != nil || address.IsLoopback() || !networking.IsPrivateServiceAddress(task.ServiceAddress) {
		return errors.New("agent: CPA migration requires a confirmed non-loopback private address")
	}
	engine, ok := m.Docker.(interface {
		ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error)
	})
	if !ok {
		return errors.New("agent: CPA migration requires Keeper inventory")
	}
	items, err := engine.ContainerList(ctx, client.ContainerListOptions{All: true, Limit: 1025})
	if err != nil || len(items.Items) >= 1025 {
		return errors.New("agent: CPA migration could not verify Keeper inventory")
	}
	service, err := runtimeService(task, "api")
	if err != nil {
		return err
	}
	port, err := serviceHostPort(task.Config, service)
	if err != nil {
		return err
	}
	for _, item := range items.Items {
		if item.Labels[applicationIdentityLabel] != keeperKey {
			continue
		}
		current, err := m.Docker.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil || current.Container.ID != item.ID || current.Container.Config == nil || validateApplicationResourceLabels(current.Container.Config.Labels, keeperKey, "keeper", "", anyApplicationDeployment) != nil {
			return errors.New("agent: Keeper ownership changed during CPA migration")
		}
		var origin string
		count := 0
		for _, entry := range current.Container.Config.Env {
			if strings.HasPrefix(entry, "CPA_BASE_URL=") {
				origin = strings.TrimPrefix(entry, "CPA_BASE_URL=")
				count++
			}
		}
		u, err := url.Parse(origin)
		if err != nil || count != 1 || u.Hostname() != task.ServiceAddress || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "http" && u.Scheme != "https" || u.Port() != strconv.Itoa(port) {
			return errors.New("agent: update Keeper to the existing same-node private CPA endpoint before switching CPA networking")
		}
	}
	return nil
}

// The source is a running, pinned, same-daemon connector on the exact owned
// network. Neither request headers, a subnet nor a caller-supplied IP grant trust.
// Both networks remain attached: Docker-name origins keep their original
// network, while host/private CPA requests use the verified dedicated gateway.
func (m cpaProxyManager) ownedTunnel(ctx context.Context) (*container.InspectResponse, error) {
	x, err := m.Docker.ContainerInspect(ctx, defaultCloudflaredContainer, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("agent: cannot inspect CPA tunnel source")
	}
	c := x.Container
	if c.Config == nil || c.Config.Labels[dockerruntime.ManagedLabel] != "true" || c.Config.Labels[dockerruntime.ComponentLabel] != "cloudflare-tunnel" || c.Config.Image != deployapi.CloudflaredImage || c.State == nil || !c.State.Running || c.HostConfig == nil || string(c.HostConfig.NetworkMode) != dockerruntime.NetworkName || c.NetworkSettings == nil || len(c.NetworkSettings.Networks) < 1 || len(c.NetworkSettings.Networks) > 2 {
		return nil, errors.New("agent: CPA tunnel ownership or runtime identity is unverified")
	}
	shared := c.NetworkSettings.Networks[dockerruntime.NetworkName]
	if shared == nil || shared.GwPriority != 0 || (len(c.NetworkSettings.Networks) == 2 && c.NetworkSettings.Networks[tunnelOriginNetwork] == nil) {
		return nil, errors.New("agent: CPA tunnel has an unexpected network or gateway")
	}
	engine, ok := m.Docker.(packageNetworkEngine)
	if !ok {
		return nil, errors.New("agent: CPA tunnel network evidence is unavailable")
	}
	runtime, err := engine.NetworkInspect(ctx, dockerruntime.NetworkName, client.NetworkInspectOptions{})
	if err != nil || runtime.Network.Driver != "bridge" || runtime.Network.ID == "" || runtime.Network.ID != shared.NetworkID || runtime.Network.Labels[dockerruntime.ManagedLabel] != "true" || runtime.Network.Labels[dockerruntime.ComponentLabel] != "runtime-network" || runtime.Network.Labels[dockerruntime.NetworkLabel] != dockerruntime.NetworkName {
		return nil, errors.New("agent: CPA shared tunnel network identity changed")
	}
	peer, exists := runtime.Network.Containers[c.ID]
	if c.ID == "" || !exists || shared.EndpointID == "" || peer.EndpointID != shared.EndpointID {
		return nil, errors.New("agent: CPA shared tunnel endpoint identity changed")
	}
	return &c, nil
}

func (m cpaProxyManager) checkOrigin(ctx context.Context, address string) error {
	ip, err := netip.ParseAddr(address)
	if err != nil || !ip.Is4() || !networking.IsPrivateServiceAddress(address) || ip.IsLoopback() {
		return errors.New("agent: CPA requires a confirmed private IPv4 origin")
	}
	engine, ok := m.Docker.(packageNetworkEngine)
	if !ok {
		return errors.New("agent: CPA origin network evidence is unavailable")
	}
	shared, err := engine.NetworkInspect(ctx, dockerruntime.NetworkName, client.NetworkInspectOptions{})
	if err != nil {
		return errors.New("agent: CPA shared origin routes cannot be inspected")
	}
	if len(shared.Network.IPAM.Config) == 0 {
		return errors.New("agent: CPA shared origin routes are missing")
	}
	for _, pool := range shared.Network.IPAM.Config {
		if pool.Subnet.IsValid() && pool.Subnet.Contains(ip) {
			return errors.New("agent: CPA origin overlaps the shared Docker network")
		}
	}
	return nil
}

// Only an explicitly authorized CPA package operation attaches this network.
// Ordinary Tunnel updates without a CPA receipt retain the original topology.
func (m cpaProxyManager) prepareTunnel(ctx context.Context) error {
	c, err := m.ownedTunnel(ctx)
	if err != nil || c == nil {
		return err
	}
	if c.NetworkSettings.Networks[tunnelOriginNetwork] != nil {
		_, err := m.tunnel(ctx)
		return err
	}
	engine, ok := m.Docker.(interface {
		dockerruntime.NetworkEngine
		NetworkConnect(context.Context, string, client.NetworkConnectOptions) (client.NetworkConnectResult, error)
	})
	if !ok {
		return errors.New("agent: CPA requires managed tunnel network attachment")
	}
	if err := dockerruntime.EnsureBridgeNetwork(ctx, engine, tunnelOriginNetwork, tunnelOriginNetworkComponent); err != nil {
		return err
	}
	isolated, err := engine.NetworkInspect(ctx, tunnelOriginNetwork, client.NetworkInspectOptions{})
	if err != nil || len(isolated.Network.Containers) != 0 {
		return errors.New("agent: CPA isolated origin network is occupied")
	}
	address, err := tunnelStaticAddress(isolated.Network)
	if err != nil {
		return err
	}
	if _, err := engine.NetworkConnect(ctx, isolated.Network.ID, client.NetworkConnectOptions{Container: c.ID, EndpointConfig: &network.EndpointSettings{GwPriority: 1, IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: address}}}); err != nil {
		return errors.New("agent: CPA tunnel private origin could not be attached")
	}
	actual, err := m.tunnel(ctx)
	if err != nil || actual == nil || actual.ContainerID != c.ID {
		return errors.New("agent: CPA tunnel identity changed during private attachment")
	}
	return nil
}

func (m cpaProxyManager) tunnel(ctx context.Context) (*cpaTunnelIdentity, error) {
	c, err := m.ownedTunnel(ctx)
	if err != nil || c == nil {
		return nil, err
	}
	endpoint := c.NetworkSettings.Networks[tunnelOriginNetwork]
	if endpoint == nil || endpoint.GwPriority != 1 {
		return nil, errors.New("agent: CPA tunnel private gateway is unverified")
	}
	engine := m.Docker.(packageNetworkEngine) // Verified by ownedTunnel.
	isolated, err := engine.NetworkInspect(ctx, tunnelOriginNetwork, client.NetworkInspectOptions{})
	if err != nil || isolated.Network.Driver != "bridge" || isolated.Network.Labels[dockerruntime.ManagedLabel] != "true" || isolated.Network.Labels[dockerruntime.ComponentLabel] != tunnelOriginNetworkComponent || isolated.Network.Labels[dockerruntime.NetworkLabel] != tunnelOriginNetwork || isolated.Network.ID != endpoint.NetworkID || len(isolated.Network.Containers) != 1 {
		return nil, errors.New("agent: CPA tunnel private network ownership changed")
	}
	peer, exists := isolated.Network.Containers[c.ID]
	if !exists || peer.EndpointID != endpoint.EndpointID || endpoint.EndpointID == "" {
		return nil, errors.New("agent: CPA tunnel endpoint identity changed")
	}
	ip := endpoint.IPAddress
	static, staticErr := tunnelStaticAddress(isolated.Network)
	if !ip.IsValid() || !ip.IsPrivate() || ip.IsLoopback() || staticErr != nil || ip != static || endpoint.IPAMConfig == nil || endpoint.IPAMConfig.IPv4Address != static {
		return nil, errors.New("agent: CPA tunnel requires a pinned private bridge address")
	}
	if !peer.IPv4Address.IsValid() || peer.IPv4Address.Addr() != ip {
		return nil, errors.New("agent: CPA tunnel address ownership changed")
	}
	return &cpaTunnelIdentity{ContainerID: c.ID, NetworkID: endpoint.NetworkID, EndpointID: endpoint.EndpointID, Address: ip.String()}, nil
}

func (m cpaProxyManager) sync(ctx context.Context, receipt *InstanceResources, grant bool, persist func() error) error {
	if receipt.CPAIngress == nil {
		return nil
	}
	if receipt.AppKey != cpaKey {
		return errors.New("agent: CPA ingress receipt belongs to another application")
	}
	resource := resourceNamed(receipt, "container", "cpa")
	if resource == nil {
		return errors.New("agent: CPA ingress container receipt missing")
	}
	current, exists, err := inspectOwnedApplicationContainer(ctx, m.Docker, resource.Name, cpaKey, resource.Component, receipt.ApplicationID, anyApplicationDeployment)
	if err != nil || !exists || current.Container.ID != resource.ID || current.Container.HostConfig == nil || current.Container.HostConfig.NetworkMode != "host" {
		return errors.New("agent: CPA ingress container ownership changed")
	}
	var identity *cpaTunnelIdentity
	var identityErr error
	if grant {
		identityErr = m.checkOrigin(ctx, receipt.CPAIngress.Address)
		if identityErr == nil {
			identity, identityErr = m.tunnel(ctx)
		}
		if identityErr != nil {
			identity = nil
		}
	}
	if receipt.CPAIngress.ResumeContainerID != "" && receipt.CPAIngress.ResumeContainerID != resource.ID {
		return errors.New("agent: CPA pending resume belongs to another container")
	}
	running := current.Container.State != nil && current.Container.State.Running
	if running && receipt.CPAIngress.ResumeContainerID == "" {
		receipt.CPAIngress.ResumeContainerID = resource.ID
		if err := persist(); err != nil {
			receipt.CPAIngress.ResumeContainerID = ""
			return err
		}
	}
	resume := receipt.CPAIngress.ResumeContainerID == resource.ID
	if running {
		timeout := 30
		if _, err := m.Docker.ContainerStop(ctx, resource.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
			return errors.New("agent: CPA could not stop for proxy trust update")
		}
	}
	// Do not resume on failure: an unverified configuration must not restart.
	copied, err := m.Docker.CopyFromContainer(ctx, resource.ID, client.CopyFromContainerOptions{SourcePath: cpaConfigurationPath})
	if err != nil || copied.Content == nil {
		return errors.New("agent: CPA configuration cannot be read for proxy trust update")
	}
	raw, header, readErr := readSingleConfiguration(copied.Content, "config.yaml")
	closeErr := copied.Content.Close()
	if readErr != nil || closeErr != nil {
		return errors.New("agent: CPA proxy configuration is unsafe")
	}
	data, err := rewriteCPAConfiguration(raw, receipt.CPAIngress, identity)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	writer := tar.NewWriter(&out)
	copy := *header
	copy.Name, copy.Size, copy.Mode = strings.TrimPrefix(cpaConfigurationPath, "/"), int64(len(data)), 0600
	if err := writer.WriteHeader(&copy); err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if _, err := m.Docker.CopyToContainer(ctx, resource.ID, client.CopyToContainerOptions{DestinationPath: "/", Content: bytes.NewReader(out.Bytes())}); err != nil {
		return errors.New("agent: CPA proxy trust could not be updated")
	}
	readback, err := m.Docker.CopyFromContainer(ctx, resource.ID, client.CopyFromContainerOptions{SourcePath: cpaConfigurationPath})
	if err != nil || readback.Content == nil {
		return errors.New("agent: CPA proxy trust read-back unavailable")
	}
	verified, _, verifyErr := readSingleConfiguration(readback.Content, "config.yaml")
	closeErr = readback.Content.Close()
	if verifyErr != nil || closeErr != nil || !bytes.Equal(verified, data) {
		return errors.New("agent: CPA proxy trust read-back differs")
	}
	receipt.CPAIngress.Tunnel = identity
	if err := persist(); err != nil {
		return err
	}
	if identityErr != nil {
		return identityErr
	}
	if identity != nil {
		actual, err := m.tunnel(ctx)
		if err != nil || actual == nil || *actual != *identity {
			return errors.New("agent: CPA tunnel changed while proxy trust was applied")
		}
	}
	if resume {
		if _, err := m.Docker.ContainerStart(ctx, resource.ID, client.ContainerStartOptions{}); err != nil {
			return errors.New("agent: CPA could not resume after proxy trust update")
		}
		x, err := m.Docker.ContainerInspect(ctx, resource.ID, client.ContainerInspectOptions{})
		if err != nil || x.Container.State == nil || !x.Container.State.Running {
			return errors.New("agent: CPA did not resume after proxy trust update")
		}
		resource.StartedAt = x.Container.State.StartedAt
		receipt.CPAIngress.ResumeContainerID = ""
		return persist()
	}
	return nil
}

// Discover only the existing, protected local resource receipts.
func (m cpaProxyManager) receipts() ([]*InstanceResources, error) {
	if m.StateDirectory == "" {
		return nil, errors.New("agent: CPA tunnel lifecycle requires the application state directory")
	}
	base := filepath.Join(m.StateDirectory, "packages")
	if err := checkPackageParents(filepath.Join(base, "member")); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(entries) > 1024 {
		return nil, errors.New("agent: too many application receipts for tunnel transition")
	}
	var receipts []*InstanceResources
	executor := PackageExecutor{StateDirectory: m.StateDirectory}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "vastora-pkg-") {
			continue
		}
		p := filepath.Join(base, entry.Name(), "resources.json")
		if err := checkPackageParents(p); err != nil {
			return nil, err
		}
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4<<20 {
			return nil, errors.New("agent: unsafe application receipt during tunnel transition")
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		var hint InstanceResources
		if json.Unmarshal(raw, &hint) != nil || packageIdentity(hint.ApplicationID) != entry.Name() {
			return nil, errors.New("agent: invalid application receipt during tunnel transition")
		}
		if hint.CPAIngress == nil || hint.State == "removed" {
			continue
		}
		r, err := executor.ReadResources(hint.ApplicationID)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, r)
	}
	return receipts, nil
}

// Revoke every owned CPA before releasing any connector endpoint.
// A failure retains both its restart intent and the old endpoint allocation.
func (m cpaProxyManager) all(ctx context.Context, grant bool) error {
	receipts, err := m.receipts()
	if err != nil {
		return err
	}
	executor := PackageExecutor{StateDirectory: m.StateDirectory}
	for _, receipt := range receipts {
		if err := m.sync(ctx, receipt, grant, func() error { return executor.save(receipt) }); err != nil {
			return err
		}
	}
	return nil
}
