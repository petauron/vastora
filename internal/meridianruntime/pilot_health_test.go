package meridianruntime

import (
	"testing"
	"time"

	"github.com/petauron/vastora/internal/landing"
)

func TestPilotRouteHealthRequiresFreshPinnedBusinessProof(t *testing.T) {
	now := time.Now().UTC()
	task := peerTaskFixture()
	report := peerResultFixture(task, now)
	checkpoint := PilotCounters{Revision: task.Desired.Revision, Source: task.Source, Peers: task.Peers, ExpectedExits: map[string]string{}}
	health := &PilotHealth{SampledAt: now, Source: task.Source, Peers: map[string]PeerObservation{}}
	for _, peer := range report.Peers {
		peer.Status.Stage = "complete"
		peer.Status.Reason = "direct_and_business_ready"
		peer.Status.UDP = false
		peer.Status.StartedAt = now.Add(-time.Second)
		peer.Status.CheckedAt = now
		peer.Status.SecurityCheckedAt = now
		peer.Status.LastHealthyAt = now
		peer.Status.AllowedUntil = now.Add(landing.AllowLifetime - time.Second)
		checkpoint.ExpectedExits[peer.EgressID] = peer.Status.ExitIP
		health.Peers[peer.EgressID] = peer
	}
	receipt := PilotReceipt{Health: health}
	deadlines, err := PilotPeerDeadlines(checkpoint, receipt, now)
	if err != nil || len(deadlines) != len(task.Peers) {
		t.Fatal("valid business evidence rejected", deadlines, err)
	}
	first := task.Peers[0].EgressID
	receipt.RuntimeRevision = checkpoint.Revision + 1
	if _, err := PilotPeerDeadlines(checkpoint, receipt, now); err == nil {
		t.Fatal("old health accepted for changed runtime")
	}
	for id, p := range health.Peers {
		p.Status.Revision = receipt.RuntimeRevision
		health.Peers[id] = p
	}
	if deadlines, err := PilotPeerDeadlines(checkpoint, receipt, now); err != nil || len(deadlines) != len(task.Peers) {
		t.Fatal("verified transport revision health rejected", err)
	}
	peer := health.Peers[first]
	peer.Status.State, peer.Status.Reason, peer.Status.Stage = "degraded", "network_failure_grace", "route_connect_or_tls"
	peer.Status.ConsecutiveFailures = 2
	peer.Status.LastHealthyAt = now.Add(-10 * time.Second)
	health.Peers[first] = peer
	deadlines, err = PilotPeerDeadlines(checkpoint, receipt, now)
	if err != nil || deadlines[first] == 0 {
		t.Fatal("bounded grace rejected", err)
	}
	peer.Status.ConsecutiveFailures = 3
	health.Peers[first] = peer
	deadlines, err = PilotPeerDeadlines(checkpoint, receipt, now)
	if err != nil || deadlines[first] != 0 {
		t.Fatal("third failure retained route", err)
	}
	peer.Status.ConsecutiveFailures = 2
	peer.Status.LastHealthyAt = now.Add(-46 * time.Second)
	health.Peers[first] = peer
	deadlines, err = PilotPeerDeadlines(checkpoint, receipt, now)
	if err != nil || deadlines[first] != 0 {
		t.Fatal("stale business evidence retained route", err)
	}
	peer.Status.State = "blocked"
	health.Peers[first] = peer
	deadlines, err = PilotPeerDeadlines(checkpoint, receipt, now)
	if err != nil || deadlines[first] != 0 || len(deadlines) != len(task.Peers)-1 {
		t.Fatal("blocked peer changed other route health", deadlines, err)
	}
	peer.Identity.PublicKey = "different-peer"
	health.Peers[first] = peer
	if _, err := PilotPeerDeadlines(checkpoint, receipt, now); err == nil {
		t.Fatal("foreign peer accepted")
	}
	if deadlines, err := PilotPeerDeadlines(checkpoint, PilotReceipt{}, now); err != nil || len(deadlines) != 0 {
		t.Fatal("missing health cleared warning")
	}
}
