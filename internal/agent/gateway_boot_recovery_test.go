package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/gateway"
)

func failedBootContainer(code int) client.ContainerInspectResult {
	return client.ContainerInspectResult{Container: container.InspectResponse{
		ID: "retained-id", Name: "retained", State: &container.State{Status: "exited", ExitCode: code,
			Error: "failed to set up container networking: failed to bind host port 100.64.0.2:443/tcp: cannot assign requested address"},
		HostConfig: &container.HostConfig{NetworkMode: "private-test", RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
			PortBindings: network.PortMap{network.MustParsePort("443/tcp"): {{HostIP: netip.MustParseAddr("100.64.0.2"), HostPort: "443"}}}},
		NetworkSettings: &container.NetworkSettings{SandboxID: "working-sandbox", Networks: map[string]*network.EndpointSettings{}},
	}}
}

func TestGatewayBootBindFailureRetainsOldProcessExitCode(t *testing.T) {
	for _, code := range []int{2, 128, 137} {
		current := failedBootContainer(code)
		if !gatewayBindStartFailed(current) {
			t.Fatalf("Docker bind error with previous process exit %d was ignored", code)
		}
	}
	current := failedBootContainer(0)
	current.Container.State.Error = ""
	if gatewayBindStartFailed(current) {
		t.Fatal("normal stop authorized an automatic start")
	}
	current = failedBootContainer(137)
	current.Container.State.OOMKilled = true
	if gatewayBindStartFailed(current) {
		t.Fatal("unresolved OOM authorized a start")
	}
	current = failedBootContainer(128)
	current.Container.HostConfig.PortBindings = nil
	if gatewayBindStartFailed(current) {
		t.Fatal("unrelated bind error authorized a start")
	}
}

func TestCohostBootstrapUsesOnlyCommittedInstalledPrivateAddress(t *testing.T) {
	state := gatewayState(1, 3000)
	current := failedBootContainer(128)
	address, err := cohostBootstrapAddress(state, current)
	if err != nil || address != "100.64.0.2" {
		t.Fatal("original private address was not recognized", err)
	}
	state.Listeners[0].Address = "100.64.0.3"
	if _, err := cohostBootstrapAddress(state, current); err == nil {
		t.Fatal("address outside installed bindings was accepted")
	}
	state.Listeners = []gateway.Listener{{Kind: "headscale", Address: "1.1.1.1"}}
	if _, err := cohostBootstrapAddress(state, current); err == nil {
		t.Fatal("public address was accepted for private bootstrap")
	}
}

func TestGatewayStartupRestoresEmptyConfigurationWithoutAdvancingState(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	before, err := store.RecordGatewayState(ctx, gatewayState(7, 3000), nil)
	if err != nil {
		t.Fatal(err)
	}
	configuration := []byte(`{"admin":{"config":{"persist":false}}}`)
	loads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write(configuration)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/load" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(400)
			return
		}
		loads++
		configuration, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer server.Close()
	driver, err := NewCaddyGatewayDriver(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	store.sharedCaddy = driver
	if err := store.restoreGatewayConfiguration(ctx); err != nil {
		t.Fatal(err)
	}
	if err := driver.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.restoreGatewayConfiguration(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := store.GatewayState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loads != 1 || after.ConfigHash != before.ConfigHash || after.Desired.Revision != before.Desired.Revision || !after.AppliedAt.Equal(before.AppliedAt) {
		t.Fatal("startup replayed a configuration or changed committed state")
	}
	configuration = []byte(`{"admin":{}}`)
	if err := driver.Health(ctx); err == nil {
		t.Fatal("Admin-only bootstrap reported a healthy applied gateway")
	}
	configuration = []byte(`{"apps":{"http":{"servers":{"incomplete":{"listen":[":9999"]}}}}}`)
	if err := store.restoreGatewayConfiguration(ctx); err != nil {
		t.Fatal(err)
	}
	if loads != 2 {
		t.Fatal("a partial old configuration was accepted as complete")
	}
}

func bootstrapFixture(t *testing.T) (*privateBootstrap, *[]bootstrapInterface, *[]string) {
	t.Helper()
	interfaces := []bootstrapInterface{}
	mutations := []string{}
	b := &privateBootstrap{Address: "100.64.0.2", GatewayID: "gateway-id", ConfigHash: "committed-hash", BootID: "original-boot", path: filepath.Join(t.TempDir(), "bootstrap.json")}
	b.run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "-j" {
			return json.Marshal(interfaces)
		}
		mutations = append(mutations, args[1])
		if args[1] == "add" {
			var i bootstrapInterface
			_ = json.Unmarshal([]byte(`{"ifname":"lo","addr_info":[{"local":"100.64.0.2","label":"lo:vstboot"}]}`), &i)
			interfaces = append(interfaces, i)
		}
		if args[1] == "del" {
			interfaces = slices.DeleteFunc(interfaces, func(i bootstrapInterface) bool { return i.Name == "lo" })
		}
		return nil, nil
	}
	return b, &interfaces, &mutations
}

func TestPrivateBootstrapReleasesOnlyTemporaryAlias(t *testing.T) {
	b, interfaces, mutations := bootstrapFixture(t)
	ctx := context.Background()
	if err := b.acquire(ctx, *interfaces); err != nil {
		t.Fatal(err)
	}
	var real bootstrapInterface
	_ = json.Unmarshal([]byte(`{"ifname":"tailscale0","addr_info":[{"local":"100.64.0.2","label":"tailscale0"}]}`), &real)
	*interfaces = append(*interfaces, real)
	if err := b.close(ctx); err != nil {
		t.Fatal(err)
	}
	if !bootstrapAddressOn(*interfaces, "tailscale0", b.Address, "") || bootstrapAddressOn(*interfaces, "lo", b.Address, privateBootstrapLabel) || !reflect.DeepEqual(*mutations, []string{"add", "del"}) {
		t.Fatal("bootstrap altered the real interface or left its alias")
	}
	if _, err := os.Lstat(b.path); !os.IsNotExist(err) {
		t.Fatal("bootstrap receipt was not removed")
	}
	if err := b.close(ctx); err != nil || len(*mutations) != 2 {
		t.Fatal("cleanup was not idempotent")
	}
}

func TestPrivateBootstrapPreservesForeignAddressAndReceipt(t *testing.T) {
	b, interfaces, mutations := bootstrapFixture(t)
	ctx := context.Background()
	raw := []byte(`{"address":"100.64.0.2","gatewayId":"another-gateway","configHash":"committed-hash","bootId":"original-boot"}`)
	if err := os.WriteFile(b.path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.acquire(ctx, *interfaces); err == nil {
		t.Fatal("foreign bootstrap receipt was adopted")
	}
	if err := b.close(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(b.path)
	if !reflect.DeepEqual(after, raw) || len(*mutations) != 0 {
		t.Fatal("foreign receipt or interface was modified")
	}
	if err := os.Remove(b.path); err != nil {
		t.Fatal(err)
	}
	var foreign bootstrapInterface
	_ = json.Unmarshal([]byte(`{"ifname":"lo","addr_info":[{"local":"100.64.0.2","label":"lo:other"}]}`), &foreign)
	*interfaces = []bootstrapInterface{foreign}
	if err := b.acquire(ctx, *interfaces); err == nil {
		t.Fatal("foreign address was adopted")
	}
	if len(*mutations) != 0 {
		t.Fatal("foreign address was modified")
	}
}

type bootNetworkDocker struct {
	*fakePackageDocker
	network  client.NetworkInspectResult
	connects int
	networks map[string]client.NetworkInspectResult
}

func (d *bootNetworkDocker) ContainerRestart(ctx context.Context, id string, _ client.ContainerRestartOptions) (client.ContainerRestartResult, error) {
	v, err := d.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return client.ContainerRestartResult{}, err
	}
	d.calls = append(d.calls, "restart")
	v.Container.NetworkSettings.SandboxID = "restored-sandbox"
	d.containers[v.Container.Name] = v
	return client.ContainerRestartResult{}, errors.New("restart response was lost")
}

func (d *bootNetworkDocker) NetworkInspect(_ context.Context, id string, _ client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	if d.networks != nil {
		n, ok := d.networks[id]
		if !ok {
			return n, errors.New("network not found")
		}
		return n, nil
	}
	return d.network, nil
}
func (d *bootNetworkDocker) NetworkCreate(context.Context, string, client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	panic("bootstrap must not create networks")
}
func (d *bootNetworkDocker) NetworkDisconnect(context.Context, string, client.NetworkDisconnectOptions) (client.NetworkDisconnectResult, error) {
	panic("bootstrap must not detach applications")
}
func (d *bootNetworkDocker) NetworkConnect(ctx context.Context, networkID string, options client.NetworkConnectOptions) (client.NetworkConnectResult, error) {
	d.connects++
	v, err := d.ContainerInspect(ctx, options.Container, client.ContainerInspectOptions{})
	if err != nil {
		return client.NetworkConnectResult{}, err
	}
	name, id := "private-test", "network-id"
	if d.networks != nil {
		n, err := d.NetworkInspect(ctx, networkID, client.NetworkInspectOptions{})
		if err != nil {
			return client.NetworkConnectResult{}, err
		}
		name, id = n.Network.Name, n.Network.ID
	}
	v.Container.NetworkSettings.Networks[name] = &network.EndpointSettings{NetworkID: id, EndpointID: "restored-endpoint", Aliases: options.EndpointConfig.Aliases, IPAMConfig: options.EndpointConfig.IPAMConfig}
	v.Container.NetworkSettings.Ports = v.Container.HostConfig.PortBindings
	d.containers[v.Container.Name] = v
	return client.NetworkConnectResult{}, errors.New("response was lost")
}

func TestRetainedNetworkRecoversLostResponseWithoutReplacingContainer(t *testing.T) {
	d := &bootNetworkDocker{fakePackageDocker: newPackageDocker(), network: client.NetworkInspectResult{Network: network.Inspect{Network: network.Network{ID: "network-id", Name: "private-test", Driver: "bridge"}}}}
	current := failedBootContainer(137)
	current.Container.HostConfig.PortBindings[network.MustParsePort("443/tcp")][0].HostIP = netip.MustParseAddr("127.0.0.1")
	current.Container.State.Error = "failed to bind host port 127.0.0.1:443/tcp: cannot assign requested address"
	d.containers[current.Container.Name] = current
	expected := []RuntimeNetworkAttachment{{Name: "private-test", ID: "network-id", Aliases: []string{"controller"}}}
	if err := recoverRetainedContainerNetworks(context.Background(), d, current, expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.calls, []string{"start"}) || d.connects != 1 || len(d.created) != 0 {
		t.Fatal("recovery repeated, recreated or pulled a workload")
	}
	current, _ = d.ContainerInspect(context.Background(), current.Container.ID, client.ContainerInspectOptions{})
	if !retainedEndpointPresent(current, expected[0]) {
		t.Fatal("endpoint or original published ports did not recover")
	}
	d.network.Network.ID = "replacement-network"
	d.calls = nil
	d.connects = 0
	if err := recoverRetainedContainerNetworks(context.Background(), d, current, expected); err == nil || len(d.calls) != 0 || d.connects != 0 {
		t.Fatal("replaced network was accepted or mutated")
	}
}

func TestPackageBootNetworkRecoveryPreservesAuthorizedReceipt(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	const appID = "retained-app"
	const key = "source/retained"
	if _, err := store.RecordApplied(ctx, AppliedInstallation{ApplicationID: appID, InstanceID: "original-task", AppKey: key, Version: "1.0.0", Config: json.RawMessage(`{}`), Secrets: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	executor := PackageExecutor{StateDirectory: store.dataDir}
	receipt := &InstanceResources{Version: 1, ApplicationID: appID, AppKey: key, Runtime: "docker", State: "ready", TaskID: "original-task", Resources: []RuntimeResource{{Kind: "container", Name: "retained", ID: "retained-id", Component: "web"}}}
	if err := executor.save(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(executor.receiptPath(appID))
	if err != nil {
		t.Fatal(err)
	}
	current := failedBootContainer(137)
	current.Container.Config = &container.Config{Labels: map[string]string{"io.vastora.managed": "true", applicationIdentityLabel: key, applicationInstallationLabel: appID, "io.vastora.component": "web"}}
	current.Container.State.Running = true
	current.Container.State.Error = ""
	current.Container.HostConfig.PortBindings = nil
	current.Container.NetworkSettings.Networks["private-test"] = &network.EndpointSettings{NetworkID: "network-id", EndpointID: "working-endpoint", Aliases: []string{"web"}}
	docker := &bootNetworkDocker{fakePackageDocker: newPackageDocker(), network: client.NetworkInspectResult{Network: network.Inspect{Network: network.Network{ID: "network-id", Name: "private-test", Driver: "bridge"}}}}
	docker.containers[current.Container.Name] = current
	for attempt := 0; attempt < 2; attempt++ {
		if err := store.recoverPackageNetworks(ctx, docker); err != nil {
			t.Fatal(err)
		}
	}
	saved, err := executor.readBootNetworks(receipt)
	if err != nil || len(saved.Containers["retained-id"]) != 1 {
		t.Fatal("working attachment was not retained", err)
	}
	info, err := os.Stat(executor.bootNetworkPath(appID))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("runtime observations are not private", err)
	}
	current.Container.State.Running = false
	current.Container.State.Error = "failed to bind host port 127.0.0.1:443/tcp: cannot assign requested address"
	current.Container.HostConfig.PortBindings = network.PortMap{network.MustParsePort("443/tcp"): {{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: "443"}}}
	current.Container.NetworkSettings.Networks = map[string]*network.EndpointSettings{}
	docker.containers[current.Container.Name] = current
	if err := store.recoverPackageNetworks(ctx, docker); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(executor.receiptPath(appID))
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("startup changed the receipt used by Center handover authorization", err)
	}
	if !reflect.DeepEqual(docker.calls, []string{"start"}) || docker.connects != 1 || len(docker.created) != 0 {
		t.Fatal("same-container recovery did not run exactly once")
	}
}

func TestRetainedDefaultBridgeUsesDockerIdentityAcrossBoot(t *testing.T) {
	for _, shared := range []bool{false, true} {
		d := &bootNetworkDocker{fakePackageDocker: newPackageDocker(), networks: map[string]client.NetworkInspectResult{}}
		bridge := client.NetworkInspectResult{Network: network.Inspect{Network: network.Network{ID: "new-built-in-id", Name: "bridge", Driver: "bridge", Scope: "local", Options: map[string]string{"com.docker.network.bridge.default_bridge": "true"}}}}
		custom := client.NetworkInspectResult{Network: network.Inspect{Network: network.Network{ID: "shared-id", Name: "original-shared", Driver: "bridge", Scope: "local"}}}
		d.networks["bridge"], d.networks["new-built-in-id"] = bridge, bridge
		d.networks["shared-id"] = custom
		current := failedBootContainer(128)
		current.Container.HostConfig.NetworkMode = "bridge"
		current.Container.HostConfig.PortBindings = nil
		current.Container.State.Running = true
		d.containers[current.Container.Name] = current
		expected := []RuntimeNetworkAttachment{{Name: "bridge", ID: "old-built-in-id"}}
		if shared {
			expected = []RuntimeNetworkAttachment{{Name: "original-shared", ID: "shared-id", Aliases: []string{"service"}}}
		}
		if err := recoverRetainedContainerNetworks(context.Background(), d, current, expected); err != nil {
			t.Fatal(err)
		}
		if len(d.created) != 0 || len(d.calls) != 0 || d.connects != map[bool]int{false: 1, true: 2}[shared] {
			t.Fatal("recovery replaced or restarted the working original container")
		}
		bridge.Network.Options = nil
		d.networks["bridge"] = bridge
		before := d.connects
		if err := recoverRetainedContainerNetworks(context.Background(), d, current, expected); err == nil || d.connects != before {
			t.Fatal("a non-default bridge was accepted or mutated")
		}
	}
}

func TestRetainedOrphanSandboxRestartsOnlyTheOriginalContainer(t *testing.T) {
	d := &bootNetworkDocker{fakePackageDocker: newPackageDocker(), network: client.NetworkInspectResult{Network: network.Inspect{Network: network.Network{ID: "network-id", Name: "private-test", Driver: "bridge"}}}}
	current := failedBootContainer(128)
	current.Container.State.Running = true
	current.Container.HostConfig.PortBindings = nil
	current.Container.NetworkSettings.SandboxID = ""
	current.Container.NetworkSettings.Networks["private-test"] = &network.EndpointSettings{NetworkID: "network-id"}
	d.containers[current.Container.Name] = current
	expected := []RuntimeNetworkAttachment{{Name: "private-test", ID: "network-id"}}
	// This stub is absent from Docker's live network; the original sandbox must
	// be recreated before the existing network can be connected.
	if err := recoverRetainedContainerNetworks(context.Background(), d, current, expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d.calls, []string{"restart"}) || d.connects != 1 || len(d.created) != 0 {
		t.Fatal("orphan recovery replaced or repeatedly restarted the application")
	}
	current.Container.NetworkSettings.SandboxID = "healthy-sandbox"
	d.containers[current.Container.Name] = current
	d.calls = nil
	if err := recoverRetainedContainerNetworks(context.Background(), d, current, expected); err != nil {
		t.Fatal(err)
	}
	if len(d.calls) != 0 {
		t.Fatal("healthy sandbox was restarted")
	}
}
