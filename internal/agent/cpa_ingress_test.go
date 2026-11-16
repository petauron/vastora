package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
	"gopkg.in/yaml.v3"
)

type cpaDocker struct {
	*fakePackageDocker
	net          client.NetworkInspectResult
	shared       client.NetworkInspectResult
	config       []byte
	failCopy     bool
	removeTunnel bool
}

func cpaFixture(t *testing.T) (*cpaDocker, cpaProxyManager, *InstanceResources) {
	t.Helper()
	e := &cpaDocker{fakePackageDocker: newPackageDocker(), config: []byte("server:\n  host: 0.0.0.0\n  port: 8317\n  trusted-proxies: [0.0.0.0/0]\naccess:\n  api-keys: [example-key]\nproviders: [{name: example, enabled: true}]\n")}
	r := &InstanceResources{Version: 1, ApplicationID: "cpa-example", AppKey: cpaKey, Runtime: "docker", State: "ready", CPAIngress: &cpaIngress{Address: "100.64.0.2", Port: 8317}, Resources: []RuntimeResource{{Kind: "container", LogicalName: "cpa", Name: "owned-cpa", ID: "cpa-id", Component: "cpa"}}}
	e.containers["owned-cpa"] = client.ContainerInspectResult{Container: container.InspectResponse{ID: "cpa-id", Name: "owned-cpa", Config: &container.Config{Labels: applicationResourceLabels(cpaKey, "cpa", r.ApplicationID, "install")}, HostConfig: &container.HostConfig{NetworkMode: "host"}, State: &container.State{Running: true}}}
	ip := netip.MustParseAddr("172.23.0.2")
	e.containers[defaultCloudflaredContainer] = client.ContainerInspectResult{Container: container.InspectResponse{ID: "tunnel-id", Name: defaultCloudflaredContainer, Config: &container.Config{Image: deployapi.CloudflaredImage, Labels: map[string]string{dockerruntime.ManagedLabel: "true", dockerruntime.ComponentLabel: "cloudflare-tunnel"}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(dockerruntime.NetworkName)}, State: &container.State{Running: true}, NetworkSettings: &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{dockerruntime.NetworkName: {NetworkID: "shared-id", EndpointID: "shared-endpoint", IPAddress: netip.MustParseAddr("172.22.0.2")}, tunnelOriginNetwork: {NetworkID: "network-id", EndpointID: "endpoint-id", GwPriority: 1, IPAddress: ip, IPAMConfig: &network.EndpointIPAMConfig{IPv4Address: ip}}}}}}
	e.net = client.NetworkInspectResult{Network: network.Inspect{Network: network.Network{ID: "network-id", Driver: "bridge", IPAM: network.IPAM{Config: []network.IPAMConfig{{Subnet: netip.MustParsePrefix("172.23.0.0/16"), Gateway: netip.MustParseAddr("172.23.0.1")}}}, Labels: map[string]string{dockerruntime.ManagedLabel: "true", dockerruntime.ComponentLabel: tunnelOriginNetworkComponent, dockerruntime.NetworkLabel: tunnelOriginNetwork}}, Containers: map[string]network.EndpointResource{"tunnel-id": {EndpointID: "endpoint-id", IPv4Address: netip.PrefixFrom(ip, 16)}}}}
	e.shared = client.NetworkInspectResult{Network: network.Inspect{Network: network.Network{ID: "shared-id", Driver: "bridge", IPAM: network.IPAM{Config: []network.IPAMConfig{{Subnet: netip.MustParsePrefix("172.22.0.0/16"), Gateway: netip.MustParseAddr("172.22.0.1")}}}, Labels: map[string]string{dockerruntime.ManagedLabel: "true", dockerruntime.ComponentLabel: "runtime-network", dockerruntime.NetworkLabel: dockerruntime.NetworkName}}, Containers: map[string]network.EndpointResource{"tunnel-id": {EndpointID: "shared-endpoint", IPv4Address: netip.MustParsePrefix("172.22.0.2/16")}}}}
	dir := packageTestDirectory(t)
	if err := (PackageExecutor{StateDirectory: dir}).save(r); err != nil {
		t.Fatal(err)
	}
	return e, cpaProxyManager{Docker: e, StateDirectory: dir}, r
}

func (e *cpaDocker) NetworkInspect(_ context.Context, name string, _ client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	if name == dockerruntime.NetworkName {
		return e.shared, nil
	}
	return e.net, nil
}
func (e *cpaDocker) NetworkCreate(context.Context, string, client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	return client.NetworkCreateResult{}, errors.New("unexpected create")
}
func (e *cpaDocker) NetworkRemove(context.Context, string, client.NetworkRemoveOptions) (client.NetworkRemoveResult, error) {
	return client.NetworkRemoveResult{}, errors.New("unexpected remove")
}
func (e *cpaDocker) CopyFromContainer(_ context.Context, id string, o client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	if o.SourcePath != cpaConfigurationPath {
		return client.CopyFromContainerResult{}, errors.New("unexpected read")
	}
	x, err := e.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
	if err != nil {
		return client.CopyFromContainerResult{}, err
	}
	if x.Container.State.Running {
		return client.CopyFromContainerResult{}, errors.New("configuration writer not stopped")
	}
	return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(configurationTar("config.yaml", e.config, tar.TypeReg)))}, nil
}

func (e *cpaDocker) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	result := client.ContainerListResult{}
	for _, x := range e.containers {
		result.Items = append(result.Items, container.Summary{ID: x.Container.ID, Labels: x.Container.Config.Labels})
	}
	return result, nil
}

func TestCPAMigrationRequiresKeeperPrivateOriginBeforeAnyStop(t *testing.T) {
	e, m, _ := cpaFixture(t)
	task := packageTestTask(t)
	task.ServiceAddress = "100.64.0.2"
	task.Manifest.Services = []catalog.Service{{Name: "api", Protocol: "http", ContainerPort: 8317, DefaultHostPort: 8317}}
	x := client.ContainerInspectResult{Container: container.InspectResponse{ID: "keeper-id", Config: &container.Config{Labels: applicationResourceLabels(keeperKey, "keeper", "keeper-example", "install"), Env: []string{"CPA_BASE_URL=http://cpa:8317"}}}}
	e.containers["keeper"] = x
	if err := m.checkKeeperOrigins(context.Background(), task); err == nil || len(e.calls) != 0 {
		t.Fatal("CPA switched before its dependent origin was prepared")
	}
	x.Container.Config.Env = []string{"CPA_BASE_URL=http://100.64.0.2:8317"}
	e.containers["keeper"] = x
	if err := m.checkKeeperOrigins(context.Background(), task); err != nil {
		t.Fatal(err)
	}
}

func TestCPAMigrationRefusesUnownedConnectorBeforeAnyStop(t *testing.T) {
	e, m, _ := cpaFixture(t)
	x := e.containers[defaultCloudflaredContainer]
	x.Container.Config.Labels[dockerruntime.ManagedLabel] = "false"
	e.containers[defaultCloudflaredContainer] = x
	task := reviewedCPATask(t)
	b := &DockerPackageBackend{Docker: e, StateDirectory: m.StateDirectory}
	if err := b.Prepare(context.Background(), task, &InstanceResources{ApplicationID: task.ApplicationID}); err == nil || len(e.calls) != 0 {
		t.Fatal("CPA migration touched runtime before connector preparation")
	}
}
func (e *cpaDocker) CopyToContainer(_ context.Context, id string, o client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	if e.failCopy {
		return client.CopyToContainerResult{}, errors.New("injected copy failure")
	}
	reader := tar.NewReader(o.Content)
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return client.CopyToContainerResult{}, err
		}
		if h.Name == strings.TrimPrefix(cpaConfigurationPath, "/") {
			e.config, err = io.ReadAll(reader)
			if err != nil {
				return client.CopyToContainerResult{}, err
			}
		}
	}
	e.calls = append(e.calls, "write-proxy-config")
	return client.CopyToContainerResult{}, nil
}
func (e *cpaDocker) ContainerRemove(ctx context.Context, id string, o client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	if id == defaultCloudflaredContainer || id == "tunnel-id" {
		var cfg map[string]any
		_ = yaml.Unmarshal(e.config, &cfg)
		if len(cfg["server"].(map[string]any)["trusted-proxies"].([]any)) != 0 {
			return client.ContainerRemoveResult{}, errors.New("old proxy trust still active")
		}
		e.removeTunnel = true
		delete(e.net.Network.Containers, "tunnel-id")
		delete(e.shared.Network.Containers, "tunnel-id")
	}
	return e.fakePackageDocker.ContainerRemove(ctx, id, o)
}

func (e *cpaDocker) ContainerCreate(ctx context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	r, err := e.fakePackageDocker.ContainerCreate(ctx, o)
	if err != nil || o.Name != defaultCloudflaredContainer {
		return r, err
	}
	ep := o.NetworkingConfig.EndpointsConfig[tunnelOriginNetwork]
	if ep == nil {
		shared := *o.NetworkingConfig.EndpointsConfig[dockerruntime.NetworkName]
		shared.NetworkID, shared.EndpointID, shared.IPAddress = "shared-id", "shared-endpoint", netip.MustParseAddr("172.22.0.2")
		x := e.containers[o.Name]
		x.Container.NetworkSettings = &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{dockerruntime.NetworkName: &shared}}
		e.containers[o.Name] = x
		e.shared.Network.Containers[r.ID] = network.EndpointResource{EndpointID: shared.EndpointID, IPv4Address: netip.PrefixFrom(shared.IPAddress, 16)}
		return r, nil
	}
	if ep.IPAMConfig == nil {
		return r, errors.New("missing isolated static endpoint")
	}
	copy := *ep
	copy.IPAddress = ep.IPAMConfig.IPv4Address
	copy.NetworkID = "network-id"
	copy.EndpointID = "fresh-endpoint"
	x := e.containers[o.Name]
	shared := *o.NetworkingConfig.EndpointsConfig[dockerruntime.NetworkName]
	shared.NetworkID, shared.EndpointID, shared.IPAddress = "shared-id", "shared-endpoint", netip.MustParseAddr("172.22.0.2")
	x.Container.NetworkSettings = &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{tunnelOriginNetwork: &copy, dockerruntime.NetworkName: &shared}}
	e.containers[o.Name] = x
	e.net.Network.Containers[r.ID] = network.EndpointResource{EndpointID: copy.EndpointID, IPv4Address: netip.PrefixFrom(copy.IPAddress, 16)}
	e.shared.Network.Containers[r.ID] = network.EndpointResource{EndpointID: shared.EndpointID, IPv4Address: netip.PrefixFrom(shared.IPAddress, 16)}
	return r, nil
}

func TestCPATunnelReplacementUsesIsolatedStaticEndpointAndRebinds(t *testing.T) {
	e, m, r := cpaFixture(t)
	state := TunnelDesiredState{Revision: 2, Status: "running", Image: deployapi.CloudflaredImage, Token: "example-token", Ingress: []TunnelIngress{{Hostname: "cpa.example.invalid", Service: "http://100.64.0.2:8317"}, {Hostname: "panel.example.invalid", Service: "http://native:2053"}}}
	if err := (DockerTunnelProvisioner{StateDirectory: m.StateDirectory}).apply(context.Background(), state, e, defaultCloudflaredContainer); err != nil {
		t.Fatal(err)
	}
	if !e.removeTunnel || len(e.created) != 1 || e.created[0].HostConfig.NetworkMode != container.NetworkMode(dockerruntime.NetworkName) || len(e.created[0].NetworkingConfig.EndpointsConfig) != 2 {
		t.Fatal("connector did not move through the isolated network")
	}
	updated, err := (PackageExecutor{StateDirectory: m.StateDirectory}).ReadResources(r.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CPAIngress.Tunnel == nil || updated.CPAIngress.Tunnel.ContainerID == "tunnel-id" || updated.CPAIngress.Tunnel.EndpointID != "fresh-endpoint" {
		t.Fatal("old connector identity was reused")
	}
	if e.created[0].NetworkingConfig.EndpointsConfig[tunnelOriginNetwork].IPAMConfig.IPv4Address != netip.MustParseAddr("172.23.0.2") {
		t.Fatal("static address did not come from the actual owned pool")
	}
}

func TestCPATunnelRejectsSharedNetworkAndDynamicAddress(t *testing.T) {
	for _, mutate := range []func(*cpaDocker){
		func(e *cpaDocker) {
			e.net.Network.Containers["foreign"] = network.EndpointResource{EndpointID: "foreign-endpoint"}
		},
		func(e *cpaDocker) {
			x := e.containers[defaultCloudflaredContainer]
			x.Container.NetworkSettings.Networks[tunnelOriginNetwork].IPAMConfig = nil
			e.containers[defaultCloudflaredContainer] = x
		},
		func(e *cpaDocker) { e.net.Network.IPAM.Config = nil },
	} {
		e, m, _ := cpaFixture(t)
		mutate(e)
		if _, err := m.tunnel(context.Background()); err == nil {
			t.Fatal("shared or unpinned address accepted")
		}
	}
}

func decodedCPA(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCPAConfigurationPreservesBusinessAndTrustsOneExactPeer(t *testing.T) {
	e, m, r := cpaFixture(t)
	original := decodedCPA(t, e.config)
	if err := m.sync(context.Background(), r, true, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	got := decodedCPA(t, e.config)
	server := got["server"].(map[string]any)
	if server["host"] != "100.64.0.2" || server["port"] != 8317 {
		t.Fatal("private listener changed")
	}
	peers := server["trusted-proxies"].([]any)
	if len(peers) != 1 || peers[0] != "172.23.0.2/32" || r.CPAIngress.Tunnel.ContainerID != "tunnel-id" {
		t.Fatal("proxy trust not tied to exact endpoint")
	}
	a, _ := json.Marshal(original["access"])
	b, _ := json.Marshal(got["access"])
	if !bytes.Equal(a, b) || got["providers"] == nil {
		t.Fatal("business configuration overwritten")
	}
	if !slices.Equal(e.calls, []string{"stop", "write-proxy-config", "start"}) {
		t.Fatalf("unsafe restart order: %v", e.calls)
	}
	if err := m.sync(context.Background(), r, false, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if r.CPAIngress.Tunnel != nil || len(decodedCPA(t, e.config)["server"].(map[string]any)["trusted-proxies"].([]any)) != 0 {
		t.Fatal("old trust retained")
	}
}

func TestCPATunnelAddressReuseAndOwnershipFailureRevokeBeforeResume(t *testing.T) {
	for _, mutate := range []func(*cpaDocker){
		func(e *cpaDocker) {
			x := e.containers[defaultCloudflaredContainer]
			x.Container.ID = "reused-name"
			e.containers[defaultCloudflaredContainer] = x
		},
		func(e *cpaDocker) {
			peer := e.net.Network.Containers["tunnel-id"]
			peer.EndpointID = "reused-endpoint"
			e.net.Network.Containers["tunnel-id"] = peer
		},
		func(e *cpaDocker) { e.net.Network.Labels[dockerruntime.ManagedLabel] = "false" },
		func(e *cpaDocker) {
			x := e.containers[defaultCloudflaredContainer]
			x.Container.Config.Image = "unreviewed:latest"
			e.containers[defaultCloudflaredContainer] = x
		},
	} {
		e, m, r := cpaFixture(t)
		mutate(e)
		if err := m.sync(context.Background(), r, true, func() error { return nil }); err == nil {
			t.Fatal("unverified source trusted")
		}
		if e.containers["owned-cpa"].Container.State.Running || r.CPAIngress.Tunnel != nil || len(decodedCPA(t, e.config)["server"].(map[string]any)["trusted-proxies"].([]any)) != 0 {
			t.Fatal("failed identity kept trust or resumed")
		}
	}
}

func TestCPATunnelRemovalRequiresDurableRevocation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		e, m, _ := cpaFixture(t)
		e.failCopy = fail
		err := (DockerTunnelProvisioner{StateDirectory: m.StateDirectory}).apply(context.Background(), TunnelDesiredState{Revision: 2, Status: "stopped"}, e, defaultCloudflaredContainer)
		if fail {
			if err == nil || e.removeTunnel {
				t.Fatal("connector released despite failed trust revocation")
			}
		} else if err != nil || !e.removeTunnel {
			t.Fatalf("safe stop failed: %v", err)
		}
	}
}

func TestCPAReplayAfterPersistFailureDoesNotReleaseOldEndpoint(t *testing.T) {
	e, m, r := cpaFixture(t)
	if err := m.sync(context.Background(), r, false, func() error { return errors.New("disk failure") }); err == nil {
		t.Fatal("lost persistence ignored")
	}
	if !e.containers["owned-cpa"].Container.State.Running || e.removeTunnel {
		t.Fatal("failed pre-stop intent persistence stopped CPA or released endpoint")
	}
	if err := m.all(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if len(decodedCPA(t, e.config)["server"].(map[string]any)["trusted-proxies"].([]any)) != 0 {
		t.Fatal("restart from durable receipt restored old trust")
	}
}

func TestCPAConfigurationRefusesAmbiguityAndArchiveMissingFile(t *testing.T) {
	p := &cpaIngress{Address: "100.64.0.2", Port: 8317}
	for _, raw := range []string{"server: []", "server: {}\nserver: {}", "server: {}\n---\nserver: {}"} {
		if _, err := rewriteCPAConfiguration([]byte(raw), p, nil); err == nil {
			t.Fatal("ambiguous config accepted")
		}
	}
	if _, err := rewriteCPAArchive(configurationTar("unrelated.json", []byte(`{}`), tar.TypeReg), p, nil); err == nil {
		t.Fatal("missing existing config initialized")
	}
	legacy, err := rewriteCPAConfiguration([]byte("host: 0.0.0.0\nport: 8317\napi-keys: [example]\n"), p, nil)
	if err != nil || decodedCPA(t, legacy)["api-keys"] == nil {
		t.Fatal("opaque existing app settings lost")
	}
}

func TestKeeperUpgradeChangesOnlySameNodeCPAOrigin(t *testing.T) {
	retained := []string{"CPA_BASE_URL=http://cpa:8317", "CPA_MANAGEMENT_KEY=example-key", "CUSTOM=preserved"}
	out, err := keeperPrivateEndpoint(retained, []string{"CPA_BASE_URL=http://100.64.0.2:8317"}, "100.64.0.2")
	if err != nil || !slices.Contains(out, "CPA_BASE_URL=http://100.64.0.2:8317") || !slices.Contains(out, "CPA_MANAGEMENT_KEY=example-key") || !slices.Contains(out, "CUSTOM=preserved") {
		t.Fatal("Keeper origin update lost business state", err)
	}
	for _, origin := range []string{"http://100.64.0.3:8317", "http://example.invalid:8317", "http://user:password@100.64.0.2:8317"} {
		if _, err := keeperPrivateEndpoint(retained, []string{"CPA_BASE_URL=" + origin}, "100.64.0.2"); err == nil {
			t.Fatal("unverified Keeper origin accepted")
		}
	}
}

func reviewedCPATask(t *testing.T) DeploymentTask {
	t.Helper()
	task := packageTestTask(t)
	task.AppKey = cpaKey
	task.Manifest.ID = "cpa"
	task.ServiceAddress = "100.64.0.2"
	task.Manifest.Services = []catalog.Service{{Name: "api", Protocol: "http", ContainerPort: 8317, DefaultHostPort: 8317}}
	task.Manifest.Runtime.RequiredCapabilities = []string{"root", "host-network", "cpa-tunnel-identity"}
	c := &task.Manifest.Runtime.Docker.Containers[0]
	c.Name = "cpa"
	c.HostNetwork = true
	c.Ports = []catalog.Port{{Service: "api"}}
	c.ConfigFiles = []catalog.ConfigFile{{Path: cpaConfigurationPath, Format: "json", Values: map[string]catalog.Value{"server": {Object: map[string]catalog.Value{"host": {Runtime: &catalog.RuntimeValue{Kind: "private-address"}}, "port": literalValue(`8317`)}}}}}
	packageTaskDigest(t, &task)
	task.AuthorizedCapabilities = slices.Clone(task.Manifest.Runtime.RequiredCapabilities)
	return task
}

func TestCPAReviewedRecipeCompilesHostWithoutPortPublication(t *testing.T) {
	task := reviewedCPATask(t)
	r := &InstanceResources{ApplicationID: task.ApplicationID}
	plans, err := compileDockerPackage(task, r)
	if err != nil {
		t.Fatal(err)
	}
	b := &DockerPackageBackend{plans: plans}
	if err := b.prepareCPAIngress(task, r, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(b.plans[0].options.HostConfig.PortBindings) != 0 || r.CPAIngress.Address != "100.64.0.2" {
		t.Fatal("host network uses Docker DNAT or wrong binding")
	}
	for _, address := range []string{"0.0.0.0", "::", "127.0.0.1", "203.0.113.1"} {
		task.ServiceAddress = address
		if err := b.prepareCPAIngress(task, r, func() error { return nil }); err == nil {
			t.Fatal("unsafe CPA listener accepted")
		}
	}
}

func TestCPAInstallAndUpgradeKeepBusinessConfigurationWithCurrentTunnel(t *testing.T) {
	e, m, _ := cpaFixture(t)
	task := reviewedCPATask(t)
	backend := &DockerPackageBackend{Docker: e, StateDirectory: m.StateDirectory, CheckHealth: func(context.Context, string, int, catalog.Service) error { return nil }}
	executor := PackageExecutor{StateDirectory: m.StateDirectory, Backend: backend}
	installed, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Resources.State != "ready" || installed.Resources.CPAIngress.Tunnel.ContainerID != "tunnel-id" {
		t.Fatal("install has no verified connector identity")
	}
	cfg := decodedCPA(t, e.config)
	cfg["custom-provider"] = map[string]any{"preserved": true}
	e.config, _ = yaml.Marshal(cfg)
	task.Operation = "upgrade"
	task.ID = "cpa-upgrade"
	task.Manifest.PackageRevision++
	packageTaskDigest(t, &task)
	upgraded, err := executor.Deploy(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if upgraded.Resources.State != "ready" || decodedCPA(t, e.config)["custom-provider"] == nil {
		t.Fatal("CPA upgrade lost business configuration")
	}
	if upgraded.Resources.CPAIngress.Tunnel.Address != "172.23.0.2" {
		t.Fatal("CPA upgrade lost exact connector binding")
	}
}

func (e *cpaDocker) NetworkConnect(_ context.Context, id string, options client.NetworkConnectOptions) (client.NetworkConnectResult, error) {
	c := e.containers[defaultCloudflaredContainer]
	if id != e.net.Network.ID || options.Container != c.Container.ID || options.EndpointConfig == nil || options.EndpointConfig.IPAMConfig == nil {
		return client.NetworkConnectResult{}, errors.New("unexpected attachment")
	}
	endpoint := *options.EndpointConfig
	endpoint.NetworkID, endpoint.EndpointID, endpoint.IPAddress = id, "attached-private-endpoint", endpoint.IPAMConfig.IPv4Address
	c.Container.NetworkSettings.Networks[tunnelOriginNetwork] = &endpoint
	e.containers[defaultCloudflaredContainer] = c
	e.net.Network.Containers[c.Container.ID] = network.EndpointResource{EndpointID: endpoint.EndpointID, IPv4Address: netip.PrefixFrom(endpoint.IPAddress, 16)}
	e.calls = append(e.calls, "connect-private")
	return client.NetworkConnectResult{}, nil
}

func TestCPATunnelWithoutIngressKeepsOrdinaryDNSOrigins(t *testing.T) {
	e, m, r := cpaFixture(t)
	cfg, err := rewriteCPAConfiguration(e.config, r.CPAIngress, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.config = cfg
	r.CPAIngress = nil
	if err := (PackageExecutor{StateDirectory: m.StateDirectory}).save(r); err != nil {
		t.Fatal(err)
	}
	state := TunnelDesiredState{Revision: 2, Status: "running", Image: deployapi.CloudflaredImage, Token: "example-token", Ingress: []TunnelIngress{{Hostname: "panel.example.invalid", Service: "http://native:2053"}}}
	if err := (DockerTunnelProvisioner{StateDirectory: m.StateDirectory}).apply(context.Background(), state, e, defaultCloudflaredContainer); err != nil {
		t.Fatal(err)
	}
	if len(e.created) != 1 || e.created[0].HostConfig.NetworkMode != container.NetworkMode(dockerruntime.NetworkName) || len(e.created[0].NetworkingConfig.EndpointsConfig) != 1 || e.created[0].NetworkingConfig.EndpointsConfig[dockerruntime.NetworkName] == nil {
		t.Fatal("ordinary Tunnel topology changed")
	}
	if slices.Contains(e.calls, "stop") || slices.Contains(e.calls, "write-proxy-config") {
		t.Fatal("ordinary Tunnel update changed CPA")
	}
}

func TestCPAFirstPrivateAttachmentRetainsSharedEndpoint(t *testing.T) {
	e, m, _ := cpaFixture(t)
	c := e.containers[defaultCloudflaredContainer]
	shared := *c.Container.NetworkSettings.Networks[dockerruntime.NetworkName]
	delete(c.Container.NetworkSettings.Networks, tunnelOriginNetwork)
	e.containers[defaultCloudflaredContainer] = c
	e.net.Network.Containers = map[string]network.EndpointResource{}
	if err := m.prepareTunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := e.containers[defaultCloudflaredContainer].Container.NetworkSettings.Networks
	if len(after) != 2 || after[dockerruntime.NetworkName].EndpointID != shared.EndpointID || after[dockerruntime.NetworkName].NetworkID != shared.NetworkID || after[dockerruntime.NetworkName].IPAddress != shared.IPAddress || after[tunnelOriginNetwork].GwPriority != 1 {
		t.Fatal("private attachment replaced the shared endpoint")
	}
	if !slices.Equal(e.calls, []string{"connect-private"}) {
		t.Fatalf("attachment changed processes: %v", e.calls)
	}
	if _, err := m.tunnel(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCPAInterruptedTrustUpdateResumesExactOriginalInstance(t *testing.T) {
	e, m, r := cpaFixture(t)
	executor := PackageExecutor{StateDirectory: m.StateDirectory}
	e.failCopy = true
	if err := m.sync(context.Background(), r, true, func() error { return executor.save(r) }); err == nil {
		t.Fatal("configuration failure ignored")
	}
	saved, err := executor.ReadResources(r.ApplicationID)
	if err != nil || saved.CPAIngress.ResumeContainerID != "cpa-id" || e.containers["owned-cpa"].Container.State.Running {
		t.Fatal("interruption lost durable resume intent")
	}
	e.failCopy = false
	if err := m.all(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	saved, err = executor.ReadResources(r.ApplicationID)
	if err != nil || saved.CPAIngress.ResumeContainerID != "" || !e.containers["owned-cpa"].Container.State.Running || e.containers["owned-cpa"].Container.ID != "cpa-id" {
		t.Fatal("retry did not restore the exact original instance")
	}
}

func TestCPATrustUpdatePreservesStoppedStateAndRejectsForeignResume(t *testing.T) {
	e, m, r := cpaFixture(t)
	e.containers["owned-cpa"].Container.State.Running = false
	if err := m.sync(context.Background(), r, true, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(e.calls, "start") || e.containers["owned-cpa"].Container.State.Running {
		t.Fatal("originally stopped CPA was started")
	}
	r.CPAIngress.ResumeContainerID = "different-container"
	calls := len(e.calls)
	if err := m.sync(context.Background(), r, true, func() error { return nil }); err == nil || len(e.calls) != calls {
		t.Fatal("foreign resume intent reached a resource operation")
	}
}
