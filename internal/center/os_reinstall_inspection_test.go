package center

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/petauron/vastora/internal/networking"
	"github.com/petauron/vastora/internal/nodediagnostics"
	"github.com/petauron/vastora/internal/osreinstall"
)

func TestOSReinstallInspectionRequiresCapabilityAndMatchingReceipt(t *testing.T) {
	s := openOrchestrationStore(t)
	defer s.Close()
	ctx := context.Background()
	candidates := []networking.Candidate{{Address: "10.0.0.9", Interface: "eth0", Kind: networking.KindLAN}}
	profile := networking.Profile{ServiceAddress: "10.0.0.9", LANAddress: "10.0.0.9", EnabledKinds: []string{networking.KindLAN}}
	old := enrollOrchestrationNode(t, s, "old-agent", NodeCapabilities{Docker: true, HostProfile: true}, candidates, profile)
	if err := s.StartOSReinstallInspection(ctx, old.ID); err == nil {
		t.Fatal("old Agent accepted")
	}
	node := enrollOrchestrationNode(t, s, "new-agent", NodeCapabilities{Docker: true, HostProfile: true, OSReinstallPreflight: true}, candidates, profile)
	// Existing network-optimization evidence must not be replaced by preflight.
	if err := s.StartNodeDiagnostic(ctx, node.ID, nodediagnostics.HostProfileKind); err != nil {
		t.Fatal(err)
	}
	ordinaryTx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := s.claimNodeDiagnostic(ctx, ordinaryTx, node.ID)
	if err != nil || ordinary == nil {
		_ = ordinaryTx.Rollback()
		t.Fatalf("claim ordinary profile: %v", err)
	}
	if err = ordinaryTx.Commit(); err != nil {
		t.Fatal(err)
	}
	host := nodediagnostics.HostProfile{CPUCount: 1, MemoryBytes: 1024, DiskBytes: 1024, Kernel: "test", Architecture: "amd64"}
	raw, _ := json.Marshal(map[string]any{"nodeDiagnostics": nodediagnostics.Result{Host: &host}})
	if err = s.completeNodeDiagnostic(ctx, commitProjectionOnlyForTest, node.ID, ordinary.ID, ordinary.Attempt, true, raw); err != nil {
		t.Fatal(err)
	}
	if err := s.StartOSReinstallInspection(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.claimNodeDiagnostic(ctx, tx, node.ID)
	if err != nil || task == nil {
		_ = tx.Rollback()
		t.Fatalf("claim: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if task.NodeDiagnostics == nil || !task.NodeDiagnostics.OSReinstallPreflight {
		t.Fatal("inspection flag missing")
	}

	if err = s.completeNodeDiagnostic(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt, true, raw); err == nil {
		t.Fatal("unrelated host profile accepted")
	}
	host.OSReinstall = &osreinstall.Inspection{
		Virtualization: "kvm", Architecture: "amd64",
		Limitations: []string{"network_configuration_requires_review"},
		Disk:        osreinstall.Disk{Path: "/dev/vda", Bytes: 8 << 30, MajorMinor: "252:0", PartitionTableID: "aabbccdd"},
		Network:     osreinstall.Network{Interface: "eth0", MAC: "02:00:00:00:00:01", Addresses: []osreinstall.Address{{Prefix: "192.0.2.10/24"}}, Gateways: []osreinstall.Gateway{{Family: "ipv4", Address: "192.0.2.1"}}, DNS: []string{"192.0.2.53"}},
	}
	raw, _ = json.Marshal(map[string]any{"nodeDiagnostics": nodediagnostics.Result{Host: &host}})
	if err = s.completeNodeDiagnostic(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt+1, true, raw); err == nil {
		t.Fatal("stale attempt accepted")
	}
	if err = s.completeNodeDiagnostic(ctx, commitProjectionOnlyForTest, node.ID, task.ID, task.Attempt, true, raw); err != nil {
		t.Fatal(err)
	}
	view, err := s.OSReinstallInspection(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || view.AgentID != node.ID || view.Host == nil || view.Host.OSReinstall == nil || view.Host.OSReinstall.Disk.Path != "/dev/vda" {
		t.Fatal("inspection result missing")
	}
	views, err := s.ListNodeDiagnostics(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].ID != ordinary.ID || views[0].Host == nil || views[0].Host.OSReinstall != nil {
		t.Fatal("reinstall inspection changed or exposed the ordinary host profile")
	}
	if err = s.StartOSReinstallInspection(ctx, node.ID); err != nil {
		t.Fatal(err)
	}
	view, err = s.OSReinstallInspection(ctx, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view == nil || view.State != "pending" || view.Host != nil || view.CheckedAt != "" {
		t.Fatal("stale inspection remained visible during a new request")
	}

}
