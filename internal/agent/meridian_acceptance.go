package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

const acceptanceOwnerLabel = "io.vastora.recovery-client"

func (e ApplicationExecutor) VerifyMeridianAcceptance(ctx context.Context, task meridianruntime.AcceptanceTask) (meridianruntime.AcceptanceResult, error) {
	digest, err := task.Digest()
	if err != nil {
		return meridianruntime.AcceptanceResult{}, err
	}
	if err := e.VerifyMeridianClient(ctx, task.Client, task.ExpectedExit); err != nil {
		return meridianruntime.AcceptanceResult{}, err
	}
	return meridianruntime.AcceptanceResult{TaskDigest: digest}, nil
}

// One isolated namespace and a Docker-assigned host loopback port prevent a
// host socket race from substituting a different SOCKS service. The container
// receives its secret configuration on stdin, never in labels/env/arguments.
func meridianAcceptanceOptions(name, networkID string) client.ContainerCreateOptions {
	port := network.MustParsePort("1080/tcp")
	pids := int64(32)
	return client.ContainerCreateOptions{Name: name,
		Config: &container.Config{Image: xrayWorkerImageReference, User: "1000:1000", Cmd: []string{"run", "-c", "stdin:"},
			OpenStdin: true, StdinOnce: true, AttachStdin: true,
			ExposedPorts: network.PortSet{port: {}}, Labels: map[string]string{acceptanceOwnerLabel: name}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(networkID), ReadonlyRootfs: true,
			CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, LogConfig: container.LogConfig{Type: "none"},
			PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
			Resources:    container.Resources{Memory: 128 << 20, MemorySwap: 128 << 20, NanoCPUs: 1000000000, PidsLimit: &pids}},
	}
}

// VerifyMeridianClient performs one authenticated business request using the
// saved native/fixed-route identity. It reports success only after cleanup.
// Invocation and durable recovery authorization belong to the task channel.
func (e ApplicationExecutor) VerifyMeridianClient(parent context.Context, spec meridianruntime.AcceptanceClient, expectedExit string) (err error) {
	address, parseErr := netip.ParseAddr(expectedExit)
	if parseErr != nil || !landing.PublicIP(address) {
		return errors.New("agent: invalid recovery exit expectation")
	}
	encoded, err := spec.Config(1080)
	if err != nil {
		return err
	}
	// Only this private namespace listens on all interfaces. Docker publishes
	// it exclusively on host loopback and no other container joins the network.
	var config map[string]any
	if json.Unmarshal(encoded, &config) != nil {
		return errors.New("agent: invalid recovery client")
	}
	config["inbounds"].([]any)[0].(map[string]any)["listen"] = "0.0.0.0"
	encoded, err = json.Marshal(config)
	if err != nil {
		return errors.New("agent: invalid recovery client")
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return errors.New("agent: recovery Docker connection failed")
	}
	defer docker.Close()
	if _, err = docker.ImageInspect(ctx, xrayWorkerImageReference); err != nil {
		if !errdefs.IsNotFound(err) {
			return errors.New("agent: recovery client image inspection failed")
		}
		pull, pullErr := docker.ImagePull(ctx, xrayWorkerImageReference, client.ImagePullOptions{})
		if pullErr != nil {
			return errors.New("agent: recovery client image unavailable")
		}
		pullErr = pull.Wait(ctx)
		pull.Close()
		if pullErr != nil {
			return errors.New("agent: recovery client image unavailable")
		}
	}
	name := "vastora-recovery-client-" + rand.Text()
	// Record cleanup before each possibly-ambiguous Docker mutation. Labels
	// protect the cleanup-by-name path when a create response is lost.
	defer func() {
		cleanup, done := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer done()
		if cleanupErr := cleanupMeridianAcceptance(cleanup, docker, name); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()
	createdNetwork, err := docker.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Labels: map[string]string{acceptanceOwnerLabel: name}})
	if err != nil {
		return errors.New("agent: recovery client network creation failed")
	}
	created, err := docker.ContainerCreate(ctx, meridianAcceptanceOptions(name, createdNetwork.ID))
	if err != nil {
		return errors.New("agent: recovery client creation failed")
	}
	attached, err := docker.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{Stream: true, Stdin: true})
	if err != nil {
		return errors.New("agent: recovery client input unavailable")
	}
	defer attached.Close()
	stop := context.AfterFunc(ctx, attached.Close)
	defer stop()
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return errors.New("agent: recovery client startup failed")
	}
	if _, err = io.Copy(attached.Conn, bytes.NewReader(encoded)); err != nil {
		return errors.New("agent: recovery client input failed")
	}
	if err = attached.CloseWrite(); err != nil {
		return errors.New("agent: recovery client input failed")
	}
	// Only local listener readiness may be retried, never the business probe.
	ready, stopReady := context.WithTimeout(ctx, 5*time.Second)
	defer stopReady()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		inspected, inspectErr := docker.ContainerInspect(ready, created.ID, client.ContainerInspectOptions{})
		if inspectErr != nil || inspected.Container.State == nil || !inspected.Container.State.Running || inspected.Container.NetworkSettings == nil {
			return errors.New("agent: recovery client did not remain running")
		}
		bindings := inspected.Container.NetworkSettings.Ports[network.MustParsePort("1080/tcp")]
		if len(bindings) != 1 || bindings[0].HostIP != netip.MustParseAddr("127.0.0.1") || bindings[0].HostPort == "" {
			return errors.New("agent: recovery client listener is not isolated")
		}
		endpoint := net.JoinHostPort("127.0.0.1", bindings[0].HostPort)
		connection, dialErr := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ready, "tcp", endpoint)
		if dialErr == nil {
			_ = connection.Close()
			return meridianruntime.ProbeAcceptance(ctx, endpoint, expectedExit)
		}
		select {
		case <-ready.Done():
			return errors.New("agent: recovery client readiness timed out")
		case <-ticker.C:
		}
	}
}

func cleanupMeridianAcceptance(ctx context.Context, docker *client.Client, name string) error {
	inspected, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		if inspected.Container.Config == nil || inspected.Container.Config.Labels[acceptanceOwnerLabel] != name {
			return errors.New("agent: recovery client cleanup ownership changed")
		}
		if err = cleanupIPQualityContainer(ctx, docker, inspected.Container.ID); err != nil {
			return errors.New("agent: recovery client cleanup unconfirmed")
		}
	} else if !errdefs.IsNotFound(err) {
		return errors.New("agent: recovery client cleanup unconfirmed")
	}
	owned, err := docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil || owned.Network.Labels[acceptanceOwnerLabel] != name {
		return errors.New("agent: recovery network cleanup unconfirmed")
	}
	if _, err = docker.NetworkRemove(ctx, owned.Network.ID, client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
		return errors.New("agent: recovery network cleanup unconfirmed")
	}
	return nil
}
