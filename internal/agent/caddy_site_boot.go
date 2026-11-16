package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/petauron/vastora/internal/hostservice"
)

const nativeCaddyAutosave = "/var/lib/vastora-caddy/config/caddy/autosave.json"
const nativeCaddyBootDropin = "/etc/systemd/system/vastora-caddy.service.d/90-vastora-bind-ready.conf"
const nativeCaddyBootMarker = "# Managed by Vastora: native Caddy bind readiness\n"

// nativeCaddyBootUnit waits only for exact non-loopback listeners in Caddy's
// own resumed configuration. The guard also makes an interrupted website
// update safe: a listener absent from the actual autosave is not waited on.
func nativeCaddyBootUnit(listens []string) ([]byte, error) {
	listens = slices.Clone(listens)
	slices.Sort(listens)
	listens = slices.Compact(listens)
	var metadata, script strings.Builder
	for _, listen := range listens {
		host, port, err := net.SplitHostPort(listen)
		if err != nil {
			return nil, errors.New("agent: invalid native Caddy boot listener")
		}
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return nil, errors.New("agent: invalid native Caddy boot port")
		}
		if host == "" {
			continue // An unspecified listener does not require a local address.
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || ip.Zone() != "" || ip.IsMulticast() {
			return nil, errors.New("agent: invalid native Caddy boot listener")
		}
		if ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		metadata.WriteString("# Listen: " + listen + "\n")
		pattern := `"listen":["` + listen + `"`
		script.WriteString("if /usr/bin/tr -d '[:space:]' < " + nativeCaddyAutosave + " 2>/dev/null | /bin/grep -Fq -- " + strconv.Quote(pattern) + "; then ")
		if ip.Is4() {
			script.WriteString("until /usr/bin/awk -v target=" + ip.String() + ` '$$1 == "|--" && $$2 == target { getline; if ($$1 == "/32" && $$2 == "host" && $$3 == "LOCAL") found=1 } END { exit !found }' /proc/net/fib_trie; do /bin/sleep 1; done; `)
		} else {
			address := ip.As16()
			hexAddress := hex.EncodeToString(address[:])
			// if_inet6 uses hexadecimal flags: bit 0x40 is tentative and
			// bit 0x08 is DAD failure. Avoid non-portable awk bit operations.
			script.WriteString("until /usr/bin/awk -v target=" + hexAddress + ` '$$1 == target { f=tolower($$5); h=substr(f,length(f)-1,1); l=substr(f,length(f),1); if (h !~ /[4567cdef]/ && l !~ /[89abcdef]/) found=1 } END { exit !found }' /proc/net/if_inet6; do /bin/sleep 1; done; `)
		}
		script.WriteString("fi; ")
	}
	if metadata.Len() == 0 {
		return nil, nil
	}
	return []byte(nativeCaddyBootMarker + metadata.String() + "[Unit]\nAfter=tailscaled.service\n[Service]\nTimeoutStartSec=180s\nExecStartPre=/bin/sh -c " + strconv.Quote(script.String()) + "\n"), nil
}

func ownedNativeCaddyBootUnit(raw []byte) bool {
	if !bytes.HasPrefix(raw, []byte(nativeCaddyBootMarker)) {
		return false
	}
	var listeners []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "# Listen: ") {
			listeners = append(listeners, strings.TrimPrefix(line, "# Listen: "))
		}
	}
	want, err := nativeCaddyBootUnit(listeners)
	return err == nil && len(want) != 0 && bytes.Equal(raw, want)
}

// SyncBootWait verifies the existing platform ownership receipt and never
// rewrites the Caddy unit or its bootstrap, certificate, or saved config.
func (runtime NativeCaddySiteRuntime) SyncBootWait(ctx context.Context, raw []byte) error {
	var err error
	runtime, err = runtime.withInitSystem()
	if err != nil || runtime.Manager.InitSystem == hostservice.OpenRC {
		return err
	}
	receiptPath := filepath.Join(runtime.StateDirectory, "shared-caddy", "runtime.json")
	if err := checkPackageParents(receiptPath); err != nil {
		return err
	}
	info, err := os.Lstat(receiptPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return errors.New("agent: unsafe native Caddy ownership receipt")
	}
	encoded, err := os.ReadFile(receiptPath)
	var receipt nativeCaddyReceipt
	bootstrap, _ := json.Marshal(map[string]any{"admin": map[string]any{"listen": "unix/" + nativeCaddySocket}, "apps": map[string]any{"http": map[string]any{"servers": map[string]any{}}}})
	if err != nil || json.Unmarshal(encoded, &receipt) != nil || receipt.Version != 1 || len(receipt.SHA256) != 64 || receipt.UnitSHA256 != sha256String([]byte(nativeCaddyUnit)) || receipt.BootstrapSHA256 != sha256String(bootstrap) {
		return errors.New("agent: invalid native Caddy ownership receipt")
	}
	if _, err := readOwnedCaddyFile(runtime.Manager.path(nativeCaddyUnitPath), receipt.UnitSHA256, 64<<10); err != nil {
		return err
	}
	if _, err := readOwnedCaddyFile(runtime.Manager.path(nativeCaddyBootstrap), receipt.BootstrapSHA256, 64<<10); err != nil {
		return err
	}
	if raw == nil {
		file := runtime.Manager.path(nativeCaddyAutosave)
		info, err := os.Stat(file)
		if errors.Is(err, os.ErrNotExist) {
			raw = []byte(`{}`)
		} else if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
			return errors.New("agent: native Caddy saved configuration unavailable")
		} else if raw, err = os.ReadFile(file); err != nil {
			return errors.New("agent: native Caddy saved configuration unavailable")
		}
	}
	var config struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen []string `json:"listen"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if len(raw) > 64<<20 || json.Unmarshal(raw, &config) != nil {
		return errors.New("agent: invalid native Caddy saved configuration")
	}
	var listeners []string
	for _, server := range config.Apps.HTTP.Servers {
		listeners = append(listeners, server.Listen...)
	}
	wanted, err := nativeCaddyBootUnit(listeners)
	if err != nil {
		return err
	}
	path := runtime.Manager.path(nativeCaddyBootDropin)
	if len(wanted) == 0 {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	if err := hostservice.EnsureSystemdDropin(ctx, filepath.Dir(path), filepath.Base(path), wanted, ownedNativeCaddyBootUnit, runtime.Manager.run); err != nil {
		return fmt.Errorf("agent: native Caddy boot readiness: %w", err)
	}
	return nil
}
