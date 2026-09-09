package center

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/networking"
)

func TestLandingLatencyDeltaOnlyChangesTheCompletedPair(t *testing.T) {
	now := time.Now().UTC()
	fast, slow := 12.0, 130.0
	a := LandingLatencyView{NodeID: "one", LandingNodeID: "exit", State: "direct", LatencyMS: &fast, CheckedAt: now}
	b := LandingLatencyView{NodeID: "two", LandingNodeID: "exit", State: "direct", LatencyMS: &slow, CheckedAt: now}
	initial := landingLatencyDelta(3, nil, []LandingLatencyView{a, b}, true)
	if !initial.Reset || len(initial.Upserts) != 2 {
		t.Fatalf("initial snapshot: %+v", initial)
	}
	updated := a
	updated.CheckedAt = now.Add(time.Second)
	updated.LatencyMS = &slow
	delta := landingLatencyDelta(3, []LandingLatencyView{a, b}, []LandingLatencyView{updated, b}, false)
	if delta.Reset || !reflect.DeepEqual(delta.Upserts, []LandingLatencyView{updated}) || len(delta.Removed) != 0 {
		t.Fatalf("unrelated pair refreshed: %+v", delta)
	}
	expired := landingLatencyDelta(3, []LandingLatencyView{updated, b}, []LandingLatencyView{updated}, false)
	if len(expired.Upserts) != 0 || !reflect.DeepEqual(expired.Removed, []LandingLatencyPair{{"two", "exit"}}) {
		t.Fatalf("expiry crossed pair boundary: %+v", expired)
	}
	reset := landingLatencyDelta(4, []LandingLatencyView{a, b}, nil, true)
	if !reset.Reset || len(reset.Upserts) != 0 || len(reset.Removed) != 0 {
		t.Fatalf("revision reset: %+v", reset)
	}
}

func TestLandingLatencyReportStreamsBeforeNextHeartbeat(t *testing.T) {
	store := openOrchestrationStore(t)
	defer store.Close()
	ctx := context.Background()
	nodes := []AgentCredential{}
	for _, address := range []string{"10.0.0.80", "10.0.0.81"} {
		nodes = append(nodes, enrollOrchestrationNode(t, store, address, NodeCapabilities{Docker: true}, []networking.Candidate{{Address: address, Interface: "eth0", Kind: networking.KindLAN}}, networking.Profile{ServiceAddress: address, LANAddress: address, EnabledKinds: []string{networking.KindLAN}}))
	}
	owner, source := nodes[0], nodes[1]
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := store.db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE agents SET tailscale_ownership='managed' WHERE id=?`, owner.ID)
	exec(`UPDATE agent_network_profiles SET headscale_address='100.64.0.8' WHERE agent_id=?`, owner.ID)
	if err := store.SelectLanding(ctx, LandingSelection{NodeIDs: []string{owner.ID}}); err != nil {
		t.Fatal(err)
	}
	peer := landing.PeerIdentity{ID: "peer", PublicKey: "key", Address: "100.64.0.8"}
	encodedPeer, _ := json.Marshal(peer)
	exec(`UPDATE landing_server_states SET status='ready',applied_revision=desired_revision,peer_json=? WHERE node_id=?`, encodedPeer, owner.ID)
	now := store.now().UTC().Format(time.RFC3339Nano)
	exec(`INSERT INTO applications(id,name,node_id,site_id,app_key,status,runtime,role,created_at,updated_at) VALUES('latency-app','Proxy',?,?,'vastora-official/3x-ui','running','docker','worker',?,?)`, source.ID, testSiteID(t, store), now, now)
	session, _, err := store.CreateFirstAdmin(ctx, "admin", "correct-horse-battery-staple")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewServer(store, "", false).Handler())
	defer server.Close()
	streamURL := server.URL + "/api/v1/three-x-ui/landing/latencies/events"
	unauthorized, err := http.Get(streamURL)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unprotected stream: %d", unauthorized.StatusCode)
	}
	streamContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(streamContext, http.MethodGet, streamURL, nil)
	request.AddCookie(&http.Cookie{Name: "vastora_session", Value: session})
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream response: %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	read := func() LandingLatencyEvent {
		t.Helper()
		for scanner.Scan() {
			if payload, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				var event LandingLatencyEvent
				if err := json.Unmarshal([]byte(payload), &event); err != nil {
					t.Fatal(err)
				}
				return event
			}
		}
		t.Fatalf("missing streamed result: %v", scanner.Err())
		return LandingLatencyEvent{}
	}
	if event := read(); !event.Reset || event.Revision != 1 || len(event.Upserts) != 0 {
		t.Fatalf("initial event: %+v", event)
	}
	ms := 12.0
	observation := landing.LatencyObservation{Target: landing.LatencyTarget{NodeID: owner.ID, Revision: 1, Peer: peer}, State: "direct", LatencyMS: &ms, CheckedAt: store.now().UTC()}
	post := func(node, credential string, value landing.LatencyObservation, want int) {
		t.Helper()
		body, _ := json.Marshal(value)
		req, _ := http.NewRequestWithContext(streamContext, http.MethodPost, server.URL+"/api/v1/agents/"+node+"/landing-latencies", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		result, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		result.Body.Close()
		if result.StatusCode != want {
			t.Fatalf("report status = %d, want %d", result.StatusCode, want)
		}
	}
	post(source.ID, "incorrect", observation, http.StatusUnauthorized)
	post(owner.ID, source.Credential, observation, http.StatusUnauthorized)
	post(source.ID, source.Credential, observation, http.StatusNoContent)
	event := read()
	if event.Reset || len(event.Upserts) != 1 || event.Upserts[0].NodeID != source.ID || event.Upserts[0].LandingNodeID != owner.ID || event.Upserts[0].LatencyMS == nil || *event.Upserts[0].LatencyMS != ms {
		t.Fatalf("wrong streamed pair: %+v", event)
	}
	post(source.ID, source.Credential, observation, http.StatusConflict) // Replayed evidence.
	observation.CheckedAt = store.now().UTC()
	observation.Target.Peer.PublicKey = "wrong-identity"
	post(source.ID, source.Credential, observation, http.StatusConflict)
	if err := store.SelectLanding(ctx, LandingSelection{Revision: 1, NodeIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	observation.Target.Peer = peer
	post(source.ID, source.Credential, observation, http.StatusConflict)
	if event := read(); !event.Reset || event.Revision != 2 || len(event.Upserts) != 0 {
		t.Fatalf("removed selection remained visible: %+v", event)
	}
}
