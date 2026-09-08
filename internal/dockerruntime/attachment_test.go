package dockerruntime

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

type fakeAttachmentEngine struct {
	fakeNetworkEngine
	connects, disconnects int
	connectErr            error
	recover               bool
}

func (e *fakeAttachmentEngine) NetworkConnect(_ context.Context, name string, input client.NetworkConnectOptions) (client.NetworkConnectResult, error) {
	e.connects++
	if e.recover {
		value := e.containers[input.Container]
		endpoint := *input.EndpointConfig
		endpoint.EndpointID = "endpoint"
		value.Container.NetworkSettings = &container.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{name: &endpoint},
			Ports:    value.Container.HostConfig.PortBindings,
		}
		e.containers[input.Container] = value
	}
	return client.NetworkConnectResult{}, e.connectErr
}

func (e *fakeAttachmentEngine) NetworkDisconnect(context.Context, string, client.NetworkDisconnectOptions) (client.NetworkDisconnectResult, error) {
	e.disconnects++
	return client.NetworkDisconnectResult{}, nil
}

func TestManagedAttachmentRecoveryRequiresLiveReadBack(t *testing.T) {
	for _, recovered := range []bool{false, true} {
		value := managedContainer("3x-ui")
		value.Container.HostConfig = &container.HostConfig{
			NetworkMode:  "test-network",
			PortBindings: network.PortMap{network.MustParsePort("2053/tcp"): {{HostIP: netip.MustParseAddr("100.64.0.4"), HostPort: "2053"}}},
		}
		engine := &fakeAttachmentEngine{
			fakeNetworkEngine: fakeNetworkEngine{
				inspectResults: []client.NetworkInspectResult{ownedNetwork("component")},
				containers:     map[string]client.ContainerInspectResult{"immutable-id": value},
			},
			connectErr: errors.New("lost connect response"), recover: recovered,
		}
		err := RecoverAttachment(context.Background(), engine, "immutable-id", "test-network", "component", "vastora-3x-ui")
		if (err == nil) != recovered || engine.connects != 1 || engine.disconnects != 0 {
			t.Fatalf("recovered=%t err=%v connects=%d disconnects=%d", recovered, err, engine.connects, engine.disconnects)
		}
		if recovered && !AttachmentHealthy(engine.containers["immutable-id"], "test-network", "vastora-3x-ui") {
			t.Fatal("live private bind and alias were not restored")
		}
	}
}
