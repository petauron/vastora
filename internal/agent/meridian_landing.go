package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
	"github.com/petauron/vastora/internal/secret"
)

// A gate identity is retained until its closed kernel table has been removed.
// It is not a second routing authority: only the Meridian artifact owns routes.
type meridianGateIdentity struct {
	Peer     landing.PeerIdentity `json:"peer"`
	Bridge   string               `json:"bridge"`
	Revision uint64               `json:"revision"`
}

type meridianTrafficGate interface {
	Install(context.Context) error
	Remove(context.Context) error
}

func newMeridianTrafficGate(identity meridianGateIdentity) (meridianTrafficGate, error) {
	return landing.NewBridgeGate(identity.Peer, identity.Bridge, identity.Revision)
}

func (state meridianRuntimeState) validateLanding() error {
	for _, plan := range []struct {
		artifact *meridian.DesiredArtifact
		peers    []meridianruntime.Peer
		source   *landing.PeerIdentity
	}{{state.Applied, state.AppliedPeers, state.AppliedSource}, {state.Pending, state.PendingPeers, state.PendingSource}} {
		if plan.artifact == nil {
			if len(plan.peers) != 0 || plan.source != nil {
				return errors.New("agent: Meridian peers have no owning artifact")
			}
			continue
		}
		task := meridianruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: *plan.artifact, Peers: plan.peers, Source: plan.source}
		if task.Validate() != nil {
			return errors.New("agent: Meridian peer plan does not match its artifact")
		}
		for _, identity := range meridianPlanGates(plan.peers, state.Bridge, plan.artifact.Revision) {
			if _, err := newMeridianTrafficGate(identity); err != nil {
				return err
			}
		}
	}
	for _, identity := range state.RetiringGates {
		if _, err := newMeridianTrafficGate(identity); err != nil {
			return err
		}
	}
	return nil
}

func meridianPlanGates(peers []meridianruntime.Peer, bridge string, revision uint64) []meridianGateIdentity {
	gates := make([]meridianGateIdentity, 0, len(peers))
	for _, peer := range peers {
		gates = append(gates, meridianGateIdentity{Peer: peer.Identity, Bridge: bridge, Revision: revision})
	}
	return gates
}

func sameMeridianPeers(left, right []meridianruntime.Peer) bool {
	if len(left) != len(right) {
		return false
	}
	for _, peer := range left {
		if !slices.Contains(right, peer) {
			return false
		}
	}
	return true
}

func cloneMeridianSource(source *landing.PeerIdentity) *landing.PeerIdentity {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func sameMeridianSource(left, right *landing.PeerIdentity) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func appendMeridianGates(destination []meridianGateIdentity, additions ...meridianGateIdentity) []meridianGateIdentity {
	for _, identity := range additions {
		if !slices.Contains(destination, identity) {
			destination = append(destination, identity)
		}
	}
	return destination
}

func (state meridianRuntimeState) knownLandingGates() []meridianGateIdentity {
	gates := appendMeridianGates(nil, state.RetiringGates...)
	if state.Applied != nil {
		gates = appendMeridianGates(gates, meridianPlanGates(state.AppliedPeers, state.Bridge, state.Applied.Revision)...)
	}
	if state.Pending != nil {
		gates = appendMeridianGates(gates, meridianPlanGates(state.PendingPeers, state.Bridge, state.Pending.Revision)...)
	}
	return gates
}

func closeMeridianGates(ctx context.Context, gates []meridianGateIdentity, factory func(meridianGateIdentity) (meridianTrafficGate, error)) error {
	var result error
	for _, identity := range gates {
		gate, err := factory(identity)
		if err == nil {
			err = gate.Install(ctx)
		}
		result = errors.Join(result, err)
	}
	return result
}

// New permission is never opened here. Shared identities are adopted rather
// than removed; a superseded DROP table is removed only behind closed new gates.
func removeSupersededMeridianGates(ctx context.Context, previous, current []meridianGateIdentity, factory func(meridianGateIdentity) (meridianTrafficGate, error)) error {
	if err := closeMeridianGates(ctx, appendMeridianGates(slices.Clone(previous), current...), factory); err != nil {
		return err
	}
	for _, identity := range previous {
		if slices.Contains(current, identity) {
			continue
		}
		gate, err := factory(identity)
		if err != nil {
			return err
		}
		if err := gate.Remove(ctx); err != nil {
			return err
		}
	}
	return nil
}

func meridianRuntimeBridge(ctx context.Context, docker *client.Client) (string, string, error) {
	result, err := docker.NetworkInspect(ctx, dockerruntime.NetworkName, client.NetworkInspectOptions{})
	if err != nil {
		return "", "", errors.New("agent: Meridian bridge is unavailable")
	}
	network := result.Network
	if network.ID == "" || network.Driver != "bridge" || network.Name != dockerruntime.NetworkName || network.Labels[dockerruntime.ManagedLabel] != "true" || network.Labels[dockerruntime.ComponentLabel] != "runtime-network" {
		return "", "", errors.New("agent: Meridian bridge ownership changed")
	}
	bridge := network.Options["com.docker.network.bridge.name"]
	if bridge == "" {
		if len(network.ID) < 12 {
			return "", "", errors.New("agent: Meridian bridge identity is invalid")
		}
		bridge = "br-" + network.ID[:12]
	}
	return bridge, network.ID, nil
}

// Read before the one-way boundary. The encrypted source remains available for
// compare-and-delete at explicit retirement, not as a runnable fallback.
func (s *Store) captureMeridianLegacyLanding(ctx context.Context, applicationID string) ([]byte, []meridianGateIdentity, error) {
	state, err := s.landingRuntime(ctx)
	if err != nil || state == nil {
		return nil, nil, err
	}
	if state.Route != nil && (state.ApplicationID != applicationID || state.Phase != "applied" || state.Applied == nil || state.Applied.Revision != state.Desired.Revision || state.Retiring != nil) {
		return nil, nil, errors.New("agent: legacy landing requires explicit reconciliation before Meridian handover")
	}
	if state.Desired.Active() && state.Desired.ApplicationID() != applicationID {
		return nil, nil, errors.New("agent: legacy landing belongs to another application")
	}
	if state.Route == nil && state.Desired.Active() {
		return nil, nil, errors.New("agent: legacy landing has no applied route evidence")
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM landing_runtime_state WHERE id=1`).Scan(&sealed); err != nil {
		return nil, nil, err
	}
	gates := []meridianGateIdentity{}
	if state.Route != nil {
		for _, use := range state.Desired.PeerUses() {
			gates = appendMeridianGates(gates, meridianGateIdentity{Peer: use.Peer, Bridge: state.Bridge, Revision: state.Desired.Revision})
		}
	}
	return sealed, gates, nil
}

// The first Meridian import has no landing peers or migrated child credentials.
// Its complete native-only artifact intentionally replaces the legacy route,
// so route parity is not a prerequisite for that one-way handover. Peer-bearing
// revisions still require the exact applied route and landing plan.
func (s *Store) verifyMeridianLegacyRouteHandover(ctx context.Context, task meridianruntime.Task, legacy *landingRuntimeState) error {
	if len(task.Peers) == 0 {
		return nil
	}
	routes, err := s.localThreeXUILandingRoutes(ctx, task.ApplicationID)
	if err != nil {
		return err
	}
	raw, _, err := routes.Read(ctx)
	if err != nil {
		return err
	}
	if _, write, err := legacy.Route.NextWrite(raw, true); err != nil || write {
		return errors.New("agent: legacy landing route changed before Meridian handover")
	}
	return s.verifyLocalLandingPlan(ctx, routes, legacy.Desired)
}

func (s *Store) verifyMeridianLegacyLanding(ctx context.Context, expected []byte) error {
	var actual []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM landing_runtime_state WHERE id=1`).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) && len(expected) == 0 {
		return nil
	}
	if err != nil || !bytes.Equal(actual, expected) {
		return errors.New("agent: legacy landing journal changed during Meridian handover")
	}
	return nil
}

func (s *Store) requireLegacyLandingAuthority(ctx context.Context) error {
	_, err := s.loadMeridianRuntimeState(ctx)
	if errors.Is(err, errApplicationNotInstalled) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("agent: Meridian owns the proxy runtime; legacy landing mutations are retired")
}

// Called by the replacement's beforeStop hook under landingMutationMu. All
// expensive preparation has already succeeded; from this journal onward no
// restart may restore the legacy writer or its monitor.
func (e ApplicationExecutor) beginMeridianLandingHandover(ctx context.Context, docker *client.Client, state *meridianRuntimeState, task meridianruntime.Task) error {
	if task.Source != nil {
		checker := landing.NewLinkChecker()
		actual, err := checker.SelfIdentity(ctx, task.Source.Address)
		checker.Close()
		if err != nil || actual != *task.Source {
			return errors.New("agent: Meridian entry private identity changed before handover")
		}
	}
	bridge, _, err := meridianRuntimeBridge(ctx, docker)
	if err != nil {
		return err
	}
	if state.Applied != nil && len(state.AppliedPeers) != 0 && state.Bridge != bridge {
		return errors.New("agent: Meridian applied bridge changed before handover")
	}
	retiring := state.knownLandingGates()
	if state.Applied == nil && state.LegacyRuntimeSHA256 == "" {
		current, _, exists, err := inspectCurrentMeridianRuntime(ctx, docker)
		if err != nil {
			return err
		}
		if exists {
			if current.Container.Config == nil || current.Container.Config.Labels[xrayWorkerRuntimeLabel] != "xray" || current.Container.Config.Labels[applicationIdentityLabel] != threeXUIKey || current.Container.Config.Labels[applicationInstallationLabel] != task.ApplicationID {
				return errors.New("agent: legacy proxy must be an owned managed Xray worker before Meridian handover")
			}
			state.LegacyRuntimeSHA256, err = e.Store.meridianLegacyWorkerDigest(ctx, task.ApplicationID)
			if err != nil {
				return err
			}
		}
	}
	if state.Applied == nil && len(state.LegacyLandingSealed) == 0 {
		sealed, gates, err := e.Store.captureMeridianLegacyLanding(ctx, task.ApplicationID)
		if err != nil {
			return err
		}
		state.LegacyLandingSealed = sealed
		retiring = appendMeridianGates(retiring, gates...)
		if len(gates) != 0 {
			legacy, err := e.Store.landingRuntime(ctx)
			if err != nil || legacy == nil || legacy.Bridge != bridge {
				return errors.New("agent: legacy landing bridge changed before Meridian handover")
			}
			if legacy.Desired.Clients != nil && len(task.Peers) != 0 && (task.Source == nil || legacy.Desired.Clients.Source != *task.Source) {
				return errors.New("agent: Meridian source identity differs from the authorized legacy entry")
			}
			if err := e.Store.verifyMeridianLegacyRouteHandover(ctx, task, legacy); err != nil {
				return err
			}
			current, _, exists, err := inspectCurrentMeridianRuntime(ctx, docker)
			if err != nil || !exists || current.Container.ID != legacy.ContainerID || current.Container.HostConfig == nil || current.Container.HostConfig.RestartPolicy.Name != "no" {
				return errors.New("agent: legacy landing container changed before Meridian handover")
			}
		}
	}
	if err := e.Store.verifyMeridianLegacyLanding(ctx, state.LegacyLandingSealed); err != nil {
		return err
	}
	state.RetiringGates = retiring
	state.Bridge = bridge
	state.Pending, state.PendingPeers = &task.Desired, slices.Clone(task.Peers)
	state.PendingSource = cloneMeridianSource(task.Source)
	state.HandoverPending = true
	if err := e.Store.saveMeridianRuntimeState(ctx, *state); err != nil {
		return err
	}
	if err := e.Store.stopLandingMonitor(ctx); err != nil {
		return uncertainTaskOutcome(err)
	}
	if err := closeMeridianGates(ctx, state.knownLandingGates(), newMeridianTrafficGate); err != nil {
		return uncertainTaskOutcome(err)
	}
	if err := e.Store.stopXrayWorkerAPI(ctx, false); err != nil {
		return uncertainTaskOutcome(err)
	}
	if state.Applied == nil && state.LegacyRuntimeSHA256 != "" {
		actual, err := e.Store.meridianLegacyWorkerDigest(ctx, task.ApplicationID)
		if err != nil || actual != state.LegacyRuntimeSHA256 {
			return uncertainTaskOutcome(errors.Join(errors.New("agent: legacy runtime changed while draining its writer"), err))
		}
	}
	return nil
}

// The API and its reconciler use the same mutex. Compare rendered authority,
// not sealed accounting counters, before and after draining those writers.
func (s *Store) meridianLegacyWorkerDigest(ctx context.Context, applicationID string) (string, error) {
	s.xrayWorkerStateMu.Lock()
	defer s.xrayWorkerStateMu.Unlock()
	state, err := s.loadXrayWorkerState(ctx)
	if err != nil || state.ApplicationID != applicationID || state.AppliedRevision != state.Revision || !s.xrayWorkerAppliedReceiptMatches(state) {
		return "", errors.New("agent: legacy worker cannot prove its applied configuration")
	}
	raw, err := renderXrayWorkerConfig(state)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func (e ApplicationExecutor) finishMeridianLandingHandover(ctx context.Context, state *meridianRuntimeState) error {
	if state.Applied == nil || state.Pending != nil {
		return errors.New("agent: Meridian landing handover has no applied authority")
	}
	if _, err := e.observeAppliedMeridianRuntime(ctx, *state); err != nil {
		return err
	}
	current := meridianPlanGates(state.AppliedPeers, state.Bridge, state.Applied.Revision)
	if err := removeSupersededMeridianGates(ctx, state.RetiringGates, current, newMeridianTrafficGate); err != nil {
		return err
	}
	state.RetiringGates, state.HandoverPending = nil, false
	if err := e.Store.saveMeridianRuntimeState(ctx, *state); err != nil {
		return err
	}
	return e.startMeridianLandingMonitor(ctx, *state)
}

// A peer-bearing runtime cannot auto-start after reboot before nft gates exist.
// Only an exact, journaled Meridian container may be started behind closed gates.
func (e ApplicationExecutor) prepareMeridianRuntimeStart(ctx context.Context, docker *client.Client, state meridianRuntimeState, artifact meridian.DesiredArtifact, peers []meridianruntime.Peer) error {
	if err := closeMeridianGates(ctx, state.knownLandingGates(), newMeridianTrafficGate); err != nil {
		return err
	}
	current, exists, err := inspectXrayWorkerContainer(ctx, docker, meridianXrayContainer)
	if err != nil || !exists || current.Container.Config == nil || current.Container.HostConfig == nil || current.Container.State == nil || current.Container.Config.Labels[applicationIdentityLabel] != meridianKey || current.Container.Config.Labels[applicationInstallationLabel] != state.ApplicationID || current.Container.Config.Image != state.ImageReference {
		return errors.New("agent: Meridian boot runtime identity is unavailable")
	}
	active, err := os.ReadFile(filepath.Join(e.Store.dataDir, meridianRuntimeDirectory, "config.json"))
	if err != nil || !artifactMatchesBytes(artifact, active) {
		return errors.New("agent: Meridian boot configuration does not match its journal")
	}
	if err := e.verifyMeridianRuntimeAttachment(ctx, docker, current, state, peers); err != nil {
		return err
	}
	if !current.Container.State.Running {
		if current.Container.HostConfig.RestartPolicy.Name != "no" {
			return errors.New("agent: stopped Meridian runtime requires explicit recovery")
		}
		if _, err := docker.ContainerStart(ctx, current.Container.ID, client.ContainerStartOptions{}); err != nil {
			return err
		}
	}
	return waitForXrayWorkerRuntime(ctx, docker, current.Container.ID)
}

// Bind the on-disk digest to the container's actual read-only configuration
// mount, and the peer gates to its actual managed network identity, not a name
// which could have been reused after Docker network recreation.
func (e ApplicationExecutor) verifyMeridianRuntimeAttachment(ctx context.Context, docker *client.Client, inspected client.ContainerInspectResult, state meridianRuntimeState, peers []meridianruntime.Peer) error {
	if inspected.Container.Config == nil || !slices.Equal([]string(inspected.Container.Config.Cmd), []string{"run", "-c", xrayWorkerConfigPath}) {
		return errors.New("agent: Meridian configuration command changed")
	}
	matches := 0
	for _, mounted := range inspected.Container.Mounts {
		if strings.HasPrefix(mounted.Destination, filepath.Dir(xrayWorkerConfigPath)+"/") {
			return errors.New("agent: Meridian configuration mount is shadowed")
		}
		if mounted.Destination != filepath.Dir(xrayWorkerConfigPath) {
			continue
		}
		if string(mounted.Type) != "bind" || mounted.Source != filepath.Join(e.Store.dataDir, meridianRuntimeDirectory) || mounted.RW {
			return errors.New("agent: Meridian configuration mount does not match its journal")
		}
		matches++
	}
	if matches != 1 {
		return errors.New("agent: Meridian configuration mount is unavailable or ambiguous")
	}
	if len(peers) == 0 {
		return nil
	}
	bridge, networkID, err := meridianRuntimeBridge(ctx, docker)
	if err != nil || bridge != state.Bridge || inspected.Container.HostConfig == nil || string(inspected.Container.HostConfig.NetworkMode) != dockerruntime.NetworkName || inspected.Container.HostConfig.RestartPolicy.Name != "no" || inspected.Container.NetworkSettings == nil {
		return errors.New("agent: Meridian landing fence does not match its journal")
	}
	attachment := inspected.Container.NetworkSettings.Networks[dockerruntime.NetworkName]
	if attachment == nil || attachment.NetworkID != networkID {
		return errors.New("agent: Meridian landing network identity changed")
	}
	return nil
}

func meridianContainerLandingPolicy(options *client.ContainerCreateOptions, peers []meridianruntime.Peer) {
	if len(peers) != 0 {
		options.HostConfig.RestartPolicy = container.RestartPolicy{Name: "no"}
	}
}

func (e ApplicationExecutor) startMeridianLandingMonitor(ctx context.Context, state meridianRuntimeState) error {
	if state.Applied == nil || state.Pending != nil || state.HandoverPending || state.validate() != nil {
		return errors.New("agent: Meridian monitor requires a completed applied handover")
	}
	if err := e.Store.stopLandingMonitor(ctx); err != nil {
		return err
	}
	monitorContext, cancel := context.WithCancel(context.Background())
	e.Store.landingStatusMu.Lock()
	e.Store.meridianMonitorRevision, e.Store.meridianMonitorSHA256 = state.Applied.Revision, state.Applied.ConfigSHA256
	e.Store.meridianMonitorSource = cloneMeridianSource(state.AppliedSource)
	e.Store.meridianPeerStatuses = map[string]meridianruntime.PeerObservation{}
	e.Store.landingPeerStatuses = map[string]landingPeerStatus{}
	e.Store.landingStatus = landing.MonitorStatus{}
	for _, peer := range state.AppliedPeers {
		e.Store.meridianPeerStatuses[peer.EgressID] = blockedMeridianPeer(peer, state.Applied.Revision, "monitor_starting")
	}
	e.Store.landingStatusMu.Unlock()
	e.Store.landingCancel, e.Store.landingDone = cancel, make(chan struct{})
	done := e.Store.landingDone
	go func() {
		defer close(done)
		var monitors sync.WaitGroup
		for _, peer := range state.AppliedPeers {
			monitors.Add(1)
			go func() {
				defer monitors.Done()
				gate, err := landing.NewBridgeGate(peer.Identity, state.Bridge, state.Applied.Revision)
				if err != nil {
					return
				}
				checker := landing.NewLinkChecker()
				monitor := landing.Monitor{Gate: gate, Links: checker, TCPOnly: true,
					CheckBusiness: func(checkContext context.Context, identity landing.PeerIdentity, revision uint64) (landing.BusinessResult, error) {
						if state.AppliedSource == nil {
							return landing.BusinessResult{}, errors.New("agent: Meridian entry identity is missing")
						}
						actual, err := checker.SelfIdentity(checkContext, state.AppliedSource.Address)
						if err != nil || actual != *state.AppliedSource {
							return landing.BusinessResult{}, errors.New("agent: Meridian entry private identity changed")
						}
						if _, err := e.observeAppliedMeridianRuntime(checkContext, state); err != nil {
							return landing.BusinessResult{}, err
						}
						result, err := (landing.Probe{TCPOnly: true}).Check(checkContext, identity, revision)
						if err != nil {
							return result, err
						}
						actual, err = checker.SelfIdentity(checkContext, state.AppliedSource.Address)
						if err != nil || actual != *state.AppliedSource {
							return landing.BusinessResult{}, errors.New("agent: Meridian entry private identity changed during probe")
						}
						return result, nil
					},
					Report: func(status landing.MonitorStatus) {
						e.Store.recordMeridianPeerObservation(state, meridianruntime.PeerObservation{EgressID: peer.EgressID, Identity: peer.Identity, Status: status})
					},
				}
				_ = monitor.Run(monitorContext)
				e.Store.recordMeridianPeerObservation(state, blockedMeridianPeer(peer, state.Applied.Revision, "monitor_stopped"))
			}()
		}
		monitors.Wait()
	}()
	return nil
}

// Caller holds landingMutationMu; status fields additionally serve heartbeat readers.
func (s *Store) meridianLandingMonitorRunning(state meridianRuntimeState) bool {
	if state.Applied == nil || s.landingDone == nil {
		return false
	}
	select {
	case <-s.landingDone:
		return len(state.AppliedPeers) == 0
	default:
	}
	s.landingStatusMu.RLock()
	defer s.landingStatusMu.RUnlock()
	return s.meridianMonitorRevision == state.Applied.Revision && s.meridianMonitorSHA256 == state.Applied.ConfigSHA256 && sameMeridianSource(s.meridianMonitorSource, state.AppliedSource)
}

func blockedMeridianPeer(peer meridianruntime.Peer, revision uint64, reason string) meridianruntime.PeerObservation {
	return meridianruntime.PeerObservation{EgressID: peer.EgressID, Identity: peer.Identity, Status: landing.MonitorStatus{Revision: revision, State: "blocked", LinkState: "unknown", Reason: reason, CheckedAt: time.Now().UTC()}}
}

func (s *Store) recordMeridianPeerObservation(state meridianRuntimeState, observation meridianruntime.PeerObservation) {
	s.landingStatusMu.Lock()
	defer s.landingStatusMu.Unlock()
	if state.Applied != nil && s.meridianMonitorRevision == state.Applied.Revision && s.meridianMonitorSHA256 == state.Applied.ConfigSHA256 && sameMeridianSource(s.meridianMonitorSource, state.AppliedSource) {
		s.meridianPeerStatuses[observation.EgressID] = observation
	}
}

func (s *Store) meridianPeerObservations(state meridianRuntimeState) []meridianruntime.PeerObservation {
	observations := make([]meridianruntime.PeerObservation, 0, len(state.AppliedPeers))
	s.landingStatusMu.RLock()
	defer s.landingStatusMu.RUnlock()
	for _, peer := range state.AppliedPeers {
		observation := blockedMeridianPeer(peer, state.Applied.Revision, "no_current_peer_evidence")
		if !state.HandoverPending && s.meridianMonitorRevision == state.Applied.Revision && s.meridianMonitorSHA256 == state.Applied.ConfigSHA256 && sameMeridianSource(s.meridianMonitorSource, state.AppliedSource) {
			if current, exists := s.meridianPeerStatuses[peer.EgressID]; exists && current.Identity == peer.Identity {
				observation = current
			}
		}
		observations = append(observations, observation)
	}
	return observations
}

// Retirement, unlike an ordinary projection, consumes old recovery evidence.
// Rebuilding aliases starts new monitors, so require their actual fresh probes
// before crossing that final boundary. Waiting never restarts a writer/runtime.
func waitForMeridianRetirementPeers(ctx context.Context, task meridianruntime.Task, observe func(context.Context) (meridianruntime.Result, error)) error {
	if len(task.Peers) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		result, err := observe(ctx)
		if err != nil {
			return err
		}
		health, err := result.PeerHealth(task, time.Now().UTC())
		if err != nil {
			return err
		}
		ready := true
		for _, healthy := range health {
			ready = ready && healthy
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("agent: Meridian landing peers are not ready for legacy retirement; retained recovery evidence")
		case <-ticker.C:
		}
	}
}

func (s *Store) verifyMeridianLegacyRetirement(ctx context.Context, applicationID string) error {
	state, err := s.loadMeridianRuntimeState(ctx)
	if errors.Is(err, errApplicationNotInstalled) {
		return s.verifyMeridianLegacyLanding(ctx, nil)
	}
	if err != nil {
		return err
	}
	if state.ApplicationID != applicationID || state.Applied == nil || state.Pending != nil || state.HandoverPending || len(state.RetiringGates) != 0 {
		return errors.New("agent: Meridian landing ownership is not ready for retirement")
	}
	if err := s.verifyMeridianLegacyLanding(ctx, state.LegacyLandingSealed); err != nil {
		return err
	}
	return s.verifyMeridianLegacyChildCoverage(ctx, *state.Applied)
}

// Retirement atomically consumes the exact retained source journal. A changed
// source stops cleanup rather than deleting another writer's recovery evidence.
func (s *Store) retireMeridianLegacyLanding(ctx context.Context, applicationID string) error {
	state, err := s.loadMeridianRuntimeState(ctx)
	if err != nil {
		if errors.Is(err, errApplicationNotInstalled) {
			return s.verifyMeridianLegacyLanding(ctx, nil)
		}
		return err
	}
	if state.ApplicationID != applicationID || state.Applied == nil || state.Pending != nil || state.HandoverPending || len(state.RetiringGates) != 0 {
		return errors.New("agent: Meridian landing ownership is not ready for retirement")
	}
	if err := s.verifyMeridianLegacyLanding(ctx, state.LegacyLandingSealed); err != nil {
		return err
	}
	if len(state.LegacyLandingSealed) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previousMeridian []byte
	if err := tx.QueryRowContext(ctx, `SELECT sealed_state FROM meridian_runtime_state WHERE id=1`).Scan(&previousMeridian); err != nil {
		return err
	}
	previousRaw, err := secret.Open(s.key, previousMeridian, []byte(meridianRuntimeStateAAD))
	expectedRaw, encodeErr := json.Marshal(state)
	if err != nil || encodeErr != nil || !bytes.Equal(previousRaw, expectedRaw) {
		return errors.New("agent: Meridian authority changed before landing retirement")
	}
	deleted, err := tx.ExecContext(ctx, `DELETE FROM landing_runtime_state WHERE id=1 AND sealed_state=?`, state.LegacyLandingSealed)
	if err != nil {
		return err
	}
	if count, err := deleted.RowsAffected(); err != nil || count != 1 {
		return errors.New("agent: legacy landing changed before retirement")
	}
	state.LegacyLandingSealed = nil
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	sealed, err := secret.Seal(s.key, raw, []byte(meridianRuntimeStateAAD))
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE meridian_runtime_state SET sealed_state=? WHERE id=1 AND sealed_state=?`, sealed, previousMeridian)
	if err != nil {
		return fmt.Errorf("agent: complete Meridian landing retirement: %w", err)
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		return errors.New("agent: Meridian authority changed during landing retirement")
	}
	return tx.Commit()
}
