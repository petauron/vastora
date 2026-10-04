//go:build linux && integration

package landing

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestHostGateKernelPolicyAndExpiry(t *testing.T) {
	if os.Getenv("VASTORA_HOST_GATE_CHILD") != "1" {
		command := exec.Command("unshare", "--net", "--", os.Args[0], "-test.run=^TestHostGateKernelPolicyAndExpiry$", "-test.v")
		command.Env = append(os.Environ(), "VASTORA_HOST_GATE_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated host gate: %v\n%s", err, output)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	peer := PeerIdentity{ID: "host-test", PublicKey: "nodekey:host-test", Address: "100.64.0.8"}
	gate, err := NewHostGate(peer, 1001, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := gate.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if err := gate.renew(ctx, time.Now().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	document, err := gate.snapshot(ctx)
	if err != nil || len(gate.elements(document)) != 0 {
		t.Fatalf("lease did not expire: %v", err)
	}
	old, _ := NewHostGate(peer, 1001, 6)
	if err := old.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if err := gate.renew(ctx, time.Now().Add(AllowLifetime)); err != nil {
		t.Fatal(err)
	}
	if err := gate.RemoveClosedConflicts(ctx); err != nil {
		t.Fatal(err)
	}
	document, err = gate.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if found, err := old.validate(document); found || err != nil {
		t.Fatal("old gate survived cleanup")
	}
	if err := gate.Install(ctx); err != nil {
		t.Fatal(err)
	}
	if err := gate.Remove(ctx); err != nil {
		t.Fatal(err)
	}
}
