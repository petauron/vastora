package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
	"github.com/petauron/vastora/internal/gateway"
	"github.com/petauron/vastora/internal/gatewayruntime"
	"github.com/petauron/vastora/internal/landing"
)

const privateBootstrapLabel = "lo:vstboot"

type privateBootstrap struct {
	Address    string `json:"address"`
	GatewayID  string `json:"gatewayId"`
	ConfigHash string `json:"configHash"`
	BootID     string `json:"bootId"`
	path       string
	run        func(context.Context, ...string) ([]byte, error)
	owned      bool
}

type bootstrapInterface struct {
	Name      string `json:"ifname"`
	Addresses []struct {
		Local string `json:"local"`
		Label string `json:"label"`
	} `json:"addr_info"`
}

// Only the enrolled, co-located Center may bootstrap its already committed
// private listener. Public nodes never get an address from a Docker label.
func (s *Store) beginCohostPrivateBootstrap(ctx context.Context, docker gatewayAttachmentEngine) (*privateBootstrap, error) {
	connection, err := s.Connection(ctx)
	if err != nil {
		return nil, err
	}
	local, err := s.LocalCenterChannel(connection.CenterURL)
	if err != nil {
		return nil, err
	}
	if !local || !loopbackCenterURL(connection.CenterURL) {
		return nil, nil
	}
	current, err := docker.ContainerInspect(ctx, defaultCaddyContainer, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := current.Container
	if v.Config == nil || v.HostConfig == nil || v.State == nil ||
		v.Config.Labels[gatewayruntime.ManagedLabel] != "true" ||
		v.Config.Labels[gatewayruntime.ComponentLabel] != gatewayruntime.CaddyComponentLabel ||
		v.Config.Labels[gatewayruntime.SystemServicesLabel] != gatewayruntime.SystemServices ||
		(!v.State.Running && !gatewayBindStartFailed(current)) {
		return nil, nil
	}
	if v.State.Paused || v.State.Restarting || v.State.Dead {
		return nil, nil
	}
	if v.ID == "" || string(v.HostConfig.NetworkMode) != "vastora-runtime" {
		return nil, errors.New("agent: private bootstrap gateway network changed")
	}
	if v.NetworkSettings != nil {
		for name := range v.NetworkSettings.Networks {
			if name != "vastora-runtime" {
				return nil, errors.New("agent: private bootstrap gateway has an additional network")
			}
		}
	}
	state, err := s.GatewayState(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateProtectedSystemRoutes(state.Desired, []string{"center", "headscale"}); err != nil {
		return nil, err
	}
	hash, err := gateway.ConfigurationHash(state.Desired, state.Certificates)
	if err != nil || hash != state.ConfigHash {
		return nil, errors.New("agent: committed system gateway identity is inconsistent")
	}
	address, err := cohostBootstrapAddress(state.Desired, current)
	if err != nil || address == "" {
		return nil, err
	}
	if runtime.GOOS != "linux" {
		return nil, errors.New("agent: private control bootstrap requires Linux iproute2")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		return nil, errors.New("agent: private control bootstrap requires iproute2 (ip)")
	}
	if err := gatewayruntime.PrepareAdminDirectory(gatewayruntime.CaddyAdminSocket); err != nil {
		return nil, err
	}
	b := &privateBootstrap{Address: address, GatewayID: v.ID, ConfigHash: hash,
		path: filepath.Join(filepath.Dir(gatewayruntime.CaddyAdminSocket), "private-bootstrap.json"),
		run: func(ctx context.Context, args ...string) ([]byte, error) {
			bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return exec.CommandContext(bounded, "ip", args...).Output()
		}}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	b.BootID = strings.TrimSpace(string(boot))
	interfaces, err := b.interfaces(ctx)
	if err != nil {
		return nil, err
	}
	if bootstrapAddressOn(interfaces, "tailscale0", address, "") {
		return nil, nil
	}
	if err := b.acquire(ctx, interfaces); err != nil {
		return nil, errors.Join(err, b.close(context.WithoutCancel(ctx)))
	}
	return b, nil
}

func cohostBootstrapAddress(state gateway.DesiredState, current client.ContainerInspectResult) (string, error) {
	var address string
	for _, listener := range state.Listeners {
		if listener.Kind != "headscale" {
			continue
		}
		ip, err := netip.ParseAddr(listener.Address)
		if err != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(ip) || address != "" && address != ip.String() {
			return "", errors.New("agent: ambiguous private control bootstrap identity")
		}
		address = ip.String()
	}
	if address == "" {
		return "", nil
	}
	if current.Container.HostConfig == nil {
		return "", errors.New("agent: system gateway has no declared bindings")
	}
	for _, bindings := range current.Container.HostConfig.PortBindings {
		for _, bind := range bindings {
			if bind.HostIP.String() == address {
				return address, nil
			}
		}
	}
	return "", errors.New("agent: private control bootstrap differs from the installed gateway")
}

func (b *privateBootstrap) interfaces(ctx context.Context) ([]bootstrapInterface, error) {
	raw, err := b.run(ctx, "-j", "address", "show")
	var interfaces []bootstrapInterface
	if err != nil || json.Unmarshal(raw, &interfaces) != nil {
		return nil, errors.New("agent: cannot inspect private bootstrap addresses")
	}
	return interfaces, nil
}

func bootstrapAddressOn(interfaces []bootstrapInterface, name, address, label string) bool {
	for _, i := range interfaces {
		for _, a := range i.Addresses {
			if i.Name == name && a.Local == address && (label == "" || a.Label == label) {
				return true
			}
		}
	}
	return false
}

func (b *privateBootstrap) acquire(ctx context.Context, interfaces []bootstrapInterface) error {
	var existing privateBootstrap
	info, err := os.Lstat(b.path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 4096 {
			return errors.New("agent: unsafe private bootstrap receipt")
		}
		raw, err := os.ReadFile(b.path)
		if err != nil || json.Unmarshal(raw, &existing) != nil || existing.Address != b.Address || existing.GatewayID != b.GatewayID || existing.ConfigHash != b.ConfigHash || existing.BootID != b.BootID {
			return errors.New("agent: private bootstrap receipt identity changed")
		}
		b.owned = true
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, i := range interfaces {
		for _, a := range i.Addresses {
			if a.Local == b.Address {
				if err == nil && i.Name == "lo" && a.Label == privateBootstrapLabel {
					return nil
				}
				return errors.New("agent: refusing to adopt an existing private bootstrap address")
			}
		}
	}
	raw, _ := json.Marshal(b)
	if err := writeHostFileAtomic(b.path, raw, 0o600); err != nil {
		return err
	}
	b.owned = true
	_, addErr := b.run(ctx, "address", "add", b.Address+"/32", "dev", "lo", "label", privateBootstrapLabel)
	after, err := b.interfaces(ctx)
	if err != nil || !bootstrapAddressOn(after, "lo", b.Address, privateBootstrapLabel) {
		return errors.Join(errors.New("agent: private bootstrap address was not prepared"), addErr, err)
	}
	return nil
}

func (b *privateBootstrap) waitForTailscale(ctx context.Context, checker interface {
	SelfIdentity(context.Context, string) (landing.PeerIdentity, error)
}) error {
	if b == nil {
		return nil
	}
	if checker == nil {
		return errors.New("agent: private control identity checker is unavailable")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if _, err := checker.SelfIdentity(ctx, b.Address); err == nil {
			interfaces, err := b.interfaces(ctx)
			if err == nil && bootstrapAddressOn(interfaces, "tailscale0", b.Address, "") {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent: waiting for original private control identity: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (b *privateBootstrap) close(ctx context.Context) error {
	if b == nil || !b.owned {
		return nil
	}
	interfaces, err := b.interfaces(ctx)
	if err != nil {
		return err
	}
	for _, i := range interfaces {
		for _, a := range i.Addresses {
			if i.Name == "lo" && a.Local == b.Address && a.Label != privateBootstrapLabel {
				return errors.New("agent: private bootstrap address ownership changed")
			}
		}
	}
	if bootstrapAddressOn(interfaces, "lo", b.Address, privateBootstrapLabel) {
		_, removeErr := b.run(ctx, "address", "del", b.Address+"/32", "dev", "lo", "label", privateBootstrapLabel)
		after, err := b.interfaces(ctx)
		if err != nil || bootstrapAddressOn(after, "lo", b.Address, privateBootstrapLabel) {
			return errors.Join(errors.New("agent: private bootstrap address cleanup failed"), removeErr, err)
		}
	}
	if err := os.Remove(b.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	b.owned = false
	return nil
}
