package dockerruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type fakeNetworkEngine struct {
	inspectResults []client.NetworkInspectResult
	inspectErrors  []error
	inspectCalls   int
	createOptions  client.NetworkCreateOptions
	createErr      error
	containers     map[string]client.ContainerInspectResult
}

func (engine *fakeNetworkEngine) NetworkInspect(context.Context, string, client.NetworkInspectOptions) (client.NetworkInspectResult, error) {
	index := engine.inspectCalls
	engine.inspectCalls++
	if index < len(engine.inspectErrors) && engine.inspectErrors[index] != nil {
		return client.NetworkInspectResult{}, engine.inspectErrors[index]
	}
	if index < len(engine.inspectResults) {
		return engine.inspectResults[index], nil
	}
	return client.NetworkInspectResult{}, errdefs.ErrNotFound.WithMessage("network not found")
}

func (engine *fakeNetworkEngine) NetworkCreate(_ context.Context, _ string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error) {
	engine.createOptions = options
	return client.NetworkCreateResult{ID: "network-id"}, engine.createErr
}

func (engine *fakeNetworkEngine) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	inspected, ok := engine.containers[id]
	if !ok {
		return client.ContainerInspectResult{}, errdefs.ErrNotFound.WithMessage("container not found")
	}
	return inspected, nil
}

func ownedNetwork(component string, containerIDs ...string) client.NetworkInspectResult {
	attached := make(map[string]network.EndpointResource, len(containerIDs))
	for _, id := range containerIDs {
		attached[id] = network.EndpointResource{Name: id}
	}
	return client.NetworkInspectResult{Network: network.Inspect{
		Network:    network.Network{Driver: "bridge", Labels: map[string]string{ManagedLabel: "true", ComponentLabel: component, NetworkLabel: "test-network"}},
		Containers: attached,
	}}
}

func managedContainer(component string) client.ContainerInspectResult {
	return client.ContainerInspectResult{Container: container.InspectResponse{Config: &container.Config{Labels: map[string]string{
		ManagedLabel: "true", ComponentLabel: component,
	}}}}
}

func TestEnsureBridgeNetworkCreatesOwnedBridge(t *testing.T) {
	engine := &fakeNetworkEngine{inspectErrors: []error{errdefs.ErrNotFound.WithMessage("network not found")}}
	if err := EnsureBridgeNetwork(context.Background(), engine, "test-network", "test-component"); err != nil {
		t.Fatal(err)
	}
	if engine.createOptions.Driver != "bridge" || engine.createOptions.Labels[ManagedLabel] != "true" || engine.createOptions.Labels[ComponentLabel] != "test-component" || engine.createOptions.Labels[NetworkLabel] != "test-network" {
		t.Fatalf("create options = %#v", engine.createOptions)
	}
}

func TestEnsureBridgeNetworkRejectsUnownedOrWrongDriver(t *testing.T) {
	for name, inspected := range map[string]client.NetworkInspectResult{
		"unowned": {Network: network.Inspect{Network: network.Network{Driver: "bridge"}}},
		"driver":  {Network: network.Inspect{Network: network.Network{Driver: "host", Labels: map[string]string{ManagedLabel: "true", ComponentLabel: "component", NetworkLabel: "test-network"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			engine := &fakeNetworkEngine{inspectResults: []client.NetworkInspectResult{inspected}}
			if err := EnsureBridgeNetwork(context.Background(), engine, "test-network", "component"); err == nil {
				t.Fatal("unsafe network was accepted")
			}
		})
	}
}

func TestEnsureBridgeNetworkRejectsUnownedAttachedContainer(t *testing.T) {
	engine := &fakeNetworkEngine{
		inspectResults: []client.NetworkInspectResult{ownedNetwork("component", "foreign")},
		containers: map[string]client.ContainerInspectResult{
			"foreign": {Container: container.InspectResponse{Config: &container.Config{}}},
		},
	}
	if err := EnsureBridgeNetwork(context.Background(), engine, "test-network", "component"); err == nil {
		t.Fatal("network with an unowned endpoint was accepted")
	}
	if engine.createOptions.Driver != "" {
		t.Fatal("unsafe existing network was replaced")
	}
}

func TestEnsureBridgeNetworkAcceptsManagedAttachedContainers(t *testing.T) {
	engine := &fakeNetworkEngine{
		inspectResults: []client.NetworkInspectResult{ownedNetwork("component", "managed")},
		containers:     map[string]client.ContainerInspectResult{"managed": managedContainer("application")},
	}
	if err := EnsureBridgeNetwork(context.Background(), engine, "test-network", "component"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureBridgeNetworkValidatesConcurrentCreate(t *testing.T) {
	engine := &fakeNetworkEngine{
		inspectErrors:  []error{errdefs.ErrNotFound.WithMessage("network not found"), nil},
		inspectResults: []client.NetworkInspectResult{{}, {Network: network.Inspect{Network: network.Network{Driver: "bridge"}}}},
		createErr:      errdefs.ErrAlreadyExists.WithMessage("already exists"),
	}
	if err := EnsureBridgeNetwork(context.Background(), engine, "test-network", "component"); err == nil {
		t.Fatal("concurrently created unowned network was accepted")
	}
	if engine.inspectCalls != 2 {
		t.Fatalf("inspect calls = %d, want 2", engine.inspectCalls)
	}
}

func TestEnsureBridgeNetworkReconcilesLostCreateResponse(t *testing.T) {
	engine := &fakeNetworkEngine{
		inspectErrors:  []error{errdefs.ErrNotFound.WithMessage("network not found"), nil},
		inspectResults: []client.NetworkInspectResult{{}, ownedNetwork("component")},
		createErr:      errors.New("create response lost"),
	}
	if err := EnsureBridgeNetwork(context.Background(), engine, "test-network", "component"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureBridgeNetworkReturnsAttachedContainerInspectError(t *testing.T) {
	engine := &fakeNetworkEngine{inspectResults: []client.NetworkInspectResult{ownedNetwork("component", "missing")}}
	err := EnsureBridgeNetwork(context.Background(), engine, "test-network", "component")
	if err == nil || !errors.Is(err, errdefs.ErrNotFound) {
		t.Fatalf("inspect error = %v", err)
	}
}
