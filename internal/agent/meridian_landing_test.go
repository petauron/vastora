package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func meridianLandingFixture() meridianRuntimeState {
	artifact := meridianRecoveryArtifact(7, `{"outbounds":[{"protocol":"freedom","tag":"direct"},{"protocol":"socks","tag":"egress","settings":{"servers":[{"address":"100.64.0.8","port":1080}]}}]}`)
	return meridianRuntimeState{
		ApplicationID: "meridian-application", ImageReference: xrayWorkerImageReference,
		Applied: &artifact, Bridge: "br-0123456789ab",
		AppliedSource: &landing.PeerIdentity{ID: "entry", PublicKey: "nodekey:entry", Address: "100.64.0.1"},
		AppliedPeers:  []meridianruntime.Peer{{EgressID: "egress", Identity: landing.PeerIdentity{ID: "tailnet-egress", PublicKey: "nodekey:egress", Address: "100.64.0.8"}}},
	}
}

func openMeridianLandingTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedMeridianLegacyLanding(t *testing.T, store *Store, phase string) landingRuntimeState {
	t.Helper()
	ctx := context.Background()
	if err := store.SaveConnection(ctx, testConnection(t, "node-1", "node", "https://center.example.com", "credential")); err != nil {
		t.Fatal(err)
	}
	peer := meridianLandingFixture().AppliedPeers[0].Identity
	route, err := landing.PrepareRouteChange(json.RawMessage(`{"outbounds":[{"tag":"direct","protocol":"freedom"}],"routing":{"rules":[]}}`), 1, []string{"business"}, peer)
	if err != nil {
		t.Fatal(err)
	}
	state := landingRuntimeState{
		Desired:       landing.DesiredState{NodeID: "node-1", Revision: 1, Proxy: &landing.ProxyPlan{ApplicationID: "meridian-application", InboundTags: []string{"business"}, Peer: peer}},
		ApplicationID: "meridian-application", ContainerID: "legacy-container", Bridge: "br-0123456789ab", RestartPolicy: "no", Route: &route, Phase: phase,
	}
	if phase == "applied" {
		applied := state.Desired
		state.Applied = &applied
	}
	if err := store.saveLandingRuntime(ctx, state); err != nil {
		t.Fatal(err)
	}
	return state
}

type recordingMeridianGate struct {
	identity meridianGateIdentity
	events   *[]string
	fail     bool
}

func (g recordingMeridianGate) Install(context.Context) error {
	*g.events = append(*g.events, fmt.Sprintf("close:%d", g.identity.Revision))
	if g.fail {
		return errors.New("gate unavailable")
	}
	return nil
}

func (g recordingMeridianGate) Remove(context.Context) error {
	*g.events = append(*g.events, fmt.Sprintf("remove:%d", g.identity.Revision))
	return nil
}

func TestMeridianGateAdoptionClosesAllBeforeRemovingSupersededTables(t *testing.T) {
	state := meridianLandingFixture()
	current := state.knownLandingGates()[0]
	previous := current
	previous.Revision--
	for _, test := range []struct {
		name string
		old  meridianGateIdentity
		want []string
	}{
		{"shared identity adopted", current, []string{"close:7"}},
		{"new revision replaces closed gate", previous, []string{"close:6", "close:7", "remove:6"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var events []string
			factory := func(identity meridianGateIdentity) (meridianTrafficGate, error) {
				return recordingMeridianGate{identity: identity, events: &events}, nil
			}
			if err := removeSupersededMeridianGates(context.Background(), []meridianGateIdentity{test.old}, []meridianGateIdentity{current}, factory); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(events, test.want) {
				t.Fatalf("gate order = %v, want %v", events, test.want)
			}
		})
	}
	var events []string
	factory := func(identity meridianGateIdentity) (meridianTrafficGate, error) {
		return recordingMeridianGate{identity: identity, events: &events, fail: identity == previous}, nil
	}
	if err := removeSupersededMeridianGates(context.Background(), []meridianGateIdentity{previous}, []meridianGateIdentity{current}, factory); err == nil {
		t.Fatal("failed gate closure permitted adoption")
	}
	if !reflect.DeepEqual(events, []string{"close:6", "close:7"}) {
		t.Fatalf("failure must attempt every close and remove nothing: %v", events)
	}
}

func TestMeridianJournalBindsPeersSourceAndBootFenceToArtifact(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	state := meridianLandingFixture()
	if err := store.saveMeridianRuntimeState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.loadMeridianRuntimeState(context.Background())
	if err != nil || !reflect.DeepEqual(state, loaded) {
		t.Fatalf("persisted authority differs: %#v %v", loaded, err)
	}
	for _, mutate := range []func(*meridianRuntimeState){
		func(value *meridianRuntimeState) { value.AppliedSource = nil },
		func(value *meridianRuntimeState) { value.AppliedPeers = nil },
		func(value *meridianRuntimeState) { value.Bridge = "" },
		func(value *meridianRuntimeState) { value.PendingPeers = value.AppliedPeers },
		func(value *meridianRuntimeState) { value.Applied = nil },
	} {
		candidate := meridianLandingFixture()
		mutate(&candidate)
		if candidate.validate() == nil {
			t.Fatal("unbound peer plan accepted")
		}
	}
	options := client.ContainerCreateOptions{HostConfig: &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}}}
	meridianContainerLandingPolicy(&options, nil)
	if options.HostConfig.RestartPolicy.Name != "unless-stopped" {
		t.Fatal("native-only boot policy changed")
	}
	meridianContainerLandingPolicy(&options, state.AppliedPeers)
	if options.HostConfig.RestartPolicy.Name != "no" {
		t.Fatal("peer runtime may start before its boot gate")
	}
}

func TestMeridianNativeRuntimeRequiresExactReadOnlyConfigurationMount(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	executor := ApplicationExecutor{Store: store}
	state := meridianLandingFixture()
	fixture := func() client.ContainerInspectResult {
		return client.ContainerInspectResult{Container: container.InspectResponse{
			Config: &container.Config{Cmd: []string{"run", "-c", xrayWorkerConfigPath}},
			Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: filepath.Join(store.dataDir, meridianRuntimeDirectory), Destination: filepath.Dir(xrayWorkerConfigPath)}},
		}}
	}
	if err := executor.verifyMeridianRuntimeAttachment(context.Background(), nil, fixture(), state, nil); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*client.ContainerInspectResult){
		func(value *client.ContainerInspectResult) { value.Container.Mounts = nil },
		func(value *client.ContainerInspectResult) { value.Container.Mounts[0].Source = "/unrelated/config" },
		func(value *client.ContainerInspectResult) { value.Container.Mounts[0].RW = true },
		func(value *client.ContainerInspectResult) {
			value.Container.Config.Cmd = []string{"run", "-c", "/unrelated.json"}
		},
		func(value *client.ContainerInspectResult) {
			value.Container.Mounts = append(value.Container.Mounts, value.Container.Mounts[0])
		},
		func(value *client.ContainerInspectResult) {
			value.Container.Mounts = append(value.Container.Mounts, container.MountPoint{Destination: xrayWorkerConfigPath})
		},
	} {
		candidate := fixture()
		mutate(&candidate)
		if err := executor.verifyMeridianRuntimeAttachment(context.Background(), nil, candidate, state, nil); err == nil {
			t.Fatal("container with unrelated or ambiguous configuration accepted")
		}
	}
}

func TestMeridianPreparationIsScopedAndDoesNotStopLegacyMonitor(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	seedMeridianLegacyLanding(t, store, "applied")
	monitorContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.landingCancel, store.landingDone = cancel, make(chan struct{})
	defer func() { store.landingCancel, store.landingDone = nil, nil }()
	for _, manifest := range officialContractFixture(t).Apps {
		if manifest.ID != "meridian" {
			continue
		}
		task := DeploymentTask{AppKey: meridianKey, ApplicationID: "meridian-application", Operation: "install", Manifest: manifest}
		if checkpoint, err := store.prepareLandingXrayRuntimeMigration(context.Background(), task); err != nil || checkpoint != nil {
			t.Fatalf("audited preparation changed runtime: %#v %v", checkpoint, err)
		}
		select {
		case <-monitorContext.Done():
			t.Fatal("image preparation stopped legacy monitor")
		default:
		}
		task.ApplicationID = "another-application"
		if _, err := store.prepareLandingXrayRuntimeMigration(context.Background(), task); err == nil {
			t.Fatal("cross-application preparation accepted")
		}
		task.ApplicationID = "meridian-application"
		task.Manifest.HostAccess = !task.Manifest.HostAccess
		if _, err := store.prepareLandingXrayRuntimeMigration(context.Background(), task); err == nil {
			t.Fatal("unofficial preparation bypassed landing guard")
		}
		store.landingCancel, store.landingDone = nil, nil
		return
	}
	t.Fatal("official Meridian manifest missing")
}

func TestMeridianLegacyCaptureRejectsIncompleteAndForeignJournal(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	seedMeridianLegacyLanding(t, store, "prepared")
	if _, _, err := store.captureMeridianLegacyLanding(context.Background(), "meridian-application"); err == nil {
		t.Fatal("unapplied legacy route accepted")
	}
	seedMeridianLegacyLanding(t, store, "applied")
	if _, _, err := store.captureMeridianLegacyLanding(context.Background(), "different-application"); err == nil {
		t.Fatal("foreign legacy route accepted")
	}
	if sealed, gates, err := store.captureMeridianLegacyLanding(context.Background(), "meridian-application"); err != nil || len(sealed) == 0 || len(gates) != 1 {
		t.Fatalf("complete legacy checkpoint rejected: gates=%d err=%v", len(gates), err)
	}
}

func TestMeridianPendingAuthorityNeverResumesLegacyWriterOrMonitor(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	legacy := seedMeridianLegacyLanding(t, store, "applied")
	state := meridianLandingFixture()
	state.Pending, state.Applied = state.Applied, nil
	state.PendingPeers, state.AppliedPeers = state.AppliedPeers, nil
	state.PendingSource, state.AppliedSource = state.AppliedSource, nil
	state.HandoverPending = true
	if err := store.saveMeridianRuntimeState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if err := store.ResumeLandingRuntime(context.Background()); err != nil || store.landingCancel != nil {
		t.Fatalf("legacy monitor resumed across one-way boundary: %v", err)
	}
	if err := store.applyLandingProxy(context.Background(), legacy.Desired); err == nil {
		t.Fatal("legacy landing retry took ownership from pending Meridian")
	}
	if err := store.reconcileXrayWorkerRuntime(context.Background(), nil, nil); err == nil {
		t.Fatal("legacy reconciler accepted Meridian authority")
	}
	response := httptest.NewRecorder()
	store.xrayWorkerHandler(nil, nil).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panel/api/server/status", nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("legacy API remains writable: %d", response.Code)
	}
	if _, err := store.db.Exec(`UPDATE meridian_runtime_state SET sealed_state=? WHERE id=1`, []byte("invalid encrypted authority")); err != nil {
		t.Fatal(err)
	}
	if err := store.ResumeLandingRuntime(context.Background()); err == nil {
		t.Fatal("corrupt Meridian authority fell back to old monitor")
	}
}

func TestMeridianPeerEvidenceCannotCarryAcrossArtifactOrIdentity(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	state := meridianLandingFixture()
	store.meridianMonitorRevision, store.meridianMonitorSHA256 = state.Applied.Revision, state.Applied.ConfigSHA256
	store.meridianMonitorSource = cloneMeridianSource(state.AppliedSource)
	peer := state.AppliedPeers[0]
	observation := blockedMeridianPeer(peer, state.Applied.Revision, "test")
	observation.Status.State = "healthy"
	observation.Status.CheckedAt = time.Now().Add(-time.Minute)
	store.meridianPeerStatuses = map[string]meridianruntime.PeerObservation{peer.EgressID: observation}
	if actual := store.meridianPeerObservations(state); len(actual) != 1 || actual[0].Status.CheckedAt != observation.Status.CheckedAt {
		t.Fatal("observing refreshed an old evidence timestamp")
	}
	for _, mutate := range []func(*meridianRuntimeState){
		func(value *meridianRuntimeState) { value.HandoverPending = true },
		func(value *meridianRuntimeState) { value.Applied.ConfigSHA256 = "changed" },
		func(value *meridianRuntimeState) { value.AppliedPeers[0].Identity.PublicKey = "nodekey:changed" },
		func(value *meridianRuntimeState) { value.AppliedSource.PublicKey = "nodekey:changed-source" },
	} {
		candidate := meridianLandingFixture()
		mutate(&candidate)
		observations := store.meridianPeerObservations(candidate)
		if len(observations) != 1 || observations[0].Status.State != "blocked" {
			t.Fatal("stale/foreign evidence made a new authority healthy")
		}
	}
}

func TestMeridianDrainedLegacyDigestIgnoresCountersButRejectsConfigurationDrift(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	state := testXrayWorkerState()
	if err := store.saveXrayWorkerState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.writeXrayWorkerConfig(state); err != nil {
		t.Fatal(err)
	}
	if err := store.recordXrayWorkerApplied(state); err != nil {
		t.Fatal(err)
	}
	expected, err := store.meridianLegacyWorkerDigest(context.Background(), state.ApplicationID)
	if err != nil {
		t.Fatal(err)
	}
	var inbound map[string]any
	if err := json.Unmarshal(state.Inbounds[0], &inbound); err != nil {
		t.Fatal(err)
	}
	inbound["up"], inbound["down"] = 12, 34
	state.Inbounds[0], _ = json.Marshal(inbound)
	if err := store.saveXrayWorkerState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if actual, err := store.meridianLegacyWorkerDigest(context.Background(), state.ApplicationID); err != nil || actual != expected {
		t.Fatalf("traffic counters changed authority: %v", err)
	}
	inbound["port"] = 8443
	state.Inbounds[0], _ = json.Marshal(inbound)
	if err := store.saveXrayWorkerState(context.Background(), state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.meridianLegacyWorkerDigest(context.Background(), state.ApplicationID); err == nil {
		t.Fatal("unapplied concurrent configuration drift accepted")
	}
	if _, err := store.meridianLegacyWorkerDigest(context.Background(), "foreign-application"); err == nil {
		t.Fatal("foreign worker authority accepted")
	}
}

func TestMeridianRetirementCASRetainsChangedLegacyEvidence(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			store := openMeridianLandingTestStore(t)
			legacy := seedMeridianLegacyLanding(t, store, "applied")
			sealed, _, err := store.captureMeridianLegacyLanding(context.Background(), legacy.ApplicationID)
			if err != nil {
				t.Fatal(err)
			}
			state := meridianLandingFixture()
			state.LegacyLandingSealed = sealed
			if err := store.saveMeridianRuntimeState(context.Background(), state); err != nil {
				t.Fatal(err)
			}
			if changed {
				if err := store.saveLandingRuntime(context.Background(), legacy); err != nil {
					t.Fatal(err)
				}
			}
			err = store.retireMeridianLegacyLanding(context.Background(), legacy.ApplicationID)
			var retained int
			if queryErr := store.db.QueryRow(`SELECT COUNT(*) FROM landing_runtime_state`).Scan(&retained); queryErr != nil {
				t.Fatal(queryErr)
			}
			if changed && (err == nil || retained != 1) || !changed && (err != nil || retained != 0) {
				t.Fatalf("retirement changed=%v retained=%d err=%v", changed, retained, err)
			}
		})
	}
}

func TestMeridianRetirementWaitRequiresFreshPeerEvidenceAndPreservesJournalOnTimeout(t *testing.T) {
	store := openMeridianLandingTestStore(t)
	legacy := seedMeridianLegacyLanding(t, store, "applied")
	state := meridianLandingFixture()
	task := meridianruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: *state.Applied, Peers: state.AppliedPeers, Source: state.AppliedSource}
	result := meridianruntime.Result{Receipt: meridian.AppliedReceipt{Revision: state.Applied.Revision, ConfigSHA256: state.Applied.ConfigSHA256, RuntimeReady: true}, Stats: json.RawMessage(`{"stat":[]}`), Source: cloneMeridianSource(state.AppliedSource)}
	result.Peers = []meridianruntime.PeerObservation{blockedMeridianPeer(task.Peers[0], task.Desired.Revision, "pending")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForMeridianRetirementPeers(ctx, task, func(context.Context) (meridianruntime.Result, error) { return result, nil }); err == nil {
		t.Fatal("blocked peer permitted retirement")
	}
	if current, err := store.landingRuntime(context.Background()); err != nil || current == nil || current.ApplicationID != legacy.ApplicationID {
		t.Fatal("retirement wait consumed old recovery evidence")
	}
	now := time.Now().UTC()
	result.Peers[0].Status = landing.MonitorStatus{Revision: task.Desired.Revision, State: "healthy", LinkState: "direct", TCP: true, CheckedAt: now, AllowedUntil: now.Add(landing.AllowLifetime), ExitIPv4: "1.1.1.1"}
	if err := waitForMeridianRetirementPeers(context.Background(), task, func(context.Context) (meridianruntime.Result, error) { return result, nil }); err != nil {
		t.Fatalf("fresh TCP-only evidence rejected: %v", err)
	}
}

func TestMeridianHeartbeatCadencePreservesNativeAndCapsPeerPlans(t *testing.T) {
	peers := meridianLandingFixture()
	native := meridianRuntimeState{Applied: peers.Applied}
	for _, test := range []struct {
		configured time.Duration
		state      meridianRuntimeState
		want       time.Duration
	}{
		{0, native, 15 * time.Second},
		{12 * time.Second, native, 12 * time.Second},
		{15 * time.Second, peers, landing.CheckInterval},
		{2 * time.Second, peers, 2 * time.Second},
		{0, peers, landing.CheckInterval},
		{15 * time.Second, meridianRuntimeState{Pending: peers.Applied, PendingPeers: peers.AppliedPeers}, 15 * time.Second},
	} {
		if got := meridianHeartbeatInterval(test.configured, test.state); got != test.want {
			t.Fatalf("cadence=%s want=%s", got, test.want)
		}
	}
}
