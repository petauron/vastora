package agent

import (
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestMeridianHostRuntimeSecurityAndDrift(t *testing.T) {
	task := DeploymentTask{ID: "host-test", AppKey: meridianKey, ApplicationID: "host-app"}
	options := meridianHostContainerOptions(task, xrayWorkerImageReference, "/tmp/meridian-host/config.json", false, 1001)
	if options.NetworkingConfig != nil || len(options.Config.ExposedPorts) != 0 || len(options.HostConfig.PortBindings) != 0 {
		t.Fatal("host runtime retained Docker NAT")
	}
	fixture := func() client.ContainerInspectResult {
		options := meridianHostContainerOptions(task, xrayWorkerImageReference, "/tmp/meridian-host/config.json", false, 1001)
		return client.ContainerInspectResult{Container: container.InspectResponse{Config: options.Config, HostConfig: options.HostConfig}}
	}
	if err := verifyMeridianHostContainer(fixture(), 1001, nil); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*client.ContainerInspectResult){
		func(v *client.ContainerInspectResult) { v.Container.Config.User = "0:0" },
		func(v *client.ContainerInspectResult) { v.Container.HostConfig.NetworkMode = "bridge" },
		func(v *client.ContainerInspectResult) { v.Container.HostConfig.PidMode = "host" },
		func(v *client.ContainerInspectResult) { v.Container.HostConfig.CapAdd = []string{"SETGID"} },
		func(v *client.ContainerInspectResult) {
			v.Container.HostConfig.CapAdd = []string{"CAP_NET_BIND_SERVICE"}
		},
		func(v *client.ContainerInspectResult) { v.Container.HostConfig.GroupAdd = []string{"0"} },
		func(v *client.ContainerInspectResult) { v.Container.HostConfig.ReadonlyRootfs = false },
		func(v *client.ContainerInspectResult) { v.Container.HostConfig.RestartPolicy.Name = "unless-stopped" },
	} {
		value := fixture()
		mutate(&value)
		if err := verifyMeridianHostContainer(value, 1001, nil); err == nil {
			t.Fatal("accepted runtime drift")
		}
	}
}

func TestMeridianHostHY2CanonicalCapabilities(t *testing.T) {
	artifact := meridianRecoveryArtifact(1, `{"inbounds":[{"protocol":"hysteria","port":443}]}`)
	task := DeploymentTask{ID: "host-hy2-test", AppKey: meridianKey, ApplicationID: "host-hy2-app"}
	options := meridianHostContainerOptions(task, xrayWorkerImageReference, "/tmp/meridian-host/config.json", true, 1001)
	if options.Config.User != "0:1001" || len(options.HostConfig.CapAdd) != 1 || options.HostConfig.CapAdd[0] != "CAP_NET_BIND_SERVICE" {
		t.Fatal("HY2 must request Docker's canonical bind capability")
	}
	for _, tc := range []struct {
		name      string
		caps      []string
		wantValid bool
	}{
		{"docker inspect", []string{"CAP_NET_BIND_SERVICE"}, true},
		{"missing", nil, false},
		{"noncanonical", []string{"NET_BIND_SERVICE"}, false},
		{"unrelated", []string{"CAP_SETGID"}, false},
		{"additional", []string{"CAP_NET_BIND_SERVICE", "CAP_SETGID"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inspected := client.ContainerInspectResult{Container: container.InspectResponse{Config: options.Config, HostConfig: options.HostConfig}}
			inspected.Container.HostConfig.CapAdd = tc.caps
			err := verifyMeridianHostContainer(inspected, 1001, &artifact)
			if (err == nil) != tc.wantValid {
				t.Fatalf("valid=%v err=%v", tc.wantValid, err)
			}
		})
	}
}

func TestMeridianHostGatesReplaceBridgeScope(t *testing.T) {
	state := meridianLandingFixture()
	state.AppliedGID = 1001
	gates := state.knownLandingGates()
	if len(gates) != 1 || gates[0].GID != 1001 || gates[0].Bridge != "" {
		t.Fatal("host journal retained bridge attachment")
	}
	if err := state.validateLanding(); err != nil {
		t.Fatal(err)
	}
}
