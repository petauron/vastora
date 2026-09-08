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

func TestNativeServerKernelPolicy(t *testing.T) {
	if os.Getenv("VASTORA_LANDING_SERVER_KERNEL_CHILD") != "1" {
		command := exec.Command("unshare", "--net", "--", os.Args[0], "-test.run=^TestNativeServerKernelPolicy$", "-test.v")
		command.Env = append(os.Environ(), "VASTORA_LANDING_SERVER_KERNEL_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated native firewall: %v\n%s", err, output)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	run := func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		command := exec.CommandContext(ctx, "/usr/sbin/nft", args...)
		command.Stdin = bytes.NewReader(input)
		var stderr bytes.Buffer
		command.Stderr = &stderr
		output, err := command.Output()
		if err != nil {
			return nil, fmt.Errorf("isolated nft: %w: %s", err, stderr.String())
		}
		return output, nil
	}
	policy := testServerFirewall()
	for range 2 {
		if err := policy.install(ctx, run); err != nil {
			actual, _ := run(ctx, nil, "--json", "list", "ruleset")
			expected, _ := json.Marshal(policy.objects())
			t.Fatalf("install: %v\nactual: %s\nexpected: %s", err, actual, expected)
		}
	}
}
