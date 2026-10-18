package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/petauron/vastora/internal/meridianruntime"
)

func TestMeridianFirewallPreflightPreservesRuntimeJournal(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "new landing", true: "pending recovery"}[pending], func(t *testing.T) {
			ctx := context.Background()
			store := openMeridianLandingTestStore(t)
			state := meridianLandingFixture()
			task := meridianruntime.Task{ApplicationID: state.ApplicationID, ImageReference: state.ImageReference, Desired: *state.Applied, Peers: state.AppliedPeers, Source: state.AppliedSource}
			task.Desired.Revision++
			if pending {
				pendingArtifact := task.Desired
				state.Pending = &pendingArtifact
				state.PendingPeers = state.AppliedPeers
				state.PendingSource = state.AppliedSource
				state.HandoverPending = true
				task.Peers = nil
				task.Source = nil
				task.Desired = meridianRecoveryArtifact(9, `{"outbounds":[{"protocol":"freedom","tag":"direct"}]}`)
			}
			if err := task.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := store.saveMeridianRuntimeState(ctx, state); err != nil {
				t.Fatal(err)
			}
			before, err := store.loadMeridianRuntimeState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			receipt := &InstanceResources{Version: 1, ApplicationID: state.ApplicationID, AppKey: meridianKey, Runtime: "docker", State: "ready", Resources: []RuntimeResource{{Kind: "container", LogicalName: "xray", Name: meridianXrayContainer, ID: "fixed-container", SHA256: strings.Split(xrayWorkerImageReference, "@sha256:")[1], StartedAt: "2026-09-24T00:00:00Z", Component: "xray"}}}
			if err := (PackageExecutor{StateDirectory: store.dataDir}).save(receipt); err != nil {
				t.Fatal(err)
			}
			unsupported := errors.New("unsupported nftables")
			calls := 0
			_, err = (ApplicationExecutor{Store: store, DockerSocket: "unix:///nonexistent-firewall-preflight.sock"}).applyMeridianRuntime(ctx, task, func(context.Context) error { calls++; return unsupported })
			if !errors.Is(err, unsupported) || calls != 1 {
				t.Fatalf("preflight calls=%d error=%v", calls, err)
			}
			after, err := store.loadMeridianRuntimeState(ctx)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("preflight changed journal: %v", err)
			}
		})
	}
}
