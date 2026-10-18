package agent

import "testing"

func TestLandingOnlySecurityTaskRemainsTyped(t *testing.T) {
	if !landingOnlyTaskAllowed("node.security") {
		t.Fatal("landing-only nodes cannot execute their typed security task")
	}
	for _, kind := range []string{"shell", "node.security.shell", "application.apply"} {
		if landingOnlyTaskAllowed(kind) {
			t.Fatal("untyped/unsupported task admitted", kind)
		}
	}
}
