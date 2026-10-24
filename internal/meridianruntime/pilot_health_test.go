package meridianruntime

import (
	"testing"
	"time"
)

func TestPilotRouteHealthRequiresFreshPinnedBusinessProof(t *testing.T) {
	now := time.Now().UTC()
	task := peerTaskFixture()
	report := peerResultFixture(task, now)
	checkpoint := PilotCounters{Revision: task.Desired.Revision, Source: task.Source, Peers: task.Peers, ExpectedExits: map[string]string{}}
	health := &PilotHealth{SampledAt: now, Source: task.Source, Peers: map[string]PeerObservation{}}
	for _, peer := range report.Peers {
		peer.Status.Stage = "startup"
		peer.Status.Reason = "startup_ready"
		peer.Status.UDP = false
		peer.Status.StartedAt = now.Add(-time.Second)
		peer.Status.CheckedAt = now
		peer.Status.SecurityCheckedAt = now
		peer.Status.LastHealthyAt = now
		peer.Status.AllowedUntil = time.Time{}
		checkpoint.ExpectedExits[peer.EgressID] = peer.Status.ExitIP
		health.Peers[peer.EgressID] = peer
	}
	receipt := PilotReceipt{Health: health}
	deadlines, err := PilotPeerReadiness(checkpoint, receipt, now)
	if err != nil || len(deadlines) != len(task.Peers) {
		t.Fatal("valid business evidence rejected", deadlines, err)
	}

	for id, peer := range health.Peers {
		peer.Status.LinkState, peer.Status.Reason = "authenticated", "startup_ready"
		health.Peers[id] = peer
	}
	if deadlines, err := PilotPeerReadiness(checkpoint, receipt, now); err != nil || len(deadlines) != len(task.Peers) {
		t.Fatal("public business evidence rejected", err)
	}
	for id, peer := range health.Peers {
		saved := peer
		peer.Status.ExitIP = "8.8.4.4"
		health.Peers[id] = peer
		if deadlines, err := PilotPeerReadiness(checkpoint, receipt, now); err != nil || deadlines[id] {
			t.Fatal("changed public exit accepted", err)
		}
		health.Peers[id] = saved
	}
	if ready, err := PilotPeerReadiness(checkpoint, receipt, now.Add(24*time.Hour)); err != nil || len(ready) != len(task.Peers) {
		t.Fatal("startup proof expired", err)
	}
	first := task.Peers[0].EgressID
	receipt.RuntimeRevision = checkpoint.Revision + 1
	if _, err := PilotPeerReadiness(checkpoint, receipt, now); err == nil {
		t.Fatal("old health accepted for changed runtime")
	}
	for id, p := range health.Peers {
		p.Status.Revision = receipt.RuntimeRevision
		health.Peers[id] = p
	}
	if deadlines, err := PilotPeerReadiness(checkpoint, receipt, now); err != nil || len(deadlines) != len(task.Peers) {
		t.Fatal("verified transport revision health rejected", err)
	}
	peer := health.Peers[first]
	peer.Status.State, peer.Status.Reason, peer.Status.Stage = "degraded", "network_failure_grace", "route_connect_or_tls"
	peer.Status.ConsecutiveFailures = 2
	peer.Status.LastHealthyAt = now.Add(-10 * time.Second)
	health.Peers[first] = peer
	deadlines, err = PilotPeerReadiness(checkpoint, receipt, now)
	if err != nil || deadlines[first] {
		t.Fatal("legacy runtime grace accepted as startup proof", err)
	}
	peer.Status.ConsecutiveFailures = 3
	health.Peers[first] = peer
	deadlines, err = PilotPeerReadiness(checkpoint, receipt, now)
	if err != nil || deadlines[first] {
		t.Fatal("third failure retained route", err)
	}
	peer.Status.ConsecutiveFailures = 2
	peer.Status.LastHealthyAt = now.Add(-46 * time.Second)
	health.Peers[first] = peer
	deadlines, err = PilotPeerReadiness(checkpoint, receipt, now)
	if err != nil || deadlines[first] {
		t.Fatal("stale business evidence retained route", err)
	}
	peer.Status.State = "blocked"
	health.Peers[first] = peer
	deadlines, err = PilotPeerReadiness(checkpoint, receipt, now)
	if err != nil || deadlines[first] || len(deadlines) != len(task.Peers)-1 {
		t.Fatal("blocked peer changed other route health", deadlines, err)
	}
	peer.Identity.PublicKey = "different-peer"
	health.Peers[first] = peer
	if _, err := PilotPeerReadiness(checkpoint, receipt, now); err == nil {
		t.Fatal("foreign peer accepted")
	}
	if deadlines, err := PilotPeerReadiness(checkpoint, PilotReceipt{}, now); err != nil || len(deadlines) != 0 {
		t.Fatal("missing health cleared warning")
	}
}
