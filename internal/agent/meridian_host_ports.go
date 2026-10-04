package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/moby/moby/client"
	"github.com/petauron/meridian"
)

// Check before stopping the previous container. An occupied socket is allowed
// only when it belongs to that verified runtime and will be released by stop.
func validateMeridianHostPorts(ctx context.Context, docker *client.Client, artifact meridian.DesiredArtifact, state meridianRuntimeState) error {
	previous, _, exists, err := inspectCurrentMeridianRuntime(ctx, docker)
	if err != nil {
		return err
	}
	if exists && state.AppliedGID != 0 {
		if err := verifyMeridianHostContainer(previous, state.AppliedGID, state.Applied); err != nil {
			return err
		}
	}
	var config struct {
		Inbounds []struct {
			Protocol, Listen string
			Port             int
		}
	}
	if json.Unmarshal(artifact.Config, &config) != nil {
		return errors.New("agent: invalid host listener configuration")
	}
	for _, inbound := range config.Inbounds {
		network := "tcp4"
		if inbound.Protocol == "hysteria" {
			network = "udp4"
		}
		address := net.JoinHostPort(inbound.Listen, strconv.Itoa(inbound.Port))
		var bindErr error
		if network == "tcp4" {
			socket, err := net.Listen(network, address)
			bindErr = err
			if err == nil {
				_ = socket.Close()
			}
		} else {
			socket, err := net.ListenPacket(network, address)
			bindErr = err
			if err == nil {
				_ = socket.Close()
			}
		}
		if bindErr == nil {
			continue
		}
		owned := false
		if errors.Is(bindErr, syscall.EADDRINUSE) && exists && previous.Container.State != nil && previous.Container.State.Running {
			if state.AppliedGID != 0 {
				owned = meridianHostPortOwned(previous.Container.State.Pid, network[:3], inbound.Port)
			} else if network == "udp4" && inbound.Port == 443 && previous.Container.HostConfig != nil {
				// One-time bridge migration: Docker owns the old public UDP mapping.
				for port, bindings := range previous.Container.HostConfig.PortBindings {
					if port.String() != "443/udp" {
						continue
					}
					for _, binding := range bindings {
						if binding.HostPort == "443" {
							owned = true
						}
					}
				}
			}
		}
		if !owned {
			return fmt.Errorf("agent: Meridian %s listener %s is unavailable", network, address)
		}
	}
	return nil
}

func meridianHostPortOwned(pid int, protocol string, port int) bool {
	if pid <= 0 || protocol != "tcp" && protocol != "udp" {
		return false
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return false
	}
	inodes := map[string]bool{}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(fmt.Sprintf("/proc/%d/fd", pid), fd.Name()))
		if err == nil && strings.HasPrefix(target, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = true
		}
	}
	found := false
	for _, family := range []string{protocol, protocol + "6"} {
		data, err := os.ReadFile("/proc/net/" + family)
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || protocol == "tcp" && fields[3] != "0A" {
				continue
			}
			_, encoded, ok := strings.Cut(fields[1], ":")
			parsed, err := strconv.ParseUint(encoded, 16, 16)
			if !ok || err != nil || int(parsed) != port {
				continue
			}
			if !inodes[fields[9]] {
				return false
			}
			found = true
		}
	}
	return found
}
