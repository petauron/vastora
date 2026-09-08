//go:build linux && integration

package landing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Run only in repository CI, in a new network namespace. The parent never
// changes the runner's firewall, Docker networks or network connectivity.
func TestBridgeGateKernelPolicyAndExpiry(t *testing.T) {
	if os.Getenv("VASTORA_LANDING_KERNEL_CHILD") != "1" {
		command := exec.Command("unshare", "--net", "--", os.Args[0], "-test.run=^TestBridgeGateKernelPolicyAndExpiry$", "-test.v")
		command.Env = append(os.Environ(), "VASTORA_LANDING_KERNEL_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated kernel check: %v\n%s", err, output)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	gate, err := NewBridgeGate(PeerIdentity{ID: "kernel-test", PublicKey: "nodekey:kernel-test", Address: "100.64.0.8"}, "br-test", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Diagnostics here contain only this isolated fixture, never a host's
	// firewall or application configuration.
	gate.run = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "/usr/sbin/nft", args...)
		command.Stdin = bytes.NewReader(input)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("nft: %w: %s", err, stderr.String())
		}
		return output, nil
	}
	if err := gate.Install(ctx); err != nil {
		document, _ := gate.snapshot(ctx)
		actual, _ := json.Marshal(document)
		expected, _ := json.Marshal(gate.objects())
		t.Fatalf("install: %v\nactual: %s\nexpected: %s", err, actual, expected)
	}
	until := time.Now().Add(5 * time.Second)
	if err := gate.renew(ctx, until); err != nil {
		document, _ := gate.snapshot(ctx)
		t.Fatalf("renewal: %v; fixture: %#v", err, document)
	}
	// No monitoring process refreshes the grant. The kernel must expire it.
	<-time.After(time.Until(until))
	document, err := gate.snapshot(ctx)
	if err != nil || gate.hasLiveLease(document, time.Now().Add(AllowLifetime)) {
		t.Fatalf("permission survived proof expiry: %v", err)
	}
	if err := gate.renew(ctx, time.Now().Add(AllowLifetime)); err != nil {
		t.Fatal(err)
	}
	if err := gate.Install(ctx); err != nil { // process restart closes a live grant
		t.Fatal(err)
	}
	document, err = gate.snapshot(ctx)
	if err != nil || len(gate.elements(document)) != 0 {
		t.Fatalf("process restart retained permission: %v", err)
	}
}
