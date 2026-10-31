package agent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"strconv"

	dockernetwork "github.com/moby/moby/api/types/network"
	"github.com/petauron/vastora/internal/gateway"
)

func (provisioner DockerGatewayProvisioner) ReconcileWebsites(ctx context.Context, desired gateway.DesiredState, sites []CaddySite) error {
	if desired.Revision == 0 && len(sites) == 0 {
		settings, err := provisioner.settings()
		if err != nil {
			return err
		}
		docker, err := provisioner.client()
		if err != nil {
			return err
		}
		defer docker.Close()
		existing, err := inspectManagedCaddy(ctx, docker, settings.Container)
		if err != nil {
			return err
		}
		if existing == nil {
			return errSharedCaddyRuntimeAbsent
		}
		// Keep existing bindings until the combined empty configuration has
		// been loaded. Idle then removes only this owned, unused container.
		return provisioner.Ensure(ctx)
	}
	return provisioner.reconcile(ctx, &desired, sites)
}

func addWebsiteDockerPorts(exposed dockernetwork.PortSet, bindings dockernetwork.PortMap, desired gateway.DesiredState, sites []CaddySite) error {
	for _, site := range sites {
		if err := site.validate(); err != nil {
			return err
		}
		endpoints := []struct {
			address string
			port    int
			public  bool
		}{{site.Address, site.HTTPPort, false}, {"127.0.0.1", site.TLSPort, false}}
		if site.TLSPort != 0 && site.PublicListen != "" {
			address, _, _ := net.SplitHostPort(site.PublicListen)
			endpoints = append(endpoints, struct {
				address string
				port    int
				public  bool
			}{address, 443, true})
		}
		for _, endpoint := range endpoints {
			if endpoint.port == 0 {
				continue
			}
			// HAProxy already owns this public socket and forwards website SNI to
			// Caddy over the runtime network. Do not claim its host port twice.
			if endpoint.public && desired.SharedHTTPS != nil && desired.SharedHTTPS.Port == endpoint.port && desired.SharedHTTPS.Address == endpoint.address {
				continue
			}
			address, err := netip.ParseAddr(endpoint.address)
			if err != nil {
				return errors.New("agent: invalid website listener address")
			}
			port := dockernetwork.MustParsePort(strconv.Itoa(endpoint.port) + "/tcp")
			binding := dockernetwork.PortBinding{HostIP: address.Unmap(), HostPort: strconv.Itoa(endpoint.port)}
			for existingPort, values := range bindings {
				for _, existing := range values {
					if existing.HostIP == binding.HostIP && existing.HostPort == binding.HostPort && existingPort != port {
						return errors.New("agent: website conflicts with a gateway listener")
					}
				}
			}
			if previous := bindings[port]; len(previous) != 0 {
				found := false
				for _, value := range previous {
					if value == binding {
						found = true
					}
					if value.HostIP.IsUnspecified() {
						return errors.New("agent: website conflicts with a wildcard gateway binding")
					}
				}
				if !found {
					if !endpoint.public {
						return errors.New("agent: website cannot expand an existing gateway port binding")
					}
					bindings[port] = append(previous, binding)
					sort.Slice(bindings[port], func(i, j int) bool { return bindings[port][i].HostIP.String() < bindings[port][j].HostIP.String() })
				}
				continue
			}
			exposed[port] = struct{}{}
			bindings[port] = []dockernetwork.PortBinding{binding}
		}
	}
	return nil
}
