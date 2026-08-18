// Package gateway contains Vastora's proxy-independent desired-state model.
package gateway

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

type Upstream struct {
	Address string `json:"address"`
	Port    int    `json:"port"`
}

type Listener struct {
	Kind      string `json:"kind"`
	Address   string `json:"address"`
	HTTPPort  int    `json:"httpPort"`
	HTTPSPort int    `json:"httpsPort"`
}

type Route struct {
	ID           string     `json:"id"`
	Hostname     string     `json:"hostname"`
	Protocol     string     `json:"protocol"`
	Upstreams    []Upstream `json:"upstreams"`
	TLSEnabled   bool       `json:"tlsEnabled"`
	ListenerKind string     `json:"listenerKind"`
}

// Layer4Route is a raw TCP upstream selected from the TLS ClientHello SNI.
// TLS remains end-to-end; HAProxy never terminates certificates.
type Layer4Route struct {
	ID        string     `json:"id"`
	Hostname  string     `json:"hostname"`
	Upstreams []Upstream `json:"upstreams"`
}

// SharedHTTPS describes the optional public TCP frontend that owns port 443.
// Unknown and Web SNI values are passed through to Caddy, which remains the
// HTTPS endpoint and certificate manager.
type SharedHTTPS struct {
	Address      string        `json:"address"`
	Port         int           `json:"port"`
	CaddyAddress string        `json:"caddyAddress"`
	CaddyPort    int           `json:"caddyPort"`
	Routes       []Layer4Route `json:"routes"`
}

type DesiredState struct {
	Revision    int64        `json:"revision"`
	Listeners   []Listener   `json:"listeners"`
	Routes      []Route      `json:"routes"`
	SharedHTTPS *SharedHTTPS `json:"sharedHttps,omitempty"`
}

func (state DesiredState) Validate() error {
	if state.Revision < 1 {
		return errors.New("gateway: revision must be positive")
	}
	seenIDs := make(map[string]struct{}, len(state.Routes))
	seenHosts := make(map[string]struct{}, len(state.Routes))
	listeners := make(map[string]Listener, len(state.Listeners))
	for _, listener := range state.Listeners {
		if listener.Kind != "lan" && listener.Kind != "headscale" && listener.Kind != "public" {
			return fmt.Errorf("gateway: unsupported listener kind %q", listener.Kind)
		}
		if _, exists := listeners[listener.Kind]; exists {
			return fmt.Errorf("gateway: duplicate listener kind %q", listener.Kind)
		}
		if net.ParseIP(listener.Address) == nil || listener.HTTPPort < 1 || listener.HTTPPort > 65535 || listener.HTTPSPort < 1 || listener.HTTPSPort > 65535 {
			return fmt.Errorf("gateway: invalid %s listener", listener.Kind)
		}
		listeners[listener.Kind] = listener
	}
	for _, route := range state.Routes {
		if strings.TrimSpace(route.ID) == "" {
			return errors.New("gateway: route id is required")
		}
		if _, exists := seenIDs[route.ID]; exists {
			return fmt.Errorf("gateway: duplicate route id %q", route.ID)
		}
		seenIDs[route.ID] = struct{}{}
		if !hostnamePattern.MatchString(route.Hostname) {
			return fmt.Errorf("gateway: invalid hostname %q", route.Hostname)
		}
		hostKey := route.ListenerKind + "\x00" + route.Hostname
		if _, exists := seenHosts[hostKey]; exists {
			return fmt.Errorf("gateway: duplicate hostname %q", route.Hostname)
		}
		seenHosts[hostKey] = struct{}{}
		if _, exists := listeners[route.ListenerKind]; !exists {
			return fmt.Errorf("gateway: route %q references an unavailable listener", route.ID)
		}
		if route.Protocol != "http" && route.Protocol != "https" {
			return fmt.Errorf("gateway: unsupported protocol %q", route.Protocol)
		}
		if len(route.Upstreams) == 0 {
			return fmt.Errorf("gateway: route %q requires an upstream", route.ID)
		}
		for _, upstream := range route.Upstreams {
			if net.ParseIP(upstream.Address) == nil || upstream.Port < 1 || upstream.Port > 65535 {
				return fmt.Errorf("gateway: route %q has an invalid upstream", route.ID)
			}
		}
	}
	if state.SharedHTTPS != nil {
		shared := state.SharedHTTPS
		if net.ParseIP(shared.Address) == nil || shared.Port < 1 || shared.Port > 65535 || !net.ParseIP(shared.CaddyAddress).IsLoopback() || shared.CaddyPort < 1 || shared.CaddyPort > 65535 {
			return errors.New("gateway: invalid shared HTTPS frontend")
		}
		if shared.Address == shared.CaddyAddress && shared.Port == shared.CaddyPort {
			return errors.New("gateway: shared HTTPS frontend and Caddy backend must use different sockets")
		}
		publicListener, exists := listeners["public"]
		if !exists || publicListener.Address != shared.Address || publicListener.HTTPSPort != shared.Port {
			return errors.New("gateway: shared HTTPS frontend requires the matching public listener")
		}
		webHosts := make(map[string]bool)
		for _, route := range state.Routes {
			if route.ListenerKind == "public" && route.TLSEnabled {
				webHosts[route.Hostname] = true
			}
		}
		seenLayer4 := make(map[string]bool, len(shared.Routes))
		for _, route := range shared.Routes {
			if strings.TrimSpace(route.ID) == "" || !hostnamePattern.MatchString(route.Hostname) || len(route.Upstreams) == 0 {
				return errors.New("gateway: invalid shared HTTPS route")
			}
			if seenLayer4[route.Hostname] || webHosts[route.Hostname] {
				return fmt.Errorf("gateway: duplicate shared HTTPS hostname %q", route.Hostname)
			}
			seenLayer4[route.Hostname] = true
			for _, upstream := range route.Upstreams {
				if net.ParseIP(upstream.Address) == nil || upstream.Port < 1 || upstream.Port > 65535 {
					return fmt.Errorf("gateway: shared HTTPS route %q has an invalid upstream", route.ID)
				}
			}
		}
	}
	return nil
}

func (state DesiredState) Sorted() DesiredState {
	result := DesiredState{Revision: state.Revision, Listeners: append([]Listener(nil), state.Listeners...), Routes: append([]Route(nil), state.Routes...)}
	if state.SharedHTTPS != nil {
		shared := *state.SharedHTTPS
		shared.Routes = append([]Layer4Route(nil), state.SharedHTTPS.Routes...)
		for index := range shared.Routes {
			shared.Routes[index].Upstreams = append([]Upstream(nil), shared.Routes[index].Upstreams...)
			sort.Slice(shared.Routes[index].Upstreams, func(left, right int) bool {
				first, second := shared.Routes[index].Upstreams[left], shared.Routes[index].Upstreams[right]
				if first.Address == second.Address {
					return first.Port < second.Port
				}
				return first.Address < second.Address
			})
		}
		sort.Slice(shared.Routes, func(left, right int) bool { return shared.Routes[left].ID < shared.Routes[right].ID })
		result.SharedHTTPS = &shared
	}
	sort.Slice(result.Listeners, func(left, right int) bool { return result.Listeners[left].Kind < result.Listeners[right].Kind })
	for index := range result.Routes {
		result.Routes[index].Upstreams = append([]Upstream(nil), result.Routes[index].Upstreams...)
		sort.Slice(result.Routes[index].Upstreams, func(left, right int) bool {
			first := result.Routes[index].Upstreams[left]
			second := result.Routes[index].Upstreams[right]
			if first.Address == second.Address {
				return first.Port < second.Port
			}
			return first.Address < second.Address
		})
	}
	sort.Slice(result.Routes, func(left, right int) bool { return result.Routes[left].ID < result.Routes[right].ID })
	return result
}
