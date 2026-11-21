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

	"github.com/moby/moby/client"
	"github.com/petauron/glivela"
	"github.com/petauron/vastora/internal/dockerruntime"
	"github.com/petauron/vastora/internal/glivelaruntime"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/secret"
)

// A gate identity is retained until its closed kernel table has been removed.
// It is not a second routing authority: only the Glivela artifact owns routes.
type glivelaGateIdentity struct {
	Peer     landing.PeerIdentity `json:"peer"`
	Bridge   string               `json:"bridge,omitempty"`
	GID      uint32               `json:"gid,omitempty"`
	Revision uint64               `json:"revision"`
}

type glivelaTrafficGate interface {
	Install(context.Context) error
	Remove(context.Context) error
}

func newGlivelaTrafficGate(identity glivelaGateIdentity) (*landing.TrafficGate, error) {
	if identity.GID != 0 {
		if identity.Bridge != "" {
			return nil, errors.New("agent: ambiguous Glivela gate scope")
		}
		return landing.NewHostGate(identity.Peer, identity.GID, identity.Revision)
	}
	return landing.NewBridgeGate(identity.Peer, identity.Bridge, identity.Revision)
}

func glivelaTrafficGateFactory(identity glivelaGateIdentity) (glivelaTrafficGate, error) {
	return newGlivelaTrafficGate(identity)
}

func (state glivelaRuntimeState) validateLanding() error {
	for _, plan := range []struct {
		artifact     *glivela.DesiredArtifact
		peers        []glivelaruntime.Peer
		source       *landing.PeerIdentity
		gateRevision uint64
		gid          uint32
	}{{state.Applied, state.AppliedPeers, state.AppliedSource, state.appliedGateRevision(), state.AppliedGID}, {state.Pending, state.PendingPeers, state.PendingSource, 0, state.PendingGID}} {
		if plan.artifact == nil {
			if len(plan.peers) != 0 || plan.source != nil || plan.gid != 0 {
				return errors.New("agent: Glivela peers have no owning artifact")
			}
			continue
		}
		task := glivelaruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: *plan.artifact, Peers: plan.peers, Source: plan.source}
		if task.Validate() != nil {
			return errors.New("agent: Glivela peer plan does not match its artifact")
		}
		gateRevision := plan.artifact.Revision
		if plan.gateRevision != 0 {
			gateRevision = plan.gateRevision
		}
		for _, identity := range glivelaPlanGates(plan.peers, state.Bridge, plan.gid, gateRevision) {
			if _, err := newGlivelaTrafficGate(identity); err != nil {
				return err
			}
		}
	}
	for _, identity := range state.RetiringGates {
		if _, err := newGlivelaTrafficGate(identity); err != nil {
			return err
		}
	}
	return nil
}

func glivelaPlanGates(peers []glivelaruntime.Peer, bridge string, gid uint32, revision uint64) []glivelaGateIdentity {
	if gid != 0 {
		bridge = ""
	}
	gates := make([]glivelaGateIdentity, 0, len(peers))
	for _, peer := range peers {
		gates = append(gates, glivelaGateIdentity{Peer: peer.Identity, Bridge: bridge, GID: gid, Revision: revision})
	}
	return gates
}

func sameGlivelaPeers(left, right []glivelaruntime.Peer) bool {
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

func cloneGlivelaSource(source *landing.PeerIdentity) *landing.PeerIdentity {
	if source == nil {
		return nil
	}
	value := *source
	return &value
}

func sameGlivelaSource(left, right *landing.PeerIdentity) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func appendGlivelaGates(destination []glivelaGateIdentity, additions ...glivelaGateIdentity) []glivelaGateIdentity {
	for _, identity := range additions {
		if !slices.Contains(destination, identity) {
			destination = append(destination, identity)
		}
	}
	return destination
}

func (state glivelaRuntimeState) knownLandingGates() []glivelaGateIdentity {
	gates := appendGlivelaGates(nil, state.RetiringGates...)
	if state.Applied != nil {
		gates = appendGlivelaGates(gates, glivelaPlanGates(state.AppliedPeers, state.Bridge, state.AppliedGID, state.appliedGateRevision())...)
	}
	if state.Pending != nil {
		gates = appendGlivelaGates(gates, glivelaPlanGates(state.PendingPeers, state.Bridge, state.PendingGID, state.Pending.Revision)...)
	}
	return gates
}

// An unchanged Xray configuration keeps its existing nft gate and lease when
// Center advances only the desired revision. Zero is the original behavior
// for states created before this field existed and after a real replacement.
func (state glivelaRuntimeState) appliedGateRevision() uint64 {
	if state.GateRevision != 0 {
		return state.GateRevision
	}
	if state.Applied != nil {
		return state.Applied.Revision
	}
	return 0
}

func closeGlivelaGates(ctx context.Context, gates []glivelaGateIdentity, factory func(glivelaGateIdentity) (glivelaTrafficGate, error)) error {
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
func removeSupersededGlivelaGates(ctx context.Context, previous, current []glivelaGateIdentity, factory func(glivelaGateIdentity) (glivelaTrafficGate, error)) error {
	if err := closeGlivelaGates(ctx, appendGlivelaGates(slices.Clone(previous), current...), factory); err != nil {
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

func glivelaRuntimeBridge(ctx context.Context, docker *client.Client) (string, string, error) {
	result, err := docker.NetworkInspect(ctx, dockerruntime.NetworkName, client.NetworkInspectOptions{})
	if err != nil {
		return "", "", errors.New("agent: Glivela bridge is unavailable")
	}
	network := result.Network
	if network.ID == "" || network.Driver != "bridge" || network.Name != dockerruntime.NetworkName || network.Labels[dockerruntime.ManagedLabel] != "true" || network.Labels[dockerruntime.ComponentLabel] != "runtime-network" {
		return "", "", errors.New("agent: Glivela bridge ownership changed")
	}
	bridge := network.Options["com.docker.network.bridge.name"]
	if bridge == "" {
		if len(network.ID) < 12 {
			return "", "", errors.New("agent: Glivela bridge identity is invalid")
		}
		bridge = "br-" + network.ID[:12]
	}
	return bridge, network.ID, nil
}

// Read before the one-way boundary. The encrypted source remains available for
// compare-and-delete at explicit retirement, not as a runnable fallback.
func (s *Store) captureGlivelaLegacyLanding(ctx context.Context, applicationID string) ([]byte, []glivelaGateIdentity, error) {
	state, err := s.landingRuntime(ctx)
	if err != nil || state == nil {
		return nil, nil, err
	}
	if state.Route != nil && (state.ApplicationID != applicationID || state.Phase != "applied" || state.Applied == nil || state.Applied.Revision != state.Desired.Revision || state.Retiring != nil) {
		return nil, nil, errors.New("agent: legacy landing requires explicit reconciliation before Glivela handover")
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
	gates := []glivelaGateIdentity{}
	if state.Route != nil {
		for _, use := range state.Desired.PeerUses() {
			gates = appendGlivelaGates(gates, glivelaGateIdentity{Peer: use.Peer, Bridge: state.Bridge, Revision: state.Desired.Revision})
		}
	}
	return sealed, gates, nil
}

// The first Glivela import has no landing peers or migrated child credentials.
// Its complete native-only artifact intentionally replaces the legacy route,
// so route parity is not a prerequisite for that one-way handover. Peer-bearing
// revisions still require the exact applied route and landing plan.
func (s *Store) verifyGlivelaLegacyRouteHandover(ctx context.Context, task glivelaruntime.Task, legacy *landingRuntimeState) error {
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
		return errors.New("agent: legacy landing route changed before Glivela handover")
	}
	return s.verifyLocalLandingPlan(ctx, routes, legacy.Desired)
}

func (s *Store) verifyGlivelaLegacyLanding(ctx context.Context, expected []byte) error {
	var actual []byte
	err := s.db.QueryRowContext(ctx, `SELECT sealed_state FROM landing_runtime_state WHERE id=1`).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) && len(expected) == 0 {
		return nil
	}
	if err != nil || !bytes.Equal(actual, expected) {
		return errors.New("agent: legacy landing journal changed during Glivela handover")
	}
	return nil
}

func (s *Store) requireLegacyLandingAuthority(ctx context.Context) error {
	_, err := s.loadGlivelaRuntimeState(ctx)
	if errors.Is(err, errApplicationNotInstalled) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("agent: Glivela owns the proxy runtime; legacy landing mutations are retired")
}

// Called by the replacement's beforeStop hook under landingMutationMu. All
// expensive preparation has already succeeded; from this journal onward no
// restart may restore the legacy writer or its monitor.
func (e ApplicationExecutor) beginGlivelaLandingHandover(ctx context.Context, docker *client.Client, state *glivelaRuntimeState, task glivelaruntime.Task, gid uint32) error {
	if task.Source != nil {
		checker := landing.NewLinkChecker()
		actual, err := checker.SelfIdentity(ctx, task.Source.Address)
		checker.Close()
		if err != nil || actual != *task.Source {
			return errors.New("agent: Glivela entry private identity changed before handover")
		}
	}
	// Only retained bridge leases need the old Docker network during migration.
	// A host runtime must not depend on a bridge existing on its next replay.
	bridge := state.Bridge
	if state.Applied != nil && state.AppliedGID == 0 && len(state.AppliedPeers) != 0 {
		actual, _, err := glivelaRuntimeBridge(ctx, docker)
		if err != nil || bridge != actual {
			return errors.New("agent: Glivela applied bridge changed before handover")
		}
	}
	retiring := state.knownLandingGates()
	if state.Applied == nil && state.LegacyRuntimeSHA256 == "" {
		current, _, exists, err := inspectCurrentGlivelaRuntime(ctx, docker)
		if err != nil {
			return err
		}
		if exists {
			if current.Container.Config == nil || current.Container.Config.Labels[xrayWorkerRuntimeLabel] != "xray" || current.Container.Config.Labels[applicationIdentityLabel] != threeXUIKey || current.Container.Config.Labels[applicationInstallationLabel] != task.ApplicationID {
				return errors.New("agent: legacy proxy must be an owned managed Xray worker before Glivela handover")
			}
			state.LegacyRuntimeSHA256, err = e.Store.glivelaLegacyWorkerDigest(ctx, task.ApplicationID)
			if err != nil {
				return err
			}
		}
	}
	if state.Applied == nil && len(state.LegacyLandingSealed) == 0 {
		sealed, gates, err := e.Store.captureGlivelaLegacyLanding(ctx, task.ApplicationID)
		if err != nil {
			return err
		}
		state.LegacyLandingSealed = sealed
		retiring = appendGlivelaGates(retiring, gates...)
		if len(gates) != 0 {
			actual, _, err := glivelaRuntimeBridge(ctx, docker)
			if err != nil {
				return err
			}
			bridge = actual
			legacy, err := e.Store.landingRuntime(ctx)
			if err != nil || legacy == nil || legacy.Bridge != bridge {
				return errors.New("agent: legacy landing bridge changed before Glivela handover")
			}
			if legacy.Desired.Clients != nil && len(task.Peers) != 0 && (task.Source == nil || legacy.Desired.Clients.Source != *task.Source) {
				return errors.New("agent: Glivela source identity differs from the authorized legacy entry")
			}
			if err := e.Store.verifyGlivelaLegacyRouteHandover(ctx, task, legacy); err != nil {
				return err
			}
			current, _, exists, err := inspectCurrentGlivelaRuntime(ctx, docker)
			if err != nil || !exists || current.Container.ID != legacy.ContainerID || current.Container.HostConfig == nil || current.Container.HostConfig.RestartPolicy.Name != "no" {
				return errors.New("agent: legacy landing container changed before Glivela handover")
			}
		}
	}
	if err := e.Store.verifyGlivelaLegacyLanding(ctx, state.LegacyLandingSealed); err != nil {
		return err
	}
	state.RetiringGates = retiring
	state.Bridge = bridge
	state.Pending, state.PendingPeers = &task.Desired, slices.Clone(task.Peers)
	state.PendingSource = cloneGlivelaSource(task.Source)
	state.PendingGID = gid
	state.HandoverPending = true
	if err := e.Store.saveGlivelaRuntimeState(ctx, *state); err != nil {
		return err
	}
	if err := e.Store.stopLandingMonitor(ctx); err != nil {
		return uncertainTaskOutcome(err)
	}
	if err := closeGlivelaGates(ctx, state.knownLandingGates(), glivelaTrafficGateFactory); err != nil {
		return uncertainTaskOutcome(err)
	}
	if err := e.Store.stopXrayWorkerAPI(ctx, false); err != nil {
		return uncertainTaskOutcome(err)
	}
	if state.Applied == nil && state.LegacyRuntimeSHA256 != "" {
		actual, err := e.Store.glivelaLegacyWorkerDigest(ctx, task.ApplicationID)
		if err != nil || actual != state.LegacyRuntimeSHA256 {
			return uncertainTaskOutcome(errors.Join(errors.New("agent: legacy runtime changed while draining its writer"), err))
		}
	}
	return nil
}

// The API and its reconciler use the same mutex. Compare rendered authority,
// not sealed accounting counters, before and after draining those writers.
func (s *Store) glivelaLegacyWorkerDigest(ctx context.Context, applicationID string) (string, error) {
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

func (e ApplicationExecutor) finishGlivelaLandingHandover(ctx context.Context, state *glivelaRuntimeState) error {
	if state.Applied == nil || state.Pending != nil {
		return errors.New("agent: Glivela landing handover has no applied authority")
	}
	if _, err := e.observeAppliedGlivelaRuntime(ctx, *state); err != nil {
		return err
	}
	current := glivelaPlanGates(state.AppliedPeers, state.Bridge, state.AppliedGID, state.Applied.Revision)
	previous := appendGlivelaGates(slices.Clone(state.RetiringGates), glivelaPlanGates(state.AppliedPeers, state.Bridge, state.AppliedGID, state.appliedGateRevision())...)
	if err := removeSupersededGlivelaGates(ctx, previous, current, glivelaTrafficGateFactory); err != nil {
		return err
	}
	state.GateRevision = 0
	if err := removeClosedGlivelaGateConflicts(ctx, *state); err != nil {
		return err
	}
	state.RetiringGates, state.HandoverPending = nil, false
	if err := e.Store.saveGlivelaRuntimeState(ctx, *state); err != nil {
		return err
	}
	return e.startGlivelaLandingMonitor(ctx, *state)
}

func removeClosedGlivelaGateConflicts(ctx context.Context, state glivelaRuntimeState) error {
	if state.Applied == nil || state.Pending != nil {
		return errors.New("agent: Glivela gate cleanup requires an applied runtime")
	}
	for _, peer := range state.AppliedPeers {
		gate, err := newGlivelaTrafficGate(glivelaPlanGates([]glivelaruntime.Peer{peer}, state.Bridge, state.AppliedGID, state.appliedGateRevision())[0])
		if err != nil {
			return err
		}
		if err := gate.RemoveClosedConflicts(ctx); err != nil {
			return err
		}
	}
	return nil
}

// A peer-bearing runtime cannot auto-start after reboot before nft gates exist.
// Only an exact, journaled Glivela container may be started behind closed gates.
func (e ApplicationExecutor) prepareGlivelaRuntimeStart(ctx context.Context, docker *client.Client, state glivelaRuntimeState, artifact glivela.DesiredArtifact, peers []glivelaruntime.Peer) error {
	if err := closeGlivelaGates(ctx, state.knownLandingGates(), glivelaTrafficGateFactory); err != nil {
		return err
	}
	current, exists, err := inspectXrayWorkerContainer(ctx, docker, legacyXrayRuntimeContainer)
	if err != nil || !exists || current.Container.Config == nil || current.Container.HostConfig == nil || current.Container.State == nil || current.Container.Config.Labels[applicationIdentityLabel] != legacyProxyAppKey || current.Container.Config.Labels[applicationInstallationLabel] != state.ApplicationID || current.Container.Config.Image != state.ImageReference {
		return errors.New("agent: Glivela boot runtime identity is unavailable")
	}
	active, err := os.ReadFile(filepath.Join(e.Store.glivelaConfigDir, "config.json"))
	if err != nil || !artifactMatchesBytes(artifact, active) {
		return errors.New("agent: Glivela boot configuration does not match its journal")
	}
	if err := e.verifyGlivelaRuntimeAttachment(ctx, docker, current, state, peers); err != nil {
		return err
	}
	if !current.Container.State.Running {
		if current.Container.HostConfig.RestartPolicy.Name != "no" {
			return errors.New("agent: stopped Glivela runtime requires explicit recovery")
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
func (e ApplicationExecutor) verifyGlivelaRuntimeAttachment(ctx context.Context, docker *client.Client, inspected client.ContainerInspectResult, state glivelaRuntimeState, peers []glivelaruntime.Peer) error {
	if inspected.Container.Config == nil || !slices.Equal([]string(inspected.Container.Config.Cmd), []string{"run", "-c", xrayWorkerConfigPath}) {
		return errors.New("agent: Glivela configuration command changed")
	}
	matches := 0
	for _, mounted := range inspected.Container.Mounts {
		if strings.HasPrefix(mounted.Destination, filepath.Dir(xrayWorkerConfigPath)+"/") {
			return errors.New("agent: Glivela configuration mount is shadowed")
		}
		if mounted.Destination != filepath.Dir(xrayWorkerConfigPath) {
			continue
		}
		if string(mounted.Type) != "bind" || mounted.Source != e.Store.glivelaConfigDir || mounted.RW {
			return errors.New("agent: Glivela configuration mount does not match its journal")
		}
		matches++
	}
	if matches != 1 {
		return errors.New("agent: Glivela configuration mount is unavailable or ambiguous")
	}
	gid, artifact := state.AppliedGID, state.Applied
	if state.Pending != nil {
		gid, artifact = state.PendingGID, state.Pending
	}
	if gid != 0 {
		return verifyGlivelaHostContainer(inspected, gid, artifact)
	}
	if len(peers) == 0 {
		return nil
	}
	bridge, networkID, err := glivelaRuntimeBridge(ctx, docker)
	if err != nil || bridge != state.Bridge || inspected.Container.HostConfig == nil || string(inspected.Container.HostConfig.NetworkMode) != dockerruntime.NetworkName || inspected.Container.HostConfig.RestartPolicy.Name != "no" || inspected.Container.NetworkSettings == nil {
		return errors.New("agent: Glivela landing fence does not match its journal")
	}
	attachment := inspected.Container.NetworkSettings.Networks[dockerruntime.NetworkName]
	if attachment == nil || attachment.NetworkID != networkID {
		return errors.New("agent: Glivela landing network identity changed")
	}
	return nil
}

func (e ApplicationExecutor) startGlivelaLandingMonitor(ctx context.Context, state glivelaRuntimeState) error {
	if state.Applied == nil || state.Pending != nil || state.HandoverPending || state.validate() != nil {
		return errors.New("agent: Glivela monitor requires a completed applied handover")
	}
	if err := e.Store.stopLandingMonitor(ctx); err != nil {
		return err
	}
	monitorContext, cancel := context.WithCancel(context.Background())
	e.Store.landingStatusMu.Lock()
	e.Store.glivelaMonitorRevision, e.Store.glivelaMonitorSHA256 = state.appliedGateRevision(), state.Applied.ConfigSHA256
	e.Store.glivelaMonitorSource = cloneGlivelaSource(state.AppliedSource)
	e.Store.glivelaPeerStatuses = map[string]glivelaruntime.PeerObservation{}
	e.Store.landingPeerStatuses = map[string]landingPeerStatus{}
	e.Store.landingStatus = landing.MonitorStatus{}
	for _, peer := range state.AppliedPeers {
		e.Store.glivelaPeerStatuses[peer.EgressID] = blockedGlivelaPeer(peer, state.Applied.Revision, "monitor_starting")
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
				gate, err := newGlivelaTrafficGate(glivelaPlanGates([]glivelaruntime.Peer{peer}, state.Bridge, state.AppliedGID, state.appliedGateRevision())[0])
				if err != nil {
					return
				}
				checker := landing.NewLinkChecker()
				monitor := landing.Monitor{Gate: gate, Links: checker, TCPOnly: true,
					CheckBusiness: func(checkContext context.Context, identity landing.PeerIdentity, revision uint64) (landing.BusinessResult, error) {
						if state.AppliedSource == nil {
							return landing.BusinessResult{}, errors.New("agent: Glivela entry identity is missing")
						}
						actual, err := checker.SelfIdentity(checkContext, state.AppliedSource.Address)
						if err != nil || actual != *state.AppliedSource {
							return landing.BusinessResult{}, errors.New("agent: Glivela entry private identity changed")
						}
						if _, err := e.observeAppliedGlivelaRuntime(checkContext, state); err != nil {
							return landing.BusinessResult{}, err
						}
						result, err := (landing.Probe{TCPOnly: true}).Check(checkContext, identity, revision)
						if err != nil {
							return result, err
						}
						actual, err = checker.SelfIdentity(checkContext, state.AppliedSource.Address)
						if err != nil || actual != *state.AppliedSource {
							return landing.BusinessResult{}, errors.New("agent: Glivela entry private identity changed during probe")
						}
						return result, nil
					},
					Report: func(status landing.MonitorStatus) {
						e.Store.recordGlivelaPeerObservation(state, glivelaruntime.PeerObservation{EgressID: peer.EgressID, Identity: peer.Identity, Status: status})
					},
				}
				if err := monitor.Run(monitorContext); err != nil {
					e.Store.recordGlivelaPeerObservation(state, blockedGlivelaPeer(peer, state.Applied.Revision, "startup_failed"))
				}
				<-monitorContext.Done()
			}()
		}
		monitors.Wait()
	}()
	return nil
}

// Caller holds landingMutationMu; status fields additionally serve heartbeat readers.
func (s *Store) glivelaLandingMonitorRunning(state glivelaRuntimeState) bool {
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
	return s.glivelaMonitorRevision == state.appliedGateRevision() && s.glivelaMonitorSHA256 == state.Applied.ConfigSHA256 && sameGlivelaSource(s.glivelaMonitorSource, state.AppliedSource)
}

func blockedGlivelaPeer(peer glivelaruntime.Peer, revision uint64, reason string) glivelaruntime.PeerObservation {
	return glivelaruntime.PeerObservation{EgressID: peer.EgressID, Identity: peer.Identity, Status: landing.MonitorStatus{Revision: revision, State: "blocked", LinkState: "unknown", Reason: reason, CheckedAt: time.Now().UTC()}}
}

func (s *Store) recordGlivelaPeerObservation(state glivelaRuntimeState, observation glivelaruntime.PeerObservation) {
	s.landingStatusMu.Lock()
	defer s.landingStatusMu.Unlock()
	if state.Applied != nil && s.glivelaMonitorRevision == state.appliedGateRevision() && s.glivelaMonitorSHA256 == state.Applied.ConfigSHA256 && sameGlivelaSource(s.glivelaMonitorSource, state.AppliedSource) {
		s.glivelaPeerStatuses[observation.EgressID] = observation
	}
}

func (s *Store) glivelaPeerObservations(state glivelaRuntimeState) []glivelaruntime.PeerObservation {
	observations := make([]glivelaruntime.PeerObservation, 0, len(state.AppliedPeers))
	s.landingStatusMu.RLock()
	defer s.landingStatusMu.RUnlock()
	for _, peer := range state.AppliedPeers {
		observation := blockedGlivelaPeer(peer, state.Applied.Revision, "no_current_peer_evidence")
		if !state.HandoverPending && s.glivelaMonitorRevision == state.appliedGateRevision() && s.glivelaMonitorSHA256 == state.Applied.ConfigSHA256 && sameGlivelaSource(s.glivelaMonitorSource, state.AppliedSource) {
			if current, exists := s.glivelaPeerStatuses[peer.EgressID]; exists && current.Identity == peer.Identity {
				observation = current
				observation.Status.Revision = state.Applied.Revision
			}
		}
		observations = append(observations, observation)
	}
	return observations
}

// Retirement, unlike an ordinary projection, consumes old recovery evidence.
// Rebuilding aliases starts new monitors, so require their actual fresh probes
// before crossing that final boundary. Waiting never restarts a writer/runtime.
func waitForGlivelaRetirementPeers(ctx context.Context, task glivelaruntime.Task, observe func(context.Context) (glivelaruntime.Result, error)) error {
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
			return errors.New("agent: Glivela landing peers are not ready for legacy retirement; retained recovery evidence")
		case <-ticker.C:
		}
	}
}

func (s *Store) verifyGlivelaLegacyRetirement(ctx context.Context, applicationID string) error {
	state, err := s.loadGlivelaRuntimeState(ctx)
	if errors.Is(err, errApplicationNotInstalled) {
		return s.verifyGlivelaLegacyLanding(ctx, nil)
	}
	if err != nil {
		return err
	}
	if state.ApplicationID != applicationID || state.Applied == nil || state.Pending != nil || state.HandoverPending || len(state.RetiringGates) != 0 {
		return errors.New("agent: Glivela landing ownership is not ready for retirement")
	}
	if err := s.verifyGlivelaLegacyLanding(ctx, state.LegacyLandingSealed); err != nil {
		return err
	}
	return s.verifyGlivelaLegacyChildCoverage(ctx, *state.Applied)
}

// Retirement atomically consumes the exact retained source journal. A changed
// source stops cleanup rather than deleting another writer's recovery evidence.
func (s *Store) retireGlivelaLegacyLanding(ctx context.Context, applicationID string) error {
	state, err := s.loadGlivelaRuntimeState(ctx)
	if err != nil {
		if errors.Is(err, errApplicationNotInstalled) {
			return s.verifyGlivelaLegacyLanding(ctx, nil)
		}
		return err
	}
	if state.ApplicationID != applicationID || state.Applied == nil || state.Pending != nil || state.HandoverPending || len(state.RetiringGates) != 0 {
		return errors.New("agent: Glivela landing ownership is not ready for retirement")
	}
	if err := s.verifyGlivelaLegacyLanding(ctx, state.LegacyLandingSealed); err != nil {
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
	var previousRuntimeState []byte
	if err := tx.QueryRowContext(ctx, `SELECT sealed_state FROM glivela_runtime_state WHERE id=1`).Scan(&previousRuntimeState); err != nil {
		return err
	}
	previousRaw, err := secret.Open(s.key, previousRuntimeState, []byte(glivelaRuntimeStateAAD))
	expectedRaw, encodeErr := json.Marshal(state)
	if err != nil || encodeErr != nil || !bytes.Equal(previousRaw, expectedRaw) {
		return errors.New("agent: Glivela authority changed before landing retirement")
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
	sealed, err := secret.Seal(s.key, raw, []byte(glivelaRuntimeStateAAD))
	if err != nil {
		return err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE glivela_runtime_state SET sealed_state=? WHERE id=1 AND sealed_state=?`, sealed, previousRuntimeState)
	if err != nil {
		return fmt.Errorf("agent: complete Glivela landing retirement: %w", err)
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		return errors.New("agent: Glivela authority changed during landing retirement")
	}
	return tx.Commit()
}
