package deployer

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/deployapi"
)

// Reuse the bounded, secret-on-stdin client used for recovery acceptance.
// Serialize probes so background publication checks cannot exhaust a small host.
type MeridianProbeService struct {
	Socket       string
	RuntimeImage string
	once         sync.Once
	gate         chan struct{}
}

// Contention is not a failed connectivity observation. Wait for the existing
// probe within the caller's deadline rather than consuming Center retry attempts.
func (service *MeridianProbeService) acquire(ctx context.Context) error {
	service.once.Do(func() { service.gate = make(chan struct{}, 1) })
	select {
	case service.gate <- struct{}{}:
		if ctx.Err() != nil {
			<-service.gate
			return errors.New("deployer: proxy verification canceled")
		}
		return nil
	case <-ctx.Done():
		return errors.New("deployer: proxy verification canceled while queued")
	}
}

func (service *MeridianProbeService) VerifyMeridianEntry(ctx context.Context, input deployapi.MeridianProbeRequest) error {
	if err := service.acquire(ctx); err != nil {
		return err
	}
	defer func() { <-service.gate }()
	if err := (agent.ApplicationExecutor{DockerSocket: service.Socket}).VerifyMeridianClientInContainer(ctx, input.Client, input.ExpectedExit, service.RuntimeImage); err != nil {
		// No runtime or request errors containing credentials cross this boundary.
		return errors.New("deployer: authenticated proxy HTTPS or exit verification failed")
	}
	return nil
}

func (server *Server) WithMeridianProber(prober deployapi.MeridianProber) *Server {
	server.meridianProber = prober
	return server
}

func (server *Server) verifyMeridianEntry(writer http.ResponseWriter, request *http.Request) {
	if server.meridianProber == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("deployer: proxy verifier is unavailable"))
		return
	}
	var input deployapi.MeridianProbeRequest
	if !decodeRequest(writer, request, &input) {
		return
	}
	if err := server.meridianProber.VerifyMeridianEntry(request.Context(), input); err != nil {
		writeError(writer, http.StatusBadGateway, errors.New("deployer: authenticated proxy verification did not pass"))
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "verified"})
}
