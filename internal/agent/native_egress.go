package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/moby/moby/client"
	"net"
	"os"
	"time"

	"github.com/petauron/meridian"
	"github.com/petauron/vastora/internal/landing"
	"github.com/petauron/vastora/internal/meridianruntime"
)

func (e ApplicationExecutor) verifyMeridianEgressClient(ctx context.Context, c meridianruntime.AcceptanceClient, image string, policy meridian.EgressPolicy) (string, error) {
	var exit string
	err := e.runMeridianClient(ctx, c, image, func(ctx context.Context, endpoint string) error {
		var err error
		exit, err = (landing.Probe{TCPOnly: true}).CheckClientTCP(ctx, endpoint)
		if err != nil {
			return errors.New("agent: native egress verification failed; check the runtime's addresses, routes and DNS")
		}
		if !meridianruntime.MatchesEgress(policy, exit) {
			return errors.New("agent: native egress returned the wrong address family")
		}
		return nil
	})
	return exit, err
}

// Preflight must inspect the actual currently running host runtime namespace.
// It never enables IPv6 or changes Docker networking. The real protocol probe
// after apply is separate: successful direct preflight alone is not acceptance.
func (e ApplicationExecutor) checkNativeEgressNetwork(parent context.Context, task meridianruntime.Task, state meridianRuntimeState) error {
	socket := e.DockerSocket
	if socket == "" {
		socket = "unix:///var/run/docker.sock"
	}
	docker, err := client.New(client.WithHost(socket))
	if err != nil {
		return err
	}
	defer docker.Close()
	inspected, err := docker.ContainerInspect(parent, meridianXrayContainer, client.ContainerInspectOptions{})
	if err != nil || state.Applied == nil || verifyMeridianHostContainer(inspected, state.AppliedGID, state.Applied) != nil || inspected.Container.State == nil || !inspected.Container.State.Running {
		return errors.New("agent: an intact running Meridian host runtime is required for egress preflight")
	}
	current, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return err
	}
	actual, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", inspected.Container.State.Pid))
	if err != nil || actual != current {
		return errors.New("agent: cannot inspect the actual Meridian network namespace; no host or Docker network settings were changed")
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	family, network := "ip4", "tcp4"
	if task.NativeEgress == meridian.EgressIPv6Only {
		family, network = "ip6", "tcp6"
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, family, "www.cloudflare.com")
	if err != nil || len(addresses) == 0 {
		return errors.New("agent: selected egress family has no usable DNS result")
	}
	var target string
	for _, ip := range addresses {
		if meridianruntime.MatchesEgress(task.NativeEgress, ip.String()) {
			target = net.JoinHostPort(ip.String(), "443")
			break
		}
	}
	if target == "" {
		return errors.New("agent: selected egress family has no public DNS result")
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, target)
	if err != nil {
		return errors.New("agent: selected egress family is unreachable in the Meridian runtime network; check address and route")
	}
	return conn.Close()
}
