package main

import (
	"strings"
	"testing"
)

func TestLandingOnlyRuntimeAndSystemd(t *testing.T) {
	_, caps, err := validatedNodeRuntime("worker", "landing")
	if err != nil || !caps.LandingOnly || !caps.NativeEgress || caps.Docker {
		t.Fatalf("invalid landing runtime: %+v %v", caps, err)
	}
	if nodeCapabilitiesString(caps) != "landing" {
		t.Fatal("landing capability lost")
	}
	for _, value := range []string{"landing,docker", "landing,gateway", "landing,tunnel"} {
		if _, err := parseNodeCapabilities(value); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	unit := systemdAgentUnit("/usr/local/bin/vastora", "/var/lib/vastora/agent", "worker", "landing")
	if strings.Contains(unit, "docker") || !strings.Contains(unit, "tailscaled.service") {
		t.Fatal("landing unit depends on Docker")
	}
	if !strings.Contains(systemdAgentUnit("/usr/local/bin/vastora", "/var/lib/vastora/agent", "worker", "docker"), "docker.service") {
		t.Fatal("ordinary unit changed")
	}
}
