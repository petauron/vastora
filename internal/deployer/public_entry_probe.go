package deployer

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
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
	"github.com/moby/moby/api/types/container"
	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/deployapi"
	"github.com/petauron/vastora/internal/dockerruntime"
)

const (
	publicEntryProbeLifetime  = 30 * time.Second
	publicEntryProbeContainer = "vastora-public-entry-probe"
)

var publicEntryProbePorts = []int{80, 443}

type activePublicEntryProbe struct {
	id          string
	containerID string
	expiresAt   time.Time
}

// PublicEntryProbeService launches the short-lived listener as an isolated
// bridged container. Only the requested host sockets are published; the
// long-running Deployer never joins the host network namespace.
type PublicEntryProbeService struct {
	mu           sync.Mutex
	active       *activePublicEntryProbe
	ports        []int
	lifetime     time.Duration
	now          func() time.Time
	random       io.Reader
	dockerSocket string
	runtimeImage string
}

func NewPublicEntryProbeService(dockerSocket, runtimeImage string) *PublicEntryProbeService {
	if dockerSocket == "" {
		dockerSocket = "unix:///var/run/docker.sock"
	}
	return &PublicEntryProbeService{
		ports: append([]int(nil), publicEntryProbePorts...), lifetime: publicEntryProbeLifetime,
		now: time.Now, random: rand.Reader, dockerSocket: dockerSocket, runtimeImage: strings.TrimSpace(runtimeImage),
	}
}

func (service *PublicEntryProbeService) StartPublicEntryProbe(ctx context.Context, input deployapi.PublicEntryProbeRequest) (deployapi.PublicEntryProbe, error) {
	bindAddress, err := netip.ParseAddr(strings.TrimSpace(input.BindAddress))
	if err != nil || !bindAddress.Unmap().Is4() || bindAddress.IsUnspecified() || bindAddress.IsMulticast() {
		return deployapi.PublicEntryProbe{}, errors.New("deployer: a valid local probe address is required")
	}
	if service.runtimeImage == "" {
		return deployapi.PublicEntryProbe{}, errors.New("deployer: public entry probe runtime image is not configured")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	docker, err := client.New(client.WithHost(service.dockerSocket))
	if err != nil {
		return deployapi.PublicEntryProbe{}, fmt.Errorf("deployer: connect Docker for public entry probe: %w", err)
	}
	defer docker.Close()
	if err := dockerruntime.EnsureNetwork(ctx, docker); err != nil {
		return deployapi.PublicEntryProbe{}, err
	}
	if service.active != nil {
		if service.now().Before(service.active.expiresAt) {
			return deployapi.PublicEntryProbe{}, errors.New("deployer: another public entry probe is already running")
		}
		if err := service.closeActiveLocked(ctx, docker); err != nil {
			return deployapi.PublicEntryProbe{}, err
		}
	}
	if _, err := docker.ContainerRemove(ctx, publicEntryProbeContainer, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return deployapi.PublicEntryProbe{}, fmt.Errorf("deployer: remove stale public entry probe: %w", err)
	}
	id, err := randomToken(service.random, 16)
	if err != nil {
		return deployapi.PublicEntryProbe{}, fmt.Errorf("deployer: create public entry probe ID: %w", err)
	}
	challenge, err := randomToken(service.random, 32)
	if err != nil {
		return deployapi.PublicEntryProbe{}, fmt.Errorf("deployer: create public entry challenge: %w", err)
	}
	expiresAt := service.now().UTC().Add(service.lifetime)
	created, err := docker.ContainerCreate(ctx, publicEntryProbeCreateOptions(service.runtimeImage, bindAddress, service.ports, challenge, expiresAt))
	if err != nil {
		return deployapi.PublicEntryProbe{}, fmt.Errorf("deployer: publish public entry probe ports: %w", err)
	}
	if _, err := docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		_, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true})
		return deployapi.PublicEntryProbe{}, fmt.Errorf("deployer: start public entry probe: %w", err)
	}
	service.active = &activePublicEntryProbe{id: id, containerID: created.ID, expiresAt: expiresAt}
	go service.expire(id, service.lifetime)
	return deployapi.PublicEntryProbe{ID: id, Challenge: challenge, Ports: append([]int(nil), service.ports...), ExpiresAt: expiresAt.Format(time.RFC3339Nano)}, nil
}

func publicEntryProbeCreateOptions(image string, bindAddress netip.Addr, ports []int, challenge string, expiresAt time.Time) client.ContainerCreateOptions {
	exposed := dockernetwork.PortSet{}
	bindings := dockernetwork.PortMap{}
	for _, portNumber := range ports {
		port := dockernetwork.MustParsePort(strconv.Itoa(portNumber) + "/tcp")
		exposed[port] = struct{}{}
		bindings[port] = []dockernetwork.PortBinding{{HostIP: bindAddress.Unmap(), HostPort: strconv.Itoa(portNumber)}}
	}
	return client.ContainerCreateOptions{
		Config: &container.Config{
			Image: image, User: "0:0", Cmd: []string{"deployer", "public-entry-probe", "--challenge", challenge, "--expires-at", expiresAt.Format(time.RFC3339Nano)},
			ExposedPorts: exposed,
			Labels:       map[string]string{dockerruntime.ManagedLabel: "true", dockerruntime.ComponentLabel: "public-entry-probe"},
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(dockerruntime.NetworkName), PortBindings: bindings,
			ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"NET_BIND_SERVICE"}, SecurityOpt: []string{"no-new-privileges:true"},
		},
		NetworkingConfig: dockerruntime.NetworkingConfig(), Name: publicEntryProbeContainer,
	}
}

func (service *PublicEntryProbeService) StopPublicEntryProbe(ctx context.Context, id string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active == nil {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(service.active.id), []byte(id)) != 1 {
		return errors.New("deployer: public entry probe was not found")
	}
	docker, err := client.New(client.WithHost(service.dockerSocket))
	if err != nil {
		return fmt.Errorf("deployer: connect Docker to stop public entry probe: %w", err)
	}
	defer docker.Close()
	return service.closeActiveLocked(ctx, docker)
}

func (service *PublicEntryProbeService) expire(id string, after time.Duration) {
	timer := time.NewTimer(after)
	defer timer.Stop()
	<-timer.C
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active == nil || subtle.ConstantTimeCompare([]byte(service.active.id), []byte(id)) != 1 {
		return
	}
	docker, err := client.New(client.WithHost(service.dockerSocket))
	if err == nil {
		defer docker.Close()
		_ = service.closeActiveLocked(context.Background(), docker)
	}
}

func (service *PublicEntryProbeService) closeActiveLocked(ctx context.Context, docker *client.Client) error {
	if service.active == nil {
		return nil
	}
	if _, err := docker.ContainerRemove(ctx, service.active.containerID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("deployer: stop public entry probe: %w", err)
	}
	service.active = nil
	return nil
}

// RunPublicEntryProbe runs inside the short-lived bridged container.
func RunPublicEntryProbe(ctx context.Context, challenge string, expiresAt time.Time) error {
	if len(strings.TrimSpace(challenge)) < 32 || !expiresAt.After(time.Now()) {
		return errors.New("deployer: invalid public entry probe arguments")
	}
	listeners := make([]net.Listener, 0, len(publicEntryProbePorts))
	for _, port := range publicEntryProbePorts {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		if err != nil {
			closeListeners(listeners)
			return fmt.Errorf("deployer: listen for public entry probe on TCP %d: %w", port, err)
		}
		listeners = append(listeners, listener)
		go servePublicEntryChallenge(listener, challenge, expiresAt)
	}
	timer := time.NewTimer(time.Until(expiresAt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	closeListeners(listeners)
	return nil
}

func closeListeners(listeners []net.Listener) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}

func servePublicEntryChallenge(listener net.Listener, challenge string, expiresAt time.Time) {
	for {
		if tcp, ok := listener.(*net.TCPListener); ok {
			_ = tcp.SetDeadline(expiresAt)
		}
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		line, readErr := bufio.NewReader(io.LimitReader(connection, 256)).ReadString('\n')
		expected := "VASTORA-PROBE/1 " + challenge + "\n"
		if readErr == nil && subtle.ConstantTimeCompare([]byte(line), []byte(expected)) == 1 {
			_, _ = io.WriteString(connection, "VASTORA-OK/1 "+challenge+"\n")
		}
		_ = connection.Close()
	}
}

func randomToken(source io.Reader, size int) (string, error) {
	raw := make([]byte, size)
	if _, err := io.ReadFull(source, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
