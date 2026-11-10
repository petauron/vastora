package tailscalehost

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
)

// PrivateNetworkDialer is an explicit host-admin transport setting, never an
// ambient HTTP proxy or an application data-plane proxy. It applies only to
// literal Tailscale destinations; TLS authorization remains with the caller.
func PrivateNetworkDialer(host string) (proxy.ContextDialer, error) {
	address, err := netip.ParseAddr(host)
	if err != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(address.Unmap()) && !netip.MustParsePrefix("fd7a:115c:a1e0::/48").Contains(address) {
		return nil, nil
	}
	const path = "/etc/petauron/private-network.json"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("private network: transport configuration unavailable")
	}
	directory, directoryErr := os.Lstat("/etc/petauron")
	if directoryErr != nil {
		return nil, errors.New("private network: protected transport directory required")
	}
	directoryOwner, directoryOK := directory.Sys().(*syscall.Stat_t)
	if !directoryOK || directoryOwner.Uid != 0 || !directory.IsDir() || directory.Mode()&os.ModeSymlink != 0 || directory.Mode().Perm()&0022 != 0 {
		return nil, errors.New("private network: protected transport directory required")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 4096 {
		return nil, errors.New("private network: protected root-owned transport configuration required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private network: transport configuration unavailable")
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("private network: transport configuration changed")
	}
	var config struct {
		SOCKS5 string `json:"socks5"`
	}
	decoder := json.NewDecoder(io.LimitReader(f, 4097))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("private network: invalid transport configuration")
	}
	u, err := url.Parse(config.SOCKS5)
	if err != nil || u.Scheme != "socks5" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("private network: explicit loopback SOCKS5 endpoint required")
	}
	ip, err := netip.ParseAddr(u.Hostname())
	port, portErr := strconv.Atoi(u.Port())
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" || portErr != nil || port < 1 || port > 65535 {
		return nil, errors.New("private network: explicit loopback SOCKS5 endpoint required")
	}
	dialer, err := proxy.SOCKS5("tcp", u.Host, nil, &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		return nil, errors.New("private network: SOCKS5 transport unavailable")
	}
	contextual, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("private network: cancellable SOCKS5 transport required")
	}
	return contextual, nil
}
