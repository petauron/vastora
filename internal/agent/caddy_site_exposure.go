package agent

import (
	"errors"
	"github.com/petauron/vastora/internal/gateway"
	"net"
	"strconv"
)

func delegatedWebsiteSockets(desired gateway.DesiredState, sites []CaddySite) (map[string]bool, error) {
	sockets := map[string]bool{}
	delegated := []CaddySite{}
	for _, site := range sites {
		if site.TLSLoopbackOnly {
			if err := site.validate(); err != nil {
				return nil, err
			}
			delegated = append(delegated, site)
			if site.PublicListen != "" {
				sockets[site.PublicListen] = true
			}
		}
	}
	if len(sockets) == 0 {
		return sockets, nil
	}
	if desired.SharedHTTPS != nil {
		return nil, errors.New("agent: release the separately owned shared frontend before delegating website TLS")
	}
	// The gateway may serve a different public address. Delegate only the
	// exact registered socket; sharing an upstream is not hostname ownership.
	for _, listener := range desired.Listeners {
		socket := net.JoinHostPort(listener.Address, strconv.Itoa(listener.HTTPSPort))
		if listener.Kind != "public" || !sockets[socket] {
			continue
		}
		for _, route := range desired.Routes {
			if route.ListenerKind != "public" || !route.TLSEnabled {
				continue
			}
			owned := false
			for _, site := range delegated {
				owned = owned || site.PublicListen == socket && route.Hostname == site.Hostname && !route.System && len(route.Upstreams) == 1 && route.Upstreams[0].Address == site.Address && route.Upstreams[0].Port == site.HTTPPort
			}
			if !owned {
				return nil, errors.New("agent: another website still owns this public TLS listener")
			}
		}
	}
	for _, site := range sites {
		if !site.TLSLoopbackOnly && site.PublicListen != "" && sockets[site.PublicListen] {
			return nil, errors.New("agent: public TLS socket is shared by another website")
		}
	}
	return sockets, nil
}
func removeDelegatedCaddyPublicServers(servers map[string]any, desired gateway.DesiredState, sites []CaddySite, container bool) error {
	sockets, err := delegatedWebsiteSockets(desired, sites)
	if err != nil {
		return err
	}
	if len(sockets) == 0 {
		return nil
	}
	gatewayDelegated := false
	for _, listener := range desired.Listeners {
		gatewayDelegated = gatewayDelegated || listener.Kind == "public" && sockets[net.JoinHostPort(listener.Address, strconv.Itoa(listener.HTTPSPort))]
	}
	for name, value := range servers {
		server, _ := value.(map[string]any)
		listen := caddyStringSlice(server["listen"])
		if len(listen) != 1 {
			continue
		}
		matched := sockets[listen[0]] || container && gatewayDelegated && listen[0] == "0.0.0.0:443"
		if matched {
			delete(servers, name)
		}
	}
	return nil
}
