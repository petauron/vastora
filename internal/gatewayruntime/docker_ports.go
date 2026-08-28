package gatewayruntime

import (
	"fmt"
	"net/netip"
	"strconv"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/gateway"
)

// DockerPorts translates external listener sockets into the private ports
// owned by the bridged Caddy container. Public TCP 443 is omitted while the
// layer-4 frontend owns that socket; UDP 443 remains available for HTTP/3.
func DockerPorts(state gateway.DesiredState) (dockernetwork.PortSet, dockernetwork.PortMap, error) {
	exposed := dockernetwork.PortSet{}
	bindings := dockernetwork.PortMap{}
	for _, listener := range state.Listeners {
		address, err := netip.ParseAddr(listener.Address)
		if err != nil || !address.Unmap().Is4() {
			return nil, nil, fmt.Errorf("gateway runtime: invalid %s bind address %q", listener.Kind, listener.Address)
		}
		internalHTTP, internalHTTPS, ok := CaddyListenerPorts(listener.Kind)
		if !ok {
			return nil, nil, fmt.Errorf("gateway runtime: unsupported listener kind %q", listener.Kind)
		}
		mappings := []struct {
			containerPort int
			hostPort      int
			protocol      string
		}{
			{internalHTTP, listener.HTTPPort, "tcp"},
			{internalHTTPS, listener.HTTPSPort, "udp"},
		}
		if listener.Kind != "public" || state.SharedHTTPS == nil {
			mappings = append(mappings, struct {
				containerPort int
				hostPort      int
				protocol      string
			}{internalHTTPS, listener.HTTPSPort, "tcp"})
		}
		for _, mapping := range mappings {
			port := dockernetwork.MustParsePort(strconv.Itoa(mapping.containerPort) + "/" + mapping.protocol)
			exposed[port] = struct{}{}
			bindings[port] = append(bindings[port], dockernetwork.PortBinding{HostIP: address.Unmap(), HostPort: strconv.Itoa(mapping.hostPort)})
		}
	}
	return exposed, bindings, nil
}
