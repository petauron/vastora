package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func (s *Store) landingHealth() *landing.Health {
	s.landingStatusMu.RLock()
	defer s.landingStatusMu.RUnlock()
	status := s.landingStatus
	if status.Revision == 0 {
		return nil
	}
	healthy := status.State == "healthy" && status.LinkState == "direct" && status.TCP && status.UDP && status.AllowedUntil.After(time.Now())
	return &landing.Health{Revision: status.Revision, Healthy: healthy, CheckedAt: status.CheckedAt}
}

func (s *Store) checkLandingApplicationMutation(ctx context.Context, appKey string) error {
	if appKey != threeXUIKey {
		return nil
	}
	state, err := s.landingRuntime(ctx)
	if err != nil {
		return err
	}
	if state != nil && state.Route != nil {
		return errors.New("agent: disable landing before updating or removing this proxy application")
	}
	return nil
}

func (s *Store) restoreLandingProxy(ctx context.Context) error {
	s.landingMutationMu.Lock()
	if s.landingCancel != nil {
		select {
		case <-s.landingDone:
			s.landingCancel = nil
			s.landingDone = nil
		default:
			s.landingMutationMu.Unlock()
			return nil
		}
	}
	state, err := s.landingRuntime(ctx)
	s.landingMutationMu.Unlock()
	if err != nil || state == nil || state.Route == nil {
		return err
	}
	return s.applyLandingProxy(ctx, state.Desired)
}

func waitLandingRoutes(ctx context.Context, routes threeXUILandingRoutes) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if _, _, err := routes.Read(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("agent: local proxy management did not become ready")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Store) stopLandingMonitor(ctx context.Context) error {
	if s.landingCancel == nil {
		return nil
	}
	s.landingCancel()
	select {
	case <-s.landingDone:
		s.landingCancel = nil
		s.landingDone = nil
		return nil
	case <-ctx.Done():
		return errors.New("agent: landing monitor is still stopping")
	}
}

func verifyLocalLandingInbounds(ctx context.Context, routes threeXUILandingRoutes, tags []string) error {
	raw, err := routes.request(ctx, http.MethodGet, "/panel/api/inbounds/list", nil)
	if err != nil {
		return err
	}
	var inbounds []threeXUIRealityInbound
	if json.Unmarshal(raw, &inbounds) != nil {
		return errors.New("agent: local inbound inventory is unavailable")
	}
	for _, tag := range tags {
		matches := 0
		for _, inbound := range inbounds {
			if inbound.Tag != tag {
				continue
			}
			var stream struct {
				Security string `json:"security"`
				Network  string `json:"network"`
			}
			if json.Unmarshal(inbound.StreamSettings, &stream) != nil {
				return errors.New("agent: invalid landing inbound transport")
			}
			vless := inbound.Protocol == "vless" && stream.Security == "reality" && (stream.Network == "tcp" || stream.Network == "raw")
			hy2 := inbound.Protocol == "hysteria" && stream.Security == "tls" && stream.Network == "hysteria" && strings.HasSuffix(inbound.Tag, "-hy2")
			if (inbound.NodeID != nil && *inbound.NodeID != 0) || inbound.Port != 443 || (!vless && !hy2) {
				return errors.New("agent: landing selection is not a local managed proxy inbound")
			}
			matches++
		}
		if matches != 1 {
			return errors.New("agent: selected local VLESS inbound was not found uniquely")
		}
	}
	return nil
}

func (s *Store) applyLandingProxy(ctx context.Context, desired landing.DesiredState) error {
	s.landingMutationMu.Lock()
	defer s.landingMutationMu.Unlock()
	if desired.Validate() != nil || desired.Server != nil {
		return errors.New("agent: invalid landing proxy intent")
	}
	connection, err := s.Connection(ctx)
	if err != nil || connection.AgentID != desired.NodeID {
		return errors.New("agent: landing proxy belongs to another node")
	}
	current, err := s.landingRuntime(ctx)
	if err != nil {
		return err
	}
	if current != nil {
		if desired.Revision < current.Desired.Revision {
			return errors.New("agent: stale landing proxy revision")
		}
		if desired.Revision == current.Desired.Revision {
			a, _ := json.Marshal(desired)
			b, _ := json.Marshal(current.Desired)
			if string(a) != string(b) {
				return errors.New("agent: landing proxy revision changed")
			}
		}
		if current.Route != nil && desired.Proxy != nil && desired.Revision != current.Desired.Revision {
			if current.Phase != "applied" || current.Applied == nil || current.Applied.Revision != current.Desired.Revision || current.ApplicationID != desired.Proxy.ApplicationID {
				return errors.New("agent: finish or restore the current landing change before switching")
			}
			// Do not stop a working exit while the replacement is unreachable.
			if err := landing.WaitReady(ctx, desired.Proxy.Peer, desired.Revision); err != nil {
				return err
			}
		}
	}
	if err := s.stopLandingMonitor(ctx); err != nil {
		return err
	}
	if desired.Proxy == nil {
		return s.disableLandingProxy(ctx, desired, current)
	}
	routes, err := s.localThreeXUILandingRoutes(ctx, desired.Proxy.ApplicationID)
	if err != nil {
		return err
	}
	expectedID := ""
	if current != nil && current.Route != nil {
		expectedID = current.ContainerID
	}
	docker, bridge, policy, err := openLandingDocker(ctx, desired.Proxy.ApplicationID, expectedID)
	if err != nil {
		return err
	}
	defer docker.engine.Close()
	gate, err := landing.NewBridgeGate(desired.Proxy.Peer, bridge, desired.Revision)
	if err != nil {
		return err
	}
	if current != nil && current.Route != nil && desired.Revision > current.Desired.Revision {
		if bridge != current.Bridge {
			return errors.New("agent: landing proxy bridge changed")
		}
		// Stopping the previous monitor terminates connections and restarts
		// this instance. Wait for its management API before taking the new
		// checkpoint; a running container alone is not readiness evidence.
		if err := waitLandingRoutes(ctx, routes); err != nil {
			return err
		}
		if err := verifyLocalLandingInbounds(ctx, routes, desired.Proxy.InboundTags); err != nil {
			return err
		}
		raw, _, err := routes.Read(ctx)
		if err != nil {
			return err
		}
		change, err := landing.PrepareRouteReplacement(*current.Route, raw, desired.Revision, desired.Proxy.InboundTags, desired.Proxy.Peer)
		if err != nil {
			return err
		}
		previous := current.Desired
		current.Desired, current.Retiring, current.Route, current.Phase = desired, &previous, &change, "prepared"
		if err := s.saveLandingRuntime(ctx, *current); err != nil {
			return err
		}
	}
	if current == nil || current.Route == nil {
		// Docker alone does not guarantee the nft userspace tool exists. Install
		// the host prerequisites before persisting or changing any proxy route.
		if err := ensureLandingPackage(ctx); err != nil {
			return err
		}
		if err := verifyLocalLandingInbounds(ctx, routes, desired.Proxy.InboundTags); err != nil {
			return err
		}
		if err := landing.WaitReady(ctx, desired.Proxy.Peer, desired.Revision); err != nil {
			return err
		}
		raw, _, err := routes.Read(ctx)
		if err != nil {
			return err
		}
		change, err := landing.PrepareRouteChange(raw, desired.Revision, desired.Proxy.InboundTags, desired.Proxy.Peer)
		if err != nil {
			return err
		}
		current = &landingRuntimeState{Desired: desired, ApplicationID: desired.Proxy.ApplicationID, ContainerID: docker.containerID, Bridge: bridge, RestartPolicy: policy, Route: &change, Phase: "prepared"}
		if err := s.saveLandingRuntime(ctx, *current); err != nil {
			return err
		}
	}
	if current.Bridge != bridge {
		return errors.New("agent: landing proxy bridge changed")
	}
	if err := gate.Install(ctx); err != nil {
		return err
	}
	if err := removeRetiringLandingGate(ctx, current); err != nil {
		return err
	}
	if err := docker.restartPolicy(ctx, "no"); err != nil {
		return err
	}
	// This also starts a boot-stopped instance under the closed gate so the
	// node-local management API can reconcile its saved configuration.
	if err := docker.startForReconciliation(ctx); err != nil {
		return err
	}
	if err := waitLandingRoutes(ctx, routes); err != nil {
		return err
	}
	if err := routes.Apply(ctx, *current.Route, true); err != nil {
		return err
	}
	if err := docker.terminateConnections(ctx); err != nil {
		return err
	}
	// This synchronous cutover also fences restored checkpoints after Agent
	// restart. Monitor must not repeat the same container restart on entry.
	current.Desired = desired
	current.Applied = &desired
	current.Retiring = nil
	current.Phase = "applied"
	if err := s.saveLandingRuntime(ctx, *current); err != nil {
		return err
	}
	return s.startLandingMonitor(*current)
}

// Caller holds landingMutationMu. Direct routing is restored only on an
// explicit disable, never as an automatic response to link or probe failure.
func (s *Store) disableLandingProxy(ctx context.Context, desired landing.DesiredState, current *landingRuntimeState) error {
	if current == nil || current.Route == nil {
		return s.saveLandingRuntime(ctx, landingRuntimeState{Desired: desired, Applied: &desired, Phase: "applied"})
	}
	owner := current.Desired
	if current.Applied != nil && current.Applied.Revision == current.Route.Revision {
		owner = *current.Applied
	}
	if owner.Proxy == nil {
		return errors.New("agent: missing landing route owner")
	}
	current.Applied = &owner
	current.Desired = desired
	current.Phase = "restoring"
	if err := s.saveLandingRuntime(ctx, *current); err != nil {
		return err
	}
	docker, bridge, _, err := openLandingDocker(ctx, current.ApplicationID, current.ContainerID)
	if err != nil {
		return err
	}
	defer docker.engine.Close()
	if bridge != current.Bridge {
		return errors.New("agent: landing recovery bridge changed")
	}
	gate, err := landing.NewBridgeGate(owner.Proxy.Peer, bridge, current.Route.Revision)
	if err != nil {
		return err
	}
	if err := gate.Install(ctx); err != nil {
		return err
	}
	if err := docker.restartPolicy(ctx, "no"); err != nil {
		return err
	}
	if err := docker.startForReconciliation(ctx); err != nil {
		return err
	}
	routes, err := s.localThreeXUILandingRoutes(ctx, current.ApplicationID)
	if err != nil {
		return err
	}
	if err := waitLandingRoutes(ctx, routes); err != nil {
		return err
	}
	if err := routes.Apply(ctx, *current.Route, false); err != nil {
		return err
	}
	if err := docker.terminateConnections(ctx); err != nil {
		return err
	}
	if err := docker.restartPolicy(ctx, current.RestartPolicy); err != nil {
		return err
	}
	if err := removeRetiringLandingGate(ctx, current); err != nil {
		return err
	}
	if err := gate.Remove(ctx); err != nil {
		return err
	}
	return s.saveLandingRuntime(ctx, landingRuntimeState{Desired: desired, Applied: &desired, Phase: "applied"})
}

// A replacement gate is already installed and closed before its predecessor
// is removed. The encrypted checkpoint keeps cleanup retryable after a crash.
func removeRetiringLandingGate(ctx context.Context, state *landingRuntimeState) error {
	if state.Retiring == nil {
		return nil
	}
	gate, err := landing.NewBridgeGate(state.Retiring.Proxy.Peer, state.Bridge, state.Retiring.Revision)
	if err != nil {
		return err
	}
	// Install also closes an existing lease. It makes cleanup idempotent
	// when an earlier attempt already removed this table before crashing.
	if err := gate.Install(ctx); err != nil {
		return err
	}
	return gate.Remove(ctx)
}

func (s *Store) startLandingMonitor(state landingRuntimeState) error {
	ctx, cancel := context.WithCancel(context.Background())
	gate, err := landing.NewBridgeGate(state.Desired.Proxy.Peer, state.Bridge, state.Route.Revision)
	if err != nil {
		cancel()
		return err
	}
	s.landingCancel = cancel
	s.landingDone = make(chan struct{})
	done := s.landingDone
	go func() {
		defer close(done)
		monitor := landing.Monitor{Gate: gate, Links: landing.NewLinkChecker(), CheckBusiness: (landing.Probe{}).Check,
			StopConnections: func(ctx context.Context) error {
				docker, bridge, _, err := openLandingDocker(ctx, state.ApplicationID, state.ContainerID)
				if err != nil {
					return err
				}
				defer docker.engine.Close()
				if bridge != state.Bridge {
					return errors.New("agent: monitored proxy bridge changed")
				}
				return docker.terminateConnections(ctx)
			}, Report: func(status landing.MonitorStatus) {
				s.landingStatusMu.Lock()
				s.landingStatus = status
				s.landingStatusMu.Unlock()
			},
		}
		if err := monitor.Run(ctx); err != nil {
			s.landingStatusMu.Lock()
			s.landingStatus = landing.MonitorStatus{Revision: state.Desired.Revision, State: "blocked", Reason: "monitor_stopped", CheckedAt: time.Now().UTC()}
			s.landingStatusMu.Unlock()
		}
	}()
	return nil
}
