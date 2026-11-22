package center

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodeswap"
)

func TestNodeSwapTaskLifecycleAndRevisionGuard(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()

	node := enrollOrchestrationNode(
		t,
		s,
		"swap-fixture",
		NodeCapabilities{Docker: true, NodeSwap: true},
		[]networking.Candidate{{Address: "10.0.0.19", Interface: "eth0", Kind: networking.KindLAN}},
		networking.Profile{ServiceAddress: "10.0.0.19", LANAddress: "10.0.0.19", EnabledKinds: []string{networking.KindLAN}},
	)
	landingCaps, _ := json.Marshal(NodeCapabilities{LandingOnly: true, NativeEgress: true, NodeSwap: true})
	if _, err := s.db.Exec(`UPDATE agents SET capabilities_json=? WHERE id=?`, landingCaps, node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO admins(id,username,password_hash,created_at) VALUES('swap-admin','swap-admin','test-hash','')`); err != nil {
		t.Fatal(err)
	}

	initial, err := s.NodeSwap(ctx, node.ID)
	if err != nil || initial.State != "unconfigured" {
		t.Fatalf("unexpected initial view: %+v err=%v", initial, err)
	}

	rev1 := strings.Repeat("1", 64)
	if _, err := s.QueueNodeSwap(ctx, node.ID, "swap-admin", nodeswap.Task{Action: "apply", Revision: rev1}); err == nil {
		t.Fatal("expected apply before inspect to fail")
	}

	queued, err := s.QueueNodeSwap(ctx, node.ID, "swap-admin", nodeswap.Task{Action: "inspect"})
	if err != nil || queued.State != "pending" {
		t.Fatalf("inspect not queued: %+v err=%v", queued, err)
	}
	if _, err := s.QueueNodeSwap(ctx, node.ID, "swap-admin", nodeswap.Task{Action: "inspect"}); err == nil {
		t.Fatal("expected concurrent swap task to be rejected")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.claimNodeSwap(ctx, tx, node.ID)
	if err != nil || claimed == nil {
		_ = tx.Rollback()
		t.Fatalf("claim failed: %+v err=%v", claimed, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if claimed.Kind != nodeswap.Kind || claimed.NodeSwap == nil || claimed.NodeSwap.Action != "inspect" {
		t.Fatalf("unexpected claimed task: %+v", claimed)
	}

	eligibleView := nodeswap.View{
		State:                "eligible",
		Revision:             rev1,
		MemoryTotalBytes:     1024 << 20,
		MemoryAvailableBytes: 700 << 20,
		DiskFreeBytes:        8 << 30,
		Virtualization:       "kvm",
		Systemd:              true,
		Swappiness:           60,
		Swaps:                []nodeswap.SwapDevice{},
		Target:               nodeswap.TierForMemoryKB(1048576),
		CanApply:             true,
	}
	rawEligible, _ := json.Marshal(map[string]any{"nodeSwap": eligibleView})
	if err := s.completeNodeSwap(ctx, commitProjectionOnlyForTest, node.ID, claimed.ID, claimed.Attempt, true, "", rawEligible); err != nil {
		t.Fatalf("complete inspect: %v", err)
	}

	staleRev := strings.Repeat("2", 64)
	if _, err := s.QueueNodeSwap(ctx, node.ID, "swap-admin", nodeswap.Task{Action: "apply", Revision: staleRev}); err == nil {
		t.Fatal("expected apply with mismatched revision to fail")
	}
	if _, err := s.QueueNodeSwap(ctx, node.ID, "swap-admin", nodeswap.Task{Action: "restore", Revision: rev1}); err == nil {
		t.Fatal("expected restore when CanRestore=false to fail")
	}

	applyQueued, err := s.QueueNodeSwap(ctx, node.ID, "swap-admin", nodeswap.Task{Action: "apply", Revision: rev1})
	if err != nil || applyQueued.State != "pending" {
		t.Fatalf("apply not queued: %+v err=%v", applyQueued, err)
	}

	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	applyClaimed, err := s.claimNodeSwap(ctx, tx, node.ID)
	if err != nil || applyClaimed == nil {
		_ = tx.Rollback()
		t.Fatalf("claim apply failed: %+v err=%v", applyClaimed, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	revApplied := strings.Repeat("3", 64)
	appliedView := eligibleView
	appliedView.State = "applied"
	appliedView.Revision = revApplied
	appliedView.CanApply = false
	appliedView.CanRestore = true
	appliedView.Zram = &nodeswap.ZramDevice{DisksizeBytes: 256 << 20, Algorithm: "lz4"}
	appliedView.Swaps = []nodeswap.SwapDevice{
		{Name: "/dev/zram0", Type: "partition", SizeBytes: 268431360, Priority: 100},
		{Name: "/swapfile", Type: "file", SizeBytes: 536866816, Priority: -2},
	}
	rawApplied, _ := json.Marshal(map[string]any{"nodeSwap": appliedView})
	if err := s.completeNodeSwap(ctx, commitProjectionOnlyForTest, node.ID, applyClaimed.ID, applyClaimed.Attempt, true, "", rawApplied); err != nil {
		t.Fatalf("complete apply: %v", err)
	}

	afterApply, err := s.NodeSwap(ctx, node.ID)
	if err != nil || afterApply.Result == nil || afterApply.Result.State != "applied" || !afterApply.Result.CanRestore {
		t.Fatalf("unexpected view after apply: %+v err=%v", afterApply, err)
	}

	// Offline node must be rejected.
	if _, err := s.db.Exec(`UPDATE agents SET last_seen_at=? WHERE id=?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), node.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueueNodeSwap(ctx, node.ID, "swap-admin", nodeswap.Task{Action: "inspect"}); err == nil {
		t.Fatal("expected offline node to be rejected")
	}
}
