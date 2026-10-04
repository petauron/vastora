package meridianruntime

import (
	"context"
	"testing"
)

func TestAcceptanceProbeRejectsUnverifiedEgress(t *testing.T) {
	for _, address := range []string{"", "invalid", "127.0.0.1", "100.64.0.1", "::1", "fe80::1%en0", "198.51.100.1"} {
		if err := ProbeAcceptance(context.Background(), "127.0.0.1:1080", address); err == nil || err.Error() != "acceptance: invalid expected egress" {
			t.Fatalf("invalid expectation %q: %v", address, err)
		}
	}
}
