package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

type historicalVolumeEngine struct {
	packageDockerEngine
	volume volume.Volume
}

func (e historicalVolumeEngine) VolumeInspect(context.Context, string, client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	return client.VolumeInspectResult{Volume: e.volume}, nil
}

func TestHistoricalXrayVolumeIdentity(t *testing.T) {
	name := strings.Repeat("a", 64)
	resource := RuntimeResource{Kind: "retained-volume", Name: name, Path: "/var/log/xray", VolumeCreatedAt: "2026-01-01T00:00:00Z", Persistent: true}
	task := DeploymentTask{AppKey: meridianKey}
	valid := func() volume.Volume {
		return volume.Volume{Name: name, Driver: "local", Scope: "local", CreatedAt: resource.VolumeCreatedAt, Labels: map[string]string{"com.docker.volume.anonymous": ""}}
	}
	if err := inspectHistoricalXrayVolume(context.Background(), historicalVolumeEngine{volume: valid()}, task, resource); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name   string
		change func(*volume.Volume)
	}{
		{"recreated", func(v *volume.Volume) { v.CreatedAt = "2026-02-01T00:00:00Z" }},
		{"named", func(v *volume.Volume) { v.Labels = nil }},
		{"foreign-owned", func(v *volume.Volume) { v.Labels["owner"] = "other" }},
		{"remote", func(v *volume.Volume) { v.Driver = "nfs" }},
		{"host-bind", func(v *volume.Volume) { v.Options = map[string]string{"device": "/"} }},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			v := valid()
			scenario.change(&v)
			if err := inspectHistoricalXrayVolume(context.Background(), historicalVolumeEngine{volume: v}, task, resource); err == nil {
				t.Fatal("accepted changed historical volume")
			}
		})
	}
}
