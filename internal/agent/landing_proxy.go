package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
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
		if current.Route != nil && desired.Active() && desired.Revision != current.Desired.Revision {
			if current.Phase != "applied" || current.Applied == nil || current.Applied.Revision != current.Desired.Revision || current.ApplicationID != desired.ApplicationID() {
				return errors.New("agent: finish or restore the current landing change before switching")
			}
			// Do not stop a working exit while the replacement is unreachable.
			if err := waitLandingPeers(ctx, desired); err != nil {
				return err
			}
		}
	}
	if err := s.stopLandingMonitor(ctx); err != nil {
		return err
	}
	if !desired.Active() {
		return s.disableLandingProxy(ctx, desired, current)
	}
	routes, err := s.localThreeXUILandingRoutes(ctx, desired.ApplicationID())
	if err != nil {
		return err
	}
	expectedID := ""
	if current != nil && current.Route != nil {
		expectedID = current.ContainerID
	}
	docker, bridge, policy, err := openLandingDocker(ctx, desired.ApplicationID(), expectedID)
	if err != nil {
		return err
	}
	defer docker.engine.Close()
	gates, err := landingGates(desired, bridge)
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
		if err := verifyLocalLandingPlan(ctx, routes, desired); err != nil {
			return err
		}
		raw, _, err := routes.Read(ctx)
		if err != nil {
			return err
		}
		change, err := desired.ReplaceRoutes(*current.Route, raw)
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
		if err := verifyLocalLandingPlan(ctx, routes, desired); err != nil {
			return err
		}
		if err := waitLandingPeers(ctx, desired); err != nil {
			return err
		}
		raw, _, err := routes.Read(ctx)
		if err != nil {
			return err
		}
		change, err := desired.PrepareRoutes(raw)
		if err != nil {
			return err
		}
		current = &landingRuntimeState{Desired: desired, ApplicationID: desired.ApplicationID(), ContainerID: docker.containerID, Bridge: bridge, RestartPolicy: policy, Route: &change, Phase: "prepared"}
		if err := s.saveLandingRuntime(ctx, *current); err != nil {
			return err
		}
	}
	if current.Bridge != bridge {
		return errors.New("agent: landing proxy bridge changed")
	}
	for _, gate := range gates {
		if err := gate.Install(ctx); err != nil {
			return err
		}
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
	if err := removeRetiringLandingGate(ctx, current); err != nil {
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
	if !owner.Active() {
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
	gates, err := landingGates(owner, bridge)
	if err != nil {
		return err
	}
	for _, gate := range gates {
		if err := gate.Install(ctx); err != nil {
			return err
		}
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
	for _, gate := range gates {
		if err := gate.Remove(ctx); err != nil {
			return err
		}
	}
	return s.saveLandingRuntime(ctx, landingRuntimeState{Desired: desired, Applied: &desired, Phase: "applied"})
}

// A replacement gate is already installed and closed before its predecessor
// is removed. The encrypted checkpoint keeps cleanup retryable after a crash.
func removeRetiringLandingGate(ctx context.Context, state *landingRuntimeState) error {
	if state.Retiring == nil {
		return nil
	}
	gates, err := landingGates(*state.Retiring, state.Bridge)
	if err != nil {
		return err
	}
	// Install also closes an existing lease. It makes cleanup idempotent
	// when an earlier attempt already removed this table before crashing.
	for _, gate := range gates {
		if err := gate.Install(ctx); err != nil {
			return err
		}
		if err := gate.Remove(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) startLandingMonitor(state landingRuntimeState) error {
	ctx, cancel := context.WithCancel(context.Background())
	gates, err := landingGates(state.Desired, state.Bridge)
	if err != nil {
		cancel()
		return err
	}
	s.landingCancel = cancel
	s.landingDone = make(chan struct{})
	done := s.landingDone
	go func() {
		defer close(done)
		var monitors sync.WaitGroup
		// Several peers may fail together, but their session cutovers must not
		// race Docker stop/start on the shared selected proxy instance.
		var cutover sync.Mutex
		shutdownStopped := false
		stopConnections := func(stopCtx context.Context) error {
			cutover.Lock()
			defer cutover.Unlock()
			if shutdownStopped {
				return nil
			}
			docker, bridge, _, err := openLandingDocker(stopCtx, state.ApplicationID, state.ContainerID)
			if err != nil {
				return err
			}
			defer docker.engine.Close()
			if bridge != state.Bridge {
				return errors.New("agent: monitored proxy bridge changed")
			}
			err = docker.terminateConnections(stopCtx)
			if ctx.Err() != nil && err == nil {
				shutdownStopped = true
			}
			return err
		}
		uses := state.Desired.PeerUses()
		for i, gate := range gates {
			use := uses[i]
			if !use.Active {
				continue
			}
			monitors.Add(1)
			go func() {
				defer monitors.Done()
				checkBusiness := func(checkCtx context.Context, peer landing.PeerIdentity, revision uint64) (landing.BusinessResult, error) {
					if state.Desired.Clients != nil {
						routes, err := s.localThreeXUILandingRoutes(checkCtx, state.ApplicationID)
						if err != nil {
							return landing.BusinessResult{}, err
						}
						selected := state.Desired
						selected.Proxy = nil
						selected.Clients = &landing.ClientPlan{ApplicationID: state.ApplicationID, Source: state.Desired.Clients.Source, AllowSessionReset: true}
						for _, grant := range state.Desired.Clients.Grants {
							if grant.Enabled && grant.Peer == peer {
								selected.Clients.Grants = append(selected.Clients.Grants, grant)
							}
						}
						if len(selected.Clients.Grants) > 0 {
							if err := verifyLocalLandingPlan(checkCtx, routes, selected); err != nil {
								return landing.BusinessResult{}, err
							}
						}
					}
					return (landing.Probe{TCPOnly: use.TCPOnly}).Check(checkCtx, peer, revision)
				}
				monitor := landing.Monitor{Gate: gate, Links: landing.NewLinkChecker(), TCPOnly: use.TCPOnly, CheckBusiness: checkBusiness,
					StopConnections: stopConnections, Report: func(status landing.MonitorStatus) {
						s.landingStatusMu.Lock()
						if state.Desired.Proxy != nil && use.Peer == state.Desired.Proxy.Peer {
							s.landingStatus = status
						}
						if s.landingClientStatuses == nil {
							s.landingClientStatuses = map[string]landing.MonitorStatus{}
						}
						s.landingClientStatuses[use.Peer.ID] = status
						s.landingStatusMu.Unlock()
					},
				}
				if err := monitor.Run(ctx); err != nil {
					s.landingStatusMu.Lock()
					blocked := landing.MonitorStatus{Revision: state.Desired.Revision, State: "blocked", Reason: "monitor_stopped", CheckedAt: time.Now().UTC()}
					if state.Desired.Proxy != nil && use.Peer == state.Desired.Proxy.Peer {
						s.landingStatus = blocked
					}
					if s.landingClientStatuses == nil {
						s.landingClientStatuses = map[string]landing.MonitorStatus{}
					}
					s.landingClientStatuses[use.Peer.ID] = blocked
					s.landingStatusMu.Unlock()
				}
			}()
		}
		monitors.Wait()
	}()
	return nil
}

func landingGates(state landing.DesiredState, bridge string) ([]*landing.BridgeGate, error) {
	gates := []*landing.BridgeGate{}
	for _, use := range state.PeerUses() {
		gate, err := landing.NewBridgeGate(use.Peer, bridge, state.Revision)
		if err != nil {
			return nil, err
		}
		gates = append(gates, gate)
	}
	return gates, nil
}

func waitLandingPeers(ctx context.Context, state landing.DesiredState) error {
	for _, use := range state.PeerUses() {
		if !use.Active {
			continue
		}
		var err error
		if use.TCPOnly {
			err = landing.WaitTCPReady(ctx, use.Peer, state.Revision)
		} else {
			err = landing.WaitReady(ctx, use.Peer, state.Revision)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func verifyLocalLandingPlan(ctx context.Context, routes threeXUILandingRoutes, state landing.DesiredState) error {
	if err := verifyLocalLandingInbounds(ctx, routes, state.Inbounds()); err != nil {
		return err
	}
	if state.Clients == nil {
		return nil
	}
	if slices.ContainsFunc(state.Clients.Grants, func(grant landing.ClientGrant) bool { return grant.Enabled }) {
		self, err := landing.NewLinkChecker().SelfIdentity(ctx, state.Clients.Source.Address)
		if err != nil {
			return errors.New("agent: entry private identity is unavailable")
		}
		if err := state.Clients.CheckSource(self); err != nil {
			return err
		}
	}
	raw, err := routes.request(ctx, http.MethodGet, "/panel/api/inbounds/list", nil)
	if err != nil {
		return err
	}
	var inbounds []threeXUIRealityInbound
	if json.Unmarshal(raw, &inbounds) != nil {
		return errors.New("agent: client inbound inventory unavailable")
	}
	for _, block := range state.Clients.BlockedUsers {
		for _, inbound := range inbounds {
			var settings struct {
				Clients []struct {
					Email string `json:"email"`
					ID    string `json:"id"`
				} `json:"clients"`
			}
			if json.Unmarshal(inbound.Settings, &settings) != nil {
				return errors.New("agent: blocked identity inventory unavailable")
			}
			for _, client := range settings.Clients {
				if client.Email == block.User && landing.Identity(client.ID) != block.Identity {
					return errors.New("agent: cannot block a replacement account")
				}
			}
		}
	}
	for _, grant := range state.Clients.Grants {
		// Revocation only adds a deny rule. A removed account must not prevent
		// that rule from being installed or require an offline peer to answer.
		if !grant.Enabled {
			continue
		}
		for _, inbound := range inbounds {
			if inbound.Tag != grant.InboundTag {
				continue
			}
			if inbound.Protocol != "vless" {
				return errors.New("agent: client landing requires managed VLESS")
			}
			var settings struct {
				Clients []struct {
					Email string `json:"email"`
					ID    string `json:"id"`
				} `json:"clients"`
			}
			if json.Unmarshal(inbound.Settings, &settings) != nil {
				return errors.New("agent: client inventory unavailable")
			}
			foundBase, foundFixed := false, grant.FixedUser == "" || !grant.Enabled || !grant.Mode.Fixed()
			for _, client := range settings.Clients {
				if client.Email == grant.BaseUser && landing.Identity(client.ID) == grant.BaseIdentity {
					foundBase = true
				}
				if client.Email == grant.FixedUser && landing.Identity(client.ID) == grant.FixedIdentity {
					foundFixed = true
				}
			}
			if !foundBase || !foundFixed {
				return errors.New("agent: granted user does not match local inbound")
			}
		}
	}
	return nil
}
