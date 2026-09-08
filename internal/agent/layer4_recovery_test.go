package agent

import (
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/gateway"
)

func TestHAProxyMatchingRuntimeCanBeRetained(t *testing.T) {
	configuration := []byte("global\n  maxconn 4096\n")
	expected := haproxyContainerCreateOptions(
		DockerLayer4Provisioner{Image: DefaultHAProxyImage, Container: defaultHAProxyContainer},
		gateway.SharedHTTPS{Address: "203.0.113.10", Port: 443, RejectUnmatched: true}, configuration, haproxyConfigurationHash(configuration),
	)
	current := client.ContainerInspectResult{Container: container.InspectResponse{Config: expected.Config, HostConfig: expected.HostConfig}}
	if validateHAProxyOwnership(current) != nil || !haproxyRuntimeMatches(current, expected) {
		t.Fatal("unchanged managed runtime requires replacement")
	}
	config := *expected.Config
	current.Container.Config = &config
	config.Image = "different-image"
	if haproxyRuntimeMatches(current, expected) {
		t.Fatal("changed image was retained")
	}
}
