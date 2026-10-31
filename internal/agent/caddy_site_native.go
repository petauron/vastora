package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/petauron/vastora/internal/gateway"
)

const nativeCaddySocket = "/run/vastora-caddy/admin.sock"
const nativeCaddyBinary = "/opt/vastora/runtime/caddy/caddy"
const nativeCaddyBootstrap = "/etc/vastora/caddy/bootstrap.json"
const nativeCaddyUnitPath = "/etc/systemd/system/vastora-caddy.service"

const nativeCaddyUnit = `[Unit]
Description=Vastora shared Caddy
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
DynamicUser=yes
RuntimeDirectory=vastora-caddy
RuntimeDirectoryMode=0700
StateDirectory=vastora-caddy
StateDirectoryMode=0700
LoadCredential=bootstrap:/etc/vastora/caddy/bootstrap.json
ExecStart=/opt/vastora/runtime/caddy/caddy run --resume --config %d/bootstrap
Environment=XDG_CONFIG_HOME=/var/lib/vastora-caddy/config
Environment=XDG_DATA_HOME=/var/lib/vastora-caddy/data
Environment=GOMEMLIMIT=96MiB
Environment=GOMAXPROCS=1
Restart=on-failure
RestartSec=2
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6
UMask=0077
MemoryMax=192M
TasksMax=128

[Install]
WantedBy=multi-user.target
`

type nativeCaddyReceipt struct {
	Version         int    `json:"version"`
	SHA256          string `json:"sha256"`
	UnitSHA256      string `json:"unitSha256"`
	BootstrapSHA256 string `json:"bootstrapSha256"`
}

// NativeCaddySiteRuntime is used deliberately on native-only nodes. It does not
// install Docker and does not adopt an existing service. Caddy is copied from a
// receipt-verified signed website bundle into platform-owned runtime storage so
// uninstalling its first website cannot remove the shared binary.
type NativeCaddySiteRuntime struct {
	StateDirectory string
	Manager        SystemdHostApplicationManager
}

func sha256String(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func (runtime NativeCaddySiteRuntime) Idle(ctx context.Context) error {
	path := filepath.Join(runtime.StateDirectory, "shared-caddy", "runtime.json")
	if err := checkPackageParents(path); err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var receipt nativeCaddyReceipt
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || receipt.UnitSHA256 != sha256String([]byte(nativeCaddyUnit)) {
		return errors.New("agent: cannot stop an unowned native Caddy")
	}
	if _, err := readOwnedCaddyFile(runtime.Manager.path(nativeCaddyUnitPath), receipt.UnitSHA256, 64<<10); err != nil {
		return err
	}
	if err := runtime.Manager.run(ctx, "systemctl", "stop", "vastora-caddy.service"); err != nil {
		return err
	}
	return runtime.Manager.run(ctx, "systemctl", "disable", "vastora-caddy.service")
}

func (runtime NativeCaddySiteRuntime) ReconcileWebsites(ctx context.Context, desired gateway.DesiredState, sites []CaddySite) error {
	if len(desired.Listeners) != 0 || len(desired.Routes) != 0 || desired.SharedHTTPS != nil {
		return errors.New("agent: native website Caddy cannot adopt a Docker gateway")
	}
	if runtime.StateDirectory == "" {
		return errors.New("agent: shared Caddy state directory is unavailable")
	}
	receiptPath := filepath.Join(runtime.StateDirectory, "shared-caddy", "runtime.json")
	if err := checkPackageParents(receiptPath); err != nil {
		return err
	}
	bootstrap, _ := json.Marshal(map[string]any{"admin": map[string]any{"listen": "unix/" + nativeCaddySocket}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{}}}})
	var receipt nativeCaddyReceipt
	raw, err := os.ReadFile(receiptPath)
	if err == nil {
		info, statErr := os.Lstat(receiptPath)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || len(raw) > 4096 || json.Unmarshal(raw, &receipt) != nil || receipt.Version != 1 || len(receipt.SHA256) != 64 || receipt.UnitSHA256 != sha256String([]byte(nativeCaddyUnit)) || receipt.BootstrapSHA256 != sha256String(bootstrap) {
			return errors.New("agent: invalid shared Caddy ownership receipt")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	} else {
		if len(sites) == 0 {
			return errSharedCaddyRuntimeAbsent
		}
		for _, path := range []string{nativeCaddyBinary, nativeCaddyBootstrap, nativeCaddyUnitPath} {
			if _, err := os.Lstat(runtime.Manager.path(path)); !errors.Is(err, os.ErrNotExist) {
				return errors.New("agent: refusing to adopt an unowned Caddy runtime")
			}
		}
		source := sites[0]
		if len(source.CaddySHA256) != 64 || source.CaddyPath == "" {
			return errors.New("agent: signed Caddy runtime source is unavailable")
		}
		binary, err := readOwnedCaddyFile(runtime.Manager.path(source.CaddyPath), source.CaddySHA256, 64<<20)
		if err != nil {
			return err
		}
		receipt = nativeCaddyReceipt{Version: 1, SHA256: source.CaddySHA256, UnitSHA256: sha256String([]byte(nativeCaddyUnit)), BootstrapSHA256: sha256String(bootstrap)}
		encoded, _ := json.Marshal(receipt)
		// Record ownership intent before host writes; retries verify exact files.
		if err := writeHostFileAtomic(receiptPath, encoded, 0600); err != nil {
			return err
		}
		if err := writeHostFileAtomic(runtime.Manager.path(nativeCaddyBinary), binary, 0755); err != nil {
			return err
		}
		if err := writeHostFileAtomic(runtime.Manager.path(nativeCaddyBootstrap), bootstrap, 0600); err != nil {
			return err
		}
		if err := writeHostFileAtomic(runtime.Manager.path(nativeCaddyUnitPath), []byte(nativeCaddyUnit), 0644); err != nil {
			return err
		}
		if err := runtime.Manager.run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if _, err := readOwnedCaddyFile(runtime.Manager.path(nativeCaddyBinary), receipt.SHA256, 64<<20); err != nil {
		return err
	}
	if _, err := readOwnedCaddyFile(runtime.Manager.path(nativeCaddyUnitPath), receipt.UnitSHA256, 64<<10); err != nil {
		return err
	}
	if _, err := readOwnedCaddyFile(runtime.Manager.path(nativeCaddyBootstrap), receipt.BootstrapSHA256, 64<<10); err != nil {
		return err
	}
	if err := runtime.Manager.run(ctx, "systemctl", "enable", "vastora-caddy.service"); err != nil {
		return err
	}
	for _, site := range sites {
		if err := waitForBindAddress(ctx, site.Address); err != nil {
			return err
		}
		if site.TLSPort != 0 && site.PublicListen != "" {
			address, _, _ := net.SplitHostPort(site.PublicListen)
			if err := waitForBindAddress(ctx, address); err != nil {
				return err
			}
		}
	}
	// start is a no-op for an active server. Content/leaf-certificate updates
	// only /load the combined configuration and never restart the shared unit.
	if err := runtime.Manager.run(ctx, "systemctl", "start", "vastora-caddy.service"); err != nil {
		return err
	}
	return waitForCaddyAdminSocket(ctx, runtime.Manager.path(nativeCaddySocket))
}

func readOwnedCaddyFile(path, digest string, maximum int64) ([]byte, error) {
	if err := checkPackageParents(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() < 1 || info.Size() > maximum {
		return nil, errors.New("agent: unsafe shared Caddy runtime file")
	}
	raw, err := os.ReadFile(path)
	if err != nil || sha256String(raw) != digest {
		return nil, errors.New("agent: shared Caddy runtime file differs from its receipt")
	}
	return raw, nil
}
