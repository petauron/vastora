package meridianruntime

import (
	"github.com/petauron/meridian"
	"testing"
	"time"
)

func TestNativeEgressReceiptRequiresFreshMatchingFamily(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	task := Task{NativeEgress: meridian.EgressIPv6Only, EgressClients: []AcceptanceClient{acceptanceFixture()}, Desired: meridian.DesiredArtifact{ConfigSHA256: "digest"}}
	valid := EgressObservation{Policy: task.NativeEgress, ConfigSHA256: "digest", Exits: []string{"2001:4860:4860::8888"}, CheckedAt: now}
	if err := (Result{Egress: &valid}).VerifyEgress(task, now); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*EgressObservation){
		func(o *EgressObservation) { o.Policy = meridian.EgressAuto },
		func(o *EgressObservation) { o.ConfigSHA256 = "other" },
		func(o *EgressObservation) { o.Exits = nil },
		func(o *EgressObservation) { o.Exits = []string{"1.1.1.1"} },
		func(o *EgressObservation) { o.Exits = []string{"::ffff:1.1.1.1"} },
		func(o *EgressObservation) { o.Exits = []string{"::1"} },
		func(o *EgressObservation) { o.CheckedAt = now.Add(-6 * time.Minute) },
		func(o *EgressObservation) { o.CheckedAt = now.Add(2 * time.Minute) },
	} {
		invalid := valid
		mutate(&invalid)
		if (Result{Egress: &invalid}).VerifyEgress(task, now) == nil {
			t.Fatalf("accepted invalid receipt: %#v", invalid)
		}
	}
	if (Result{}).VerifyEgress(task, now) == nil {
		t.Fatal("accepted missing receipt")
	}
	task.EgressClients = nil
	if err := (Result{}).VerifyEgress(task, now); err != nil {
		t.Fatal("ordinary credential mutation required a new probe", err)
	}
}

func TestNativeEgressTaskRequiresCapableCoreAndNativeIdentity(t *testing.T) {
	task := Task{NativeEgress: meridian.EgressIPv4Only, ImageReference: EgressImage, EgressClients: []AcceptanceClient{acceptanceFixture()}}
	if err := task.validateEgress(); err != nil {
		t.Fatal(err)
	}
	task.ImageReference = "ghcr.io/xtls/xray-core:26.7.28"
	if task.validateEgress() == nil {
		t.Fatal("old core accepted")
	}
	task.ImageReference = EgressImage
	task.EgressClients = append(task.EgressClients, task.EgressClients[0])
	if task.validateEgress() == nil {
		t.Fatal("duplicate protocol accepted")
	}
	task.EgressClients = task.EgressClients[:1]
	task.EgressClients[0].Material.Credential.Kind = meridian.RouteCredential
	if task.validateEgress() == nil {
		t.Fatal("remote landing credential accepted")
	}
	task.EgressClients = nil
	task.NativeEgress = meridian.EgressPolicy("prefer_ipv6")
	if task.validateEgress() == nil {
		t.Fatal("unimplemented fallback mode accepted")
	}
}
