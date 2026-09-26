package meridianruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
)

func peerTaskFixture() Task {
	task := Task{
		ApplicationID: "meridian-runtime", ImageReference: "example.test/xray:current",
		Source: &landing.PeerIdentity{ID: "tailscale-source", PublicKey: "nodekey:source", Address: "100.64.0.1"},
		Peers: []Peer{
			{EgressID: "agent-east", Identity: landing.PeerIdentity{ID: "tailscale-101", PublicKey: "nodekey:east", Address: "100.64.0.10"}},
			{EgressID: "agent-west", Identity: landing.PeerIdentity{ID: "tailscale-202", PublicKey: "nodekey:west", Address: "100.64.0.20"}},
		},
	}
	setPeerTaskConfig(&task, `{"outbounds":[
		{"protocol":"freedom","tag":"direct"},
		{"protocol":"socks","tag":"not-an-identity","settings":{"servers":[{"address":"100.64.0.10","port":1080}]}},
		{"protocol":"socks","tag":"second-grant-same-peer","settings":{"servers":[{"address":"100.64.0.10","port":1080}]}},
		{"protocol":"socks","tag":"arbitrary-name","settings":{"servers":[{"address":"100.64.0.20","port":1080}]}}
	]}`)
	return task
}

func setPeerTaskConfig(task *Task, config string) {
	digest := sha256.Sum256([]byte(config))
	task.Desired = meridian.DesiredArtifact{Revision: 7, Config: []byte(config), ConfigSHA256: hex.EncodeToString(digest[:])}
}

func peerResultFixture(task Task, now time.Time) Result {
	result := Result{
		Receipt: meridian.AppliedReceipt{Revision: task.Desired.Revision, ConfigSHA256: task.Desired.ConfigSHA256, RuntimeReady: true},
		Stats:   json.RawMessage(`{"stat":[]}`),
	}
	if task.Source != nil {
		source := *task.Source
		result.Source = &source
	}
	for _, peer := range task.Peers {
		result.Peers = append(result.Peers, PeerObservation{
			EgressID: peer.EgressID, Identity: peer.Identity,
			Status: landing.MonitorStatus{
				Revision: task.Desired.Revision, State: "healthy", LinkState: "direct", TCP: true, UDP: false,
				CheckedAt: now.Add(-time.Second), AllowedUntil: now.Add(5 * time.Second), ExitIP: "1.1.1.1",
			},
		})
	}
	return result
}

func TestTaskPeerContractUsesDistinctIdentityNamespacesAndActualSOCKSDestinations(t *testing.T) {
	task := peerTaskFixture()
	if err := task.Validate(); err != nil {
		t.Fatalf("different Agent/Tailscale IDs and multiple grants to one peer must be valid: %v", err)
	}
	task.Peers[0], task.Peers[1] = task.Peers[1], task.Peers[0]
	if err := task.Validate(); err != nil {
		t.Fatalf("peer ordering must not change identity binding: %v", err)
	}
}

func TestTaskRejectsIncompleteOrAmbiguousPeerBindings(t *testing.T) {
	cases := map[string]func(*Task){
		"missing source":         func(task *Task) { task.Source = nil },
		"missing source ID":      func(task *Task) { task.Source.ID = "" },
		"invalid source key":     func(task *Task) { task.Source.PublicKey = "" },
		"invalid source address": func(task *Task) { task.Source.Address = "1.1.1.1" },
		"egress is source":       func(task *Task) { *task.Source = task.Peers[0].Identity },
		"source ID collision":    func(task *Task) { task.Source.ID = task.Peers[0].Identity.ID },
		"source key collision":   func(task *Task) { task.Source.PublicKey = task.Peers[0].Identity.PublicKey },
		"source IP collision":    func(task *Task) { task.Source.Address = task.Peers[0].Identity.Address },
		"empty egress":           func(task *Task) { task.Peers[0].EgressID = "" },
		"invalid egress":         func(task *Task) { task.Peers[0].EgressID = "agent east" },
		"empty tailnet ID":       func(task *Task) { task.Peers[0].Identity.ID = "" },
		"empty public key":       func(task *Task) { task.Peers[0].Identity.PublicKey = "" },
		"blank tailnet ID":       func(task *Task) { task.Peers[0].Identity.ID = " " },
		"blank public key":       func(task *Task) { task.Peers[0].Identity.PublicKey = " " },
		"newline in ID":          func(task *Task) { task.Peers[0].Identity.ID = "tail\nnet" },
		"nul in key":             func(task *Task) { task.Peers[0].Identity.PublicKey = "nodekey:\x00" },
		"public address":         func(task *Task) { task.Peers[0].Identity.Address = "1.1.1.1" },
		"LAN address":            func(task *Task) { task.Peers[0].Identity.Address = "192.168.1.1" },
		"mapped IPv6": func(task *Task) {
			task.Peers[0].Identity.Address = netip.AddrFrom16(netip.MustParseAddr("100.64.0.10").As16()).String()
		},
		"duplicate egress":  func(task *Task) { task.Peers[1].EgressID = task.Peers[0].EgressID },
		"duplicate tailnet": func(task *Task) { task.Peers[1].Identity.ID = task.Peers[0].Identity.ID },
		"duplicate key":     func(task *Task) { task.Peers[1].Identity.PublicKey = task.Peers[0].Identity.PublicKey },
		"duplicate address": func(task *Task) { task.Peers[1].Identity.Address = task.Peers[0].Identity.Address },
		"missing peers":     func(task *Task) { task.Peers = nil },
		"missing one peer":  func(task *Task) { task.Peers = task.Peers[:1] },
		"unused extra peer": func(task *Task) {
			task.Peers = append(task.Peers, Peer{EgressID: "agent-north", Identity: landing.PeerIdentity{ID: "tailnet-303", PublicKey: "nodekey:north", Address: "100.64.0.30"}})
		},
		"different port": func(task *Task) {
			setPeerTaskConfig(task, strings.ReplaceAll(string(task.Desired.Config), `"port":1080`, `"port":1081`))
		},
		"different destination": func(task *Task) {
			setPeerTaskConfig(task, strings.ReplaceAll(string(task.Desired.Config), "100.64.0.20", "100.64.0.30"))
		},
		"DNS destination": func(task *Task) {
			setPeerTaskConfig(task, strings.ReplaceAll(string(task.Desired.Config), "100.64.0.20", "agent-west.example.test"))
		},
		"missing SOCKS servers": func(task *Task) {
			setPeerTaskConfig(task, `{"outbounds":[{"protocol":"socks","settings":{"servers":[]}}]}`)
		},
		"invalid SOCKS settings": func(task *Task) {
			setPeerTaskConfig(task, `{"outbounds":[{"protocol":"socks","settings":"invalid"}]}`)
		},
		"unused peers": func(task *Task) {
			setPeerTaskConfig(task, `{"outbounds":[{"protocol":"freedom","tag":"agent-east"}]}`)
		},
		"changed artifact hash": func(task *Task) { task.Desired.Config = []byte(`{"outbounds":[]}`) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			task := peerTaskFixture()
			mutate(&task)
			if task.Validate() == nil {
				t.Fatal("task accepted an unbound or ambiguous peer")
			}
		})
	}
}

func TestNativeOnlyRuntimeNeedsNoPeers(t *testing.T) {
	task := peerTaskFixture()
	task.Peers = nil
	task.Source = nil
	setPeerTaskConfig(&task, `{"outbounds":[{"protocol":"freedom","tag":"direct"}]}`)
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	if err := task.Validate(); err != nil {
		t.Fatalf("native task validation: %v", err)
	}
	result := peerResultFixture(task, now)
	health, err := result.PeerHealth(task, now)
	if err != nil || len(health) != 0 {
		t.Fatalf("native-only result health=%v err=%v", health, err)
	}
	result.Source = &landing.PeerIdentity{ID: "unexpected-source", PublicKey: "nodekey:unexpected", Address: "100.64.0.1"}
	if _, err := result.PeerHealth(task, now); err == nil {
		t.Fatal("native-only result accepted an unrequested source identity")
	}
	task.Source = result.Source
	if err := task.Validate(); err == nil {
		t.Fatal("native-only task accepted an unnecessary source identity")
	}
}

func TestRuntimePeerHealthRequiresExactPeerSetAndArtifact(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cases := map[string]func(*Task, *Result){
		"missing source":         func(_ *Task, result *Result) { result.Source = nil },
		"foreign source ID":      func(_ *Task, result *Result) { result.Source.ID = "different-tailnet-source" },
		"foreign source key":     func(_ *Task, result *Result) { result.Source.PublicKey = "nodekey:foreign-source" },
		"foreign source address": func(_ *Task, result *Result) { result.Source.Address = "100.64.0.2" },
		"missing all":            func(_ *Task, result *Result) { result.Peers = nil },
		"missing one":            func(_ *Task, result *Result) { result.Peers = result.Peers[:1] },
		"extra peer": func(_ *Task, result *Result) {
			result.Peers = append(result.Peers, PeerObservation{EgressID: "agent-extra"})
		},
		"duplicate peer": func(_ *Task, result *Result) { result.Peers[1] = result.Peers[0] },
		"foreign egress": func(_ *Task, result *Result) { result.Peers[0].EgressID = "other-agent" },
		"swapped identity": func(_ *Task, result *Result) {
			result.Peers[0].Identity, result.Peers[1].Identity = result.Peers[1].Identity, result.Peers[0].Identity
		},
		"foreign tailnet ID": func(_ *Task, result *Result) { result.Peers[0].Identity.ID = "other-tailnet-id" },
		"foreign key":        func(_ *Task, result *Result) { result.Peers[0].Identity.PublicKey = "nodekey:other" },
		"foreign address":    func(_ *Task, result *Result) { result.Peers[0].Identity.Address = "100.64.0.30" },
		"wrong namespace":    func(_ *Task, result *Result) { result.Peers[0].EgressID = result.Peers[0].Identity.ID },
		"old observation":    func(_ *Task, result *Result) { result.Peers[0].Status.Revision-- },
		"future observation": func(_ *Task, result *Result) { result.Peers[0].Status.Revision++ },
		"missing revision":   func(_ *Task, result *Result) { result.Peers[0].Status.Revision = 0 },
		"old receipt":        func(_ *Task, result *Result) { result.Receipt.Revision-- },
		"stale SHA":          func(_ *Task, result *Result) { result.Receipt.ConfigSHA256 = strings.Repeat("0", 64) },
		"different config same revision": func(task *Task, _ *Result) {
			setPeerTaskConfig(task, strings.ReplaceAll(string(task.Desired.Config), "not-an-identity", "new-outbound-tag"))
		},
		"runtime not ready": func(_ *Task, result *Result) { result.Receipt.RuntimeReady = false },
		"missing stats":     func(_ *Task, result *Result) { result.Stats = nil },
		"invalid stats":     func(_ *Task, result *Result) { result.Stats = json.RawMessage(`{`) },
		"invalid task":      func(task *Task, _ *Result) { task.Peers[0].Identity.PublicKey = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			task := peerTaskFixture()
			result := peerResultFixture(task, now)
			mutate(&task, &result)
			if health, err := result.PeerHealth(task, now); err == nil || health != nil {
				t.Fatalf("unbound evidence was accepted: health=%v err=%v", health, err)
			}
		})
	}
}

func TestRuntimeReadyDoesNotMeanPeersAreHealthy(t *testing.T) {
	task := peerTaskFixture()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	result := peerResultFixture(task, now)
	result.Peers = nil
	if err := result.Validate(task.Desired); err != nil {
		t.Fatalf("basic runtime validation must stay separate: %v", err)
	}
	if _, err := result.PeerHealth(task, now); err == nil {
		t.Fatal("runtime success substituted for missing peer transport evidence")
	}
}

func TestRuntimePeerHealthIsFreshTCPOnlyAndPeerScoped(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	cases := map[string]func(*landing.MonitorStatus){
		"blocked":      func(status *landing.MonitorStatus) { status.State = "blocked" },
		"unknown":      func(status *landing.MonitorStatus) { status.State = "unknown" },
		"relay":        func(status *landing.MonitorStatus) { status.LinkState = "relay" },
		"no TCP":       func(status *landing.MonitorStatus) { status.TCP = false },
		"no timestamp": func(status *landing.MonitorStatus) { status.CheckedAt = time.Time{} },
		"future proof": func(status *landing.MonitorStatus) { status.CheckedAt = now.Add(time.Nanosecond) },
		"stale proof": func(status *landing.MonitorStatus) {
			status.CheckedAt = now.Add(-landing.AllowLifetime - time.Nanosecond)
		},
		"no lease": func(status *landing.MonitorStatus) { status.AllowedUntil = time.Time{} },
		"expired lease": func(status *landing.MonitorStatus) {
			status.AllowedUntil = now.Add(-time.Nanosecond)
		},
		"lease boundary": func(status *landing.MonitorStatus) { status.AllowedUntil = now },
		"unbounded lease": func(status *landing.MonitorStatus) {
			status.AllowedUntil = status.CheckedAt.Add(landing.AllowLifetime + time.Nanosecond)
		},
		"no exit": func(status *landing.MonitorStatus) { status.ExitIP = "" },
	}
	for _, address := range []string{"invalid", "0.0.0.0", "10.0.0.1", "100.64.0.10", "127.0.0.1", "169.254.1.1", "172.16.0.1", "192.168.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "255.255.255.255", "2001:db8::1", netip.AddrFrom16(netip.MustParseAddr("1.1.1.1").As16()).String()} {
		cases["nonpublic exit "+address] = func(status *landing.MonitorStatus) { status.ExitIP = address }
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			task := peerTaskFixture()
			result := peerResultFixture(task, now)
			mutate(&result.Peers[0].Status)
			health, err := result.PeerHealth(task, now)
			if err != nil || len(health) != 2 || health["agent-east"] || !health["agent-west"] {
				t.Fatalf("unhealthy peer contaminated another route: health=%v err=%v", health, err)
			}
		})
	}
	task := peerTaskFixture()
	result := peerResultFixture(task, now)
	result.Peers[0], result.Peers[1] = result.Peers[1], result.Peers[0]
	health, err := result.PeerHealth(task, now)
	if err != nil || !health["agent-east"] || !health["agent-west"] {
		t.Fatalf("fresh TCP-only peers with reordered observations must be healthy: health=%v err=%v", health, err)
	}
}
