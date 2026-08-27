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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/deployapi"
)

const publicEntryProbeLifetime = 30 * time.Second

var publicEntryProbePorts = []int{80, 443}

type activePublicEntryProbe struct {
	id        string
	challenge string
	expiresAt time.Time
	listeners []net.Listener
}

type PublicEntryProbeService struct {
	mu       sync.Mutex
	active   *activePublicEntryProbe
	ports    []int
	lifetime time.Duration
	now      func() time.Time
	random   io.Reader
}

func NewPublicEntryProbeService() *PublicEntryProbeService {
	return &PublicEntryProbeService{
		ports:    append([]int(nil), publicEntryProbePorts...),
		lifetime: publicEntryProbeLifetime,
		now:      time.Now,
		random:   rand.Reader,
	}
}

func (service *PublicEntryProbeService) StartPublicEntryProbe(_ context.Context, input deployapi.PublicEntryProbeRequest) (deployapi.PublicEntryProbe, error) {
	bindIP := net.ParseIP(strings.TrimSpace(input.BindAddress))
	if bindIP == nil || bindIP.To4() == nil || bindIP.IsUnspecified() || bindIP.IsMulticast() {
		return deployapi.PublicEntryProbe{}, errors.New("deployer: a valid local probe address is required")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active != nil {
		if service.now().Before(service.active.expiresAt) {
			return deployapi.PublicEntryProbe{}, errors.New("deployer: another public entry probe is already running")
		}
		service.closeActiveLocked()
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
	probe := &activePublicEntryProbe{id: id, challenge: challenge, expiresAt: expiresAt}
	actualPorts := make([]int, 0, len(service.ports))
	for _, port := range service.ports {
		listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", net.JoinHostPort(bindIP.String(), strconv.Itoa(port)))
		if err != nil {
			closeListeners(probe.listeners)
			return deployapi.PublicEntryProbe{}, fmt.Errorf("deployer: public TCP port %d is unavailable: %w", port, err)
		}
		probe.listeners = append(probe.listeners, listener)
		actualPorts = append(actualPorts, listener.Addr().(*net.TCPAddr).Port)
	}
	service.active = probe
	for _, listener := range probe.listeners {
		go servePublicEntryChallenge(listener, challenge, expiresAt)
	}
	go service.expire(id, service.lifetime)
	return deployapi.PublicEntryProbe{ID: id, Challenge: challenge, Ports: actualPorts, ExpiresAt: expiresAt.Format(time.RFC3339Nano)}, nil
}

func (service *PublicEntryProbeService) StopPublicEntryProbe(_ context.Context, id string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active == nil {
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(service.active.id), []byte(id)) != 1 {
		return errors.New("deployer: public entry probe was not found")
	}
	service.closeActiveLocked()
	return nil
}

func (service *PublicEntryProbeService) expire(id string, after time.Duration) {
	timer := time.NewTimer(after)
	defer timer.Stop()
	<-timer.C
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.active != nil && subtle.ConstantTimeCompare([]byte(service.active.id), []byte(id)) == 1 {
		service.closeActiveLocked()
	}
}

func (service *PublicEntryProbeService) closeActiveLocked() {
	if service.active == nil {
		return
	}
	closeListeners(service.active.listeners)
	service.active = nil
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
