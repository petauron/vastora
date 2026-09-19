package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/gateway"
)

type recordingNodeListener struct {
	applied []gateway.SharedHTTPS
	removed int
	health  error
	apply   error
}

type recordingNodeListenerCoordinator struct {
	prepared int
	restored int
}

func (coordinator *recordingNodeListenerCoordinator) PrepareNodeListener(context.Context) error {
	coordinator.prepared++
	return nil
}

func (coordinator *recordingNodeListenerCoordinator) RestoreGatewayPublicBindings(context.Context) error {
	coordinator.restored++
	return nil
}

func (listener *recordingNodeListener) Apply(_ context.Context, desired gateway.SharedHTTPS) error {
	listener.applied = append(listener.applied, desired)
	err := listener.apply
	listener.apply = nil
	return err
}

func TestNodeListenerFailureStopsWithoutRestoringPreviousListener(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(ctx, testConnection(t, "node-a", "node-a", "https://center.example.com", "credential")); err != nil {
		t.Fatal(err)
	}
	listener := &recordingNodeListener{}
	previous := gateway.NodeListenerState{Revision: 1, NodeID: "node-a", Listener: gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, RejectUnmatched: true, Routes: []gateway.Layer4Route{{ID: "old", Hostname: "old.example.com", ApplicationNodeID: "node-a", ManagedReality: true, ProxyProtocol: gateway.ProxyProtocolV2, Upstreams: []gateway.Upstream{{Address: "vastora-xray", Port: 443}}}}}}
	if err := applyNodeListenerState(ctx, store, listener, nil, previous); err != nil {
		t.Fatal(err)
	}
	listener.apply = errors.New("replacement failed")
	next := previous
	next.Revision = 2
	next.Listener.Routes[0].Hostname = "new.example.com"
	if err := applyNodeListenerState(ctx, store, listener, nil, next); err == nil || !strings.Contains(err.Error(), "replacement failed") {
		t.Fatalf("expected replacement failure, got %v", err)
	}
	if len(listener.applied) != 2 || listener.removed != 0 {
		t.Fatalf("failure performed another mutation: %+v, removals=%d", listener.applied, listener.removed)
	}
	persisted, err := store.NodeListenerState(ctx)
	if err != nil || persisted.Desired.Revision != 1 {
		t.Fatalf("persisted listener changed after failure: %+v %v", persisted, err)
	}
}

func TestFirstNodeListenerFailureStopsWithoutGatewayRollback(t *testing.T) {
	ctx := context.Background()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SaveConnection(ctx, testConnection(t, "node-a", "node-a", "https://center.example.com", "credential")); err != nil {
		t.Fatal(err)
	}
	listener := &recordingNodeListener{apply: errors.New("replacement failed")}
	coordinator := &recordingNodeListenerCoordinator{}
	desired := gateway.NodeListenerState{Revision: 1, NodeID: "node-a", Listener: gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, RejectUnmatched: true, Routes: []gateway.Layer4Route{{ID: "new", Hostname: "new.example.com", ApplicationNodeID: "node-a", ManagedReality: true, ProxyProtocol: gateway.ProxyProtocolV2, Upstreams: []gateway.Upstream{{Address: "vastora-xray", Port: 443}}}}}}
	if err := applyNodeListenerState(ctx, store, listener, coordinator, desired); err == nil || !strings.Contains(err.Error(), "replacement failed") {
		t.Fatalf("expected replacement failure, got %v", err)
	}
	if coordinator.prepared != 1 || coordinator.restored != 0 || listener.removed != 0 || len(listener.applied) != 1 {
		t.Fatalf("failed handoff performed another mutation: coordinator=%+v listener=%+v", coordinator, listener)
	}
	if _, err := store.NodeListenerState(ctx); !errors.Is(err, errNoAppliedNodeListenerState) {
		t.Fatalf("failed first listener persisted state: %v", err)
	}
}

func (listener *recordingNodeListener) Remove(context.Context) error {
	listener.removed++
	return nil
}

func (listener *recordingNodeListener) Health(context.Context) error { return listener.health }

func (listener *recordingNodeListener) Absent(context.Context) error {
	if listener.removed == 0 {
		return errors.New("HAProxy is still present")
	}
	return nil
}

func (listener *recordingNodeListener) ConfigurationHash(context.Context) (string, error) {
	if len(listener.applied) == 0 {
		return "", errors.New("HAProxy configuration is unavailable")
	}
	configuration, err := haproxyConfiguration(listener.applied[len(listener.applied)-1])
	if err != nil {
		return "", err
	}
	return haproxyConfigurationHash(configuration), nil
}

func TestNodeListenerRejectsCrossNodeStateAndPersistsLastAppliedRevision(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	operation, err := store.BeginEnrollmentOperation(ctx, "https://center.example.test", "token", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteEnrollmentOperation(ctx, operation, Enrollment{ID: "node-a", Credential: "credential", Name: "node-a", Roles: []string{"worker"}, Capabilities: Capabilities{Docker: true}}); err != nil {
		t.Fatal(err)
	}
	listener := &recordingNodeListener{}
	state := gateway.NodeListenerState{Revision: 1, NodeID: "node-b", Listener: gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, RejectUnmatched: true, Routes: []gateway.Layer4Route{{ID: "route", Hostname: "reality.example.com", ApplicationNodeID: "node-b", ManagedReality: true, ProxyProtocol: gateway.ProxyProtocolV2, Upstreams: []gateway.Upstream{{Address: "vastora-xray", Port: 443}}}}}}
	if err := applyNodeListenerState(ctx, store, listener, nil, state); err == nil {
		t.Fatal("cross-node listener state was accepted")
	}
	state.NodeID = "node-a"
	state.Listener.Routes[0].ApplicationNodeID = "node-a"
	if err := applyNodeListenerState(ctx, store, listener, nil, state); err != nil {
		t.Fatal(err)
	}
	if err := applyNodeListenerState(ctx, store, listener, nil, state); err != nil {
		t.Fatal(err)
	}
	if len(listener.applied) != 1 {
		t.Fatalf("same revision applied %d times", len(listener.applied))
	}
	persisted, err := store.NodeListenerState(ctx)
	if err != nil || persisted.Desired.Revision != 1 {
		t.Fatalf("persisted state = %#v, err = %v", persisted, err)
	}
	listener.health = errors.New("HAProxy stopped")
	if err := applyNodeListenerState(ctx, store, listener, nil, state); err == nil {
		t.Fatal("unhealthy same revision was accepted")
	}
	if len(listener.applied) != 1 {
		t.Fatal("health failure triggered an automatic reapply")
	}
	healthy, revision, hash := nodeListenerRuntimeStatus(ctx, store, listener)
	if healthy || revision != 1 || hash == "" {
		t.Fatalf("runtime status = healthy:%v revision:%d hash:%q", healthy, revision, hash)
	}
}

func TestNodeListenerRejectsManagedRealityThatIsNotLocal(t *testing.T) {
	newState := func() gateway.NodeListenerState {
		return gateway.NodeListenerState{Revision: 1, NodeID: "node-a", Listener: gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, RejectUnmatched: true, Routes: []gateway.Layer4Route{{ID: "route", Hostname: "reality.example.com", ApplicationNodeID: "node-a", ManagedReality: true, ProxyProtocol: gateway.ProxyProtocolV2, Upstreams: []gateway.Upstream{{Address: "vastora-xray", Port: 443}}}}}}
	}
	invalid := []gateway.NodeListenerState{newState(), newState(), newState(), newState()}
	invalid[0].Listener.Routes[0].Upstreams = []gateway.Upstream{{Address: "203.0.113.20", Port: 443}}
	invalid[1].Listener.Routes[0].Upstreams = []gateway.Upstream{{Address: "vastora-xray", Port: 8443}}
	invalid[2].Listener.Routes[0].ProxyProtocol = ""
	invalid[3].Listener.Routes[0].ManagedReality = false
	for index, state := range invalid {
		if err := validateAgentNodeListenerState(state); err == nil {
			t.Fatalf("unsafe managed REALITY state %d was accepted", index)
		}
	}
}

func TestNodeListenerRemovalIsIndependentFromGatewayState(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	operation, err := store.BeginEnrollmentOperation(ctx, "https://center.example.test", "token", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteEnrollmentOperation(ctx, operation, Enrollment{ID: "node-a", Credential: "credential", Name: "node-a", Roles: []string{"worker"}, Capabilities: Capabilities{Docker: true}}); err != nil {
		t.Fatal(err)
	}
	listener := &recordingNodeListener{}
	state := gateway.NodeListenerState{Revision: 1, NodeID: "node-a", Listener: gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, RejectUnmatched: true}}
	if err := applyNodeListenerState(ctx, store, listener, nil, state); err != nil {
		t.Fatal(err)
	}
	if listener.removed != 1 {
		t.Fatalf("listener removals = %d", listener.removed)
	}
	if _, err := store.GatewayState(ctx); !errors.Is(err, errNoAppliedGatewayState) {
		t.Fatalf("node-listener removal changed Gateway state: %v", err)
	}
}

func TestNodeListenerStartupRestoresGatewayBindingsAfterEmptyState(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	operation, err := store.BeginEnrollmentOperation(ctx, "https://center.example.test", "token", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteEnrollmentOperation(ctx, operation, Enrollment{ID: "node-a", Credential: "credential", Name: "node-a", Roles: []string{"worker", "gateway"}, Capabilities: Capabilities{Docker: true, Gateway: true}}); err != nil {
		t.Fatal(err)
	}
	listener := &recordingNodeListener{}
	coordinator := &recordingNodeListenerCoordinator{}
	state := gateway.NodeListenerState{Revision: 1, NodeID: "node-a", Listener: gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, CaddyAddress: "vastora-gateway-caddy", CaddyPort: 443}}
	if err := applyNodeListenerState(ctx, store, listener, coordinator, state); err != nil {
		t.Fatal(err)
	}
	listener.removed = 0
	coordinator.restored = 0
	if err := restoreNodeListenerState(ctx, store, listener, coordinator); err != nil {
		t.Fatal(err)
	}
	if listener.removed != 1 || coordinator.restored != 1 {
		t.Fatalf("startup empty-state restore = removals:%d gateway-restores:%d", listener.removed, coordinator.restored)
	}
}
