package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/moby/moby/api/types/network"
)

// network-online.target does not guarantee a particular private address exists.
// Wait without changing the requested bind. The outer startup/task recovery loop
// retries after this bounded attempt if tailscaled is still unavailable.
func waitForBindAddress(ctx context.Context, address string) error {
	ip, err := netip.ParseAddr(address)
	if err != nil || ip.IsUnspecified() || ip.IsMulticast() {
		return errors.New("agent: refusing an invalid or wildcard service bind address")
	}
	ready, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		addresses, err := net.InterfaceAddrs()
		if err != nil {
			return fmt.Errorf("agent: inspect service bind addresses: %w", err)
		}
		for _, address := range addresses {
			prefix, err := netip.ParsePrefix(address.String())
			if err == nil && prefix.Addr().Unmap() == ip.Unmap() {
				return nil
			}
		}
		select {
		case <-ready.Done():
			return fmt.Errorf("agent: private service address is not ready; recovery will retry: %w", ready.Err())
		case <-ticker.C:
		}
	}
}

// Public ingress may intentionally bind all interfaces. Only wait for explicit
// addresses; application private binds are validated separately and never relaxed.
func waitForPublishedAddresses(ctx context.Context, ports network.PortMap) error {
	seen := map[netip.Addr]bool{}
	for _, bindings := range ports {
		for _, binding := range bindings {
			if !binding.HostIP.IsValid() || binding.HostIP.IsUnspecified() || seen[binding.HostIP] {
				continue
			}
			seen[binding.HostIP] = true
			if err := waitForBindAddress(ctx, binding.HostIP.String()); err != nil {
				return err
			}
		}
	}
	return nil
}
