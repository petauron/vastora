package agent

import "testing"

func TestLegacyRouteGrantConverged(t *testing.T) {
	for _, tc := range []struct {
		name      string
		phase     string
		taskPhase string
		want      bool
	}{
		{name: "confirmed activation", phase: "ready", taskPhase: "activate", want: true},
		{name: "prepared only", phase: "prepared", taskPhase: "prepare"},
		{name: "activation in progress", phase: "activating", taskPhase: "activate"},
		{name: "retired", phase: "revoked", taskPhase: "retire"},
		{name: "unwritten active phase", phase: "active", taskPhase: "activate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := landingControllerGrant{Phase: tc.phase}
			grant.Task.Phase = tc.taskPhase
			if got := legacyRouteGrantConverged(grant); got != tc.want {
				t.Fatalf("legacyRouteGrantConverged() = %t, want %t", got, tc.want)
			}
		})
	}
}
