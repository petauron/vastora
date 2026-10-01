package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strconv"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"github.com/petauron/meridian"
)

// A dedicated primary group scopes host sockets without also gating Agent
// probes. The runtime has no SETGID capability and cannot leave this group.
func ensureMeridianRuntimeGroup(ctx context.Context) (uint32, error) {
	if os.Geteuid() != 0 {
		return 0, errors.New("agent: Meridian host runtime requires a root Agent")
	}
	group, err := user.LookupGroup("vastora-meridian")
	if _, missing := err.(user.UnknownGroupError); missing {
		if err := exec.CommandContext(ctx, "groupadd", "--system", "vastora-meridian").Run(); err != nil {
			return 0, errors.New("agent: cannot create Meridian runtime group")
		}
		group, err = user.LookupGroup("vastora-meridian")
	}
	if err != nil {
		return 0, errors.New("agent: Meridian runtime group is unavailable")
	}
	gid, err := strconv.ParseUint(group.Gid, 10, 32)
	if err != nil || gid == 0 || gid == 65534 || gid == uint64(os.Getegid()) {
		return 0, errors.New("agent: invalid Meridian runtime group")
	}
	return uint32(gid), nil
}

// Configuration checks happen before touching the applied runtime. Binding to
// a missing private address must not silently become a wildcard listener.
func validateMeridianHostListeners(artifact meridian.DesiredArtifact) error {
	var config struct {
		Inbounds []struct {
			Protocol string
			Listen   string
			Port     int
			Tag      string
		}
	}
	if artifact.Validate() != nil || json.Unmarshal(artifact.Config, &config) != nil || len(config.Inbounds) < 2 {
		return errors.New("agent: invalid Meridian host listeners")
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return errors.New("agent: cannot inspect Meridian private address")
	}
	local := map[string]bool{}
	for _, address := range addresses {
		if prefix, err := netip.ParsePrefix(address.String()); err == nil {
			local[prefix.Addr().String()] = true
		}
	}
	seen := map[string]bool{}
	for _, inbound := range config.Inbounds {
		if seen[inbound.Protocol] {
			return errors.New("agent: duplicate Meridian host listener")
		}
		seen[inbound.Protocol] = true
		switch inbound.Protocol {
		case "dokodemo-door":
			if inbound.Tag != "api" || inbound.Listen != "127.0.0.1" || inbound.Port != meridian.XrayAPIListenPort {
				return errors.New("agent: Meridian API must stay on loopback")
			}
		case "vless":
			address, err := netip.ParseAddr(inbound.Listen)
			if err != nil || !address.Is4() || !(address.IsPrivate() || netip.MustParsePrefix("100.64.0.0/10").Contains(address)) || !local[inbound.Listen] || inbound.Port != meridian.RealityBackendPort {
				return errors.New("agent: Meridian private backend address is missing or invalid")
			}
		case "hysteria":
			if inbound.Listen != "0.0.0.0" || inbound.Port != 443 {
				return errors.New("agent: invalid Meridian public UDP listener")
			}
		default:
			return errors.New("agent: unsupported Meridian host listener")
		}
	}
	if !seen["dokodemo-door"] {
		return errors.New("agent: missing Meridian loopback API")
	}
	return nil
}

func verifyMeridianHostContainer(inspected client.ContainerInspectResult, gid uint32, artifact *meridian.DesiredArtifact) error {
	c, h := inspected.Container.Config, inspected.Container.HostConfig
	if c == nil || h == nil || gid == 0 || c.User != "0:"+strconv.FormatUint(uint64(gid), 10) || h.NetworkMode != "host" || h.UsernsMode != "host" || h.RestartPolicy.Name != "no" || !h.ReadonlyRootfs || h.Privileged || len(h.PortBindings) != 0 || len(h.Sysctls) != 0 || len(h.GroupAdd) != 0 || h.PidMode != "" || h.IpcMode.IsHost() || !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Contains(h.SecurityOpt, "no-new-privileges:true") {
		return errors.New("agent: Meridian host runtime security settings changed")
	}
	hy2 := false
	if artifact != nil {
		var err error
		hy2, err = meridianArtifactHY2Enabled(*artifact)
		if err != nil {
			return err
		}
	}
	if hy2 && !slices.Equal(h.CapAdd, []string{"NET_BIND_SERVICE"}) || !hy2 && len(h.CapAdd) != 0 {
		return errors.New("agent: Meridian host capabilities changed")
	}
	if inspected.Container.NetworkSettings != nil {
		for name := range inspected.Container.NetworkSettings.Networks {
			if name != "host" {
				return errors.New("agent: Meridian host runtime has a bridge attachment")
			}
		}
	}
	return nil
}

func meridianAppliedRuntimeUID(state meridianRuntimeState) int {
	if state.AppliedGID != 0 {
		return 0
	}
	// Only retained bridge state reaches this migration boundary.
	return xrayWorkerRuntimeUID()
}

func meridianHostContainerOptions(task DeploymentTask, imageRef, configPath string, hy2 bool, gid uint32) client.ContainerCreateOptions {
	options := xrayWorkerContainerOptions(task, imageRef, configPath, false)
	options.Config.User = "0:" + strconv.FormatUint(uint64(gid), 10)
	options.Config.ExposedPorts = nil
	options.HostConfig.NetworkMode = "host"
	options.HostConfig.UsernsMode = "host"
	options.HostConfig.PortBindings = nil
	options.HostConfig.Sysctls = nil
	options.NetworkingConfig = nil
	// Docker does not retain bind capabilities for a numeric non-root user.
	// Root inside the isolated PID/mount namespaces gets only BIND_SERVICE;
	// its dedicated, immutable group identifies host sockets for lease gates.
	// HY2 alone needs a privileged listening port. Keep all other capabilities
	// dropped; do not change host sysctls to let every process bind low ports.
	if hy2 {
		options.HostConfig.CapAdd = []string{"NET_BIND_SERVICE"}
	}
	options.HostConfig.RestartPolicy = container.RestartPolicy{Name: "no"}
	return options
}
