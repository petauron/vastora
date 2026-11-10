package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"github.com/petauron/catalog/catalog"
	"github.com/petauron/vastora/internal/hostservice"
)

const openRCCaddyApp = "platform-shared-caddy"
const openRCCaddyConfig = "/etc/vastora/packages/vastora-platform-shared-caddy/caddy"
const openRCCaddyBinary = "/opt/vastora/packages/vastora-platform-shared-caddy/artifacts/caddy/caddy"
const openRCCaddyRuntime = "vastora-pkg-shared-caddy"
const openRCCaddySocket = "/run/" + openRCCaddyRuntime + "/admin.sock"
const openRCCaddyService = "vastora-pkg-shared-caddy.service"
const openRCCaddyUnit = "/etc/init.d/vastora-pkg-shared-caddy"

type openRCCaddyReceipt struct {
	nativeCaddyReceipt
	ProcessSHA256 string `json:"processSha256"`
	AccountID     string `json:"accountId"`
}

func (runtime NativeCaddySiteRuntime) withInitSystem() (NativeCaddySiteRuntime, error) {
	if runtime.Manager.InitSystem == "" {
		// RootDir is an injected host fixture; existing systemd fixtures retain
		// their explicit environment instead of detecting the developer's Mac.
		runtime.Manager.InitSystem = hostservice.Systemd
		if runtime.Manager.RootDir == "" {
			kind, err := hostservice.Detect()
			if err != nil {
				return runtime, err
			}
			runtime.Manager.InitSystem = kind
		}
	}
	if runtime.Manager.InitSystem != hostservice.OpenRC && runtime.Manager.InitSystem != hostservice.Systemd {
		return runtime, errors.New("agent: unsupported native Caddy init system")
	}
	return runtime, nil
}

// NativeCaddyAdminURL selects only the locally detected init system. The
// OpenRC socket stays inside the launcher's protected per-service runtime dir.
func NativeCaddyAdminURL() (string, error) {
	runtime, err := (NativeCaddySiteRuntime{}).withInitSystem()
	if err != nil {
		return "", err
	}
	if runtime.Manager.InitSystem == hostservice.OpenRC {
		return "unix://" + openRCCaddySocket, nil
	}
	return "unix://" + nativeCaddySocket, nil
}

func openRCCaddyProcess(digest string) ([]byte, []byte, error) {
	var arguments []catalog.Value
	for _, value := range []string{"run", "--resume", "--config", "/run/" + openRCCaddyRuntime + "/bootstrap"} {
		raw, _ := json.Marshal(value)
		literal := json.RawMessage(raw)
		arguments = append(arguments, catalog.Value{Literal: &literal})
	}
	unit := nativeUnitPlan{
		Name: openRCCaddyService, ConfigDirectory: openRCCaddyConfig,
		RuntimeDirectory: openRCCaddyRuntime,
		Process: catalog.SystemdRuntime{
			User: nativeAccountName(openRCCaddyApp, "caddy"), BindService: true,
			Arguments: arguments,
		},
	}
	return compileOpenRCProcess(openRCCaddyApp, unit, openRCCaddyBinary, digest, openRCCaddyConfig+"/environment", map[string]string{"bootstrap": openRCCaddyConfig + "/credentials/bootstrap"}, nil, nil, nil, false)
}

const openRCCaddyEnvironment = "XDG_CONFIG_HOME=\"/var/lib/vastora-caddy/config\"\nXDG_DATA_HOME=\"/var/lib/vastora-caddy/data\"\nGOMEMLIMIT=\"96MiB\"\nGOMAXPROCS=\"1\"\n"

func (runtime NativeCaddySiteRuntime) readOpenRCCaddyReceipt() (openRCCaddyReceipt, error) {
	var receipt openRCCaddyReceipt
	path := filepath.Join(runtime.StateDirectory, "shared-caddy", "runtime.json")
	if err := checkPackageParents(path); err != nil {
		return receipt, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return receipt, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return receipt, errors.New("agent: unsafe shared Caddy ownership receipt")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return receipt, err
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.Version != 2 || len(receipt.SHA256) != 64 || receipt.AccountID == "" {
		return receipt, errors.New("agent: invalid OpenRC Caddy ownership receipt")
	}
	return receipt, nil
}

func (runtime NativeCaddySiteRuntime) verifyOpenRCCaddy(ctx context.Context, receipt openRCCaddyReceipt) error {
	unit, spec, err := openRCCaddyProcess(receipt.SHA256)
	if err != nil {
		return err
	}
	bootstrap := openRCCaddyBootstrap()
	if receipt.UnitSHA256 != sha256String(unit) || receipt.ProcessSHA256 != sha256String(spec) || receipt.BootstrapSHA256 != sha256String(bootstrap) {
		return errors.New("agent: OpenRC Caddy declaration differs from receipt")
	}
	account, exists, err := readNativeAccount(ctx, runtime.Manager, nativeAccountName(openRCCaddyApp, "caddy"), openRCCaddyApp)
	if err != nil || !exists || account.identity() != receipt.AccountID {
		return errors.New("agent: shared Caddy account ownership changed")
	}
	for path, digest := range map[string]string{
		openRCCaddyBinary: receipt.SHA256, openRCCaddyUnit: receipt.UnitSHA256,
		openRCCaddyConfig + "/process.json":          receipt.ProcessSHA256,
		openRCCaddyConfig + "/environment":           sha256String([]byte(openRCCaddyEnvironment)),
		openRCCaddyConfig + "/credentials/bootstrap": receipt.BootstrapSHA256,
	} {
		if _, err := readOwnedCaddyFile(runtime.Manager.path(path), digest, 64<<20); err != nil {
			return err
		}
	}
	state := runtime.Manager.path("/var/lib/vastora-caddy")
	if err := checkPackageParents(state); err != nil {
		return err
	}
	info, err := os.Lstat(state)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return errors.New("agent: shared Caddy state ownership changed")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != account.UID || owner.Gid != account.GID {
		return errors.New("agent: shared Caddy state ownership changed")
	}
	return nil
}

func openRCCaddyBootstrap() []byte {
	raw, _ := json.Marshal(map[string]any{"admin": map[string]any{"listen": "unix/" + openRCCaddySocket}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{}}}})
	return raw
}

func (runtime NativeCaddySiteRuntime) idleOpenRC(ctx context.Context) error {
	receipt, err := runtime.readOpenRCCaddyReceipt()
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := runtime.verifyOpenRCCaddy(ctx, receipt); err != nil {
		return err
	}
	if err := runtime.Manager.service(ctx, "stop", openRCCaddyService); err != nil {
		return err
	}
	return runtime.Manager.service(ctx, "disable", openRCCaddyService)
}

func (runtime NativeCaddySiteRuntime) reconcileOpenRC(ctx context.Context, sites []CaddySite) error {
	receipt, err := runtime.readOpenRCCaddyReceipt()
	if errors.Is(err, os.ErrNotExist) {
		if len(sites) == 0 {
			return errSharedCaddyRuntimeAbsent
		}
		for _, path := range []string{openRCCaddyBinary, openRCCaddyConfig, openRCCaddyUnit, "/var/lib/vastora-caddy", nativeCaddyUnitPath} {
			if _, err := os.Lstat(runtime.Manager.path(path)); !errors.Is(err, os.ErrNotExist) {
				return errors.New("agent: refusing to adopt an unowned shared Caddy resource")
			}
		}
		name := nativeAccountName(openRCCaddyApp, "caddy")
		if _, exists, err := readNativeAccount(ctx, runtime.Manager, name, openRCCaddyApp); err != nil || exists {
			return errors.New("agent: shared Caddy account already exists without ownership receipt")
		}
		source := sites[0]
		if source.CaddyPath == "" || len(source.CaddySHA256) != 64 {
			return errors.New("agent: signed Caddy runtime source is unavailable")
		}
		binary, err := readOwnedCaddyFile(runtime.Manager.path(source.CaddyPath), source.CaddySHA256, 64<<20)
		if err != nil {
			return err
		}
		unit, spec, err := openRCCaddyProcess(source.CaddySHA256)
		if err != nil {
			return err
		}
		if err := runtime.Manager.createNativeAccount(ctx, name, openRCCaddyApp); err != nil {
			return err
		}
		account, exists, err := readNativeAccount(ctx, runtime.Manager, name, openRCCaddyApp)
		if err != nil || !exists {
			return errors.New("agent: shared Caddy account unavailable after creation")
		}
		receipt = openRCCaddyReceipt{nativeCaddyReceipt: nativeCaddyReceipt{Version: 2, SHA256: source.CaddySHA256, UnitSHA256: sha256String(unit), BootstrapSHA256: sha256String(openRCCaddyBootstrap())}, ProcessSHA256: sha256String(spec), AccountID: account.identity()}
		raw, _ := json.Marshal(receipt)
		if err := writeHostFileAtomic(filepath.Join(runtime.StateDirectory, "shared-caddy", "runtime.json"), raw, 0600); err != nil {
			return err
		}
		for _, file := range []struct {
			path string
			raw  []byte
			mode os.FileMode
		}{
			{openRCCaddyBinary, binary, 0755}, {openRCCaddyUnit, unit, 0755},
			{openRCCaddyConfig + "/process.json", spec, 0600},
			{openRCCaddyConfig + "/environment", []byte(openRCCaddyEnvironment), 0600},
			{openRCCaddyConfig + "/credentials/bootstrap", openRCCaddyBootstrap(), 0600},
		} {
			if err := writeHostFileAtomic(runtime.Manager.path(file.path), file.raw, file.mode); err != nil {
				return err
			}
		}
		state := runtime.Manager.path("/var/lib/vastora-caddy")
		if err := checkPackageParents(state); err != nil {
			return err
		}
		if err := os.Mkdir(state, 0700); err != nil {
			return err
		}
		if err := os.Chown(state, int(account.UID), int(account.GID)); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err := runtime.verifyOpenRCCaddy(ctx, receipt); err != nil {
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
	if err := runtime.Manager.service(ctx, "enable", openRCCaddyService); err != nil {
		return err
	}
	if err := runtime.Manager.service(ctx, "start", openRCCaddyService); err != nil {
		return err
	}
	return waitForNativeCaddyAdmin(ctx, runtime.Manager.path(openRCCaddySocket))
}
