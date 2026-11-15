//go:build linux

package osreinstall

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Read every included declarative source, without evaluating provider scripts.
func readNetworkSources() (kind string, paths []string, data [][]byte, err error) {
	files, e := filepath.Glob("/etc/netplan/*.yaml")
	if e != nil {
		return "", nil, nil, e
	}
	extra, e := filepath.Glob("/etc/netplan/*.yml")
	if e != nil {
		return "", nil, nil, e
	}
	files = append(files, extra...)
	if len(files) > 0 {
		if len(files) != 1 {
			return "", nil, nil, networkError()
		}
		b, e := readReviewedFile(files[0])
		return "netplan_static_ipv4", files, [][]byte{b}, e
	}
	root := "/etc/network/interfaces"
	b, e := readReviewedFile(root)
	if e != nil {
		return "", nil, nil, e
	}
	paths = []string{root}
	data = [][]byte{b}
	entries, e := os.ReadDir("/etc/network/interfaces.d")
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return "", nil, nil, e
	}
	if len(entries) > 8 {
		return "", nil, nil, networkError()
	}
	included := bytes.Contains(b, []byte("source /etc/network/interfaces.d/*")) || bytes.Contains(b, []byte("source-directory /etc/network/interfaces.d"))
	if len(entries) > 0 && !included {
		return "", nil, nil, networkError()
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			return "", nil, nil, networkError()
		}
		p := filepath.Join("/etc/network/interfaces.d", entry.Name())
		v, e := readReviewedFile(p)
		if e != nil {
			return "", nil, nil, e
		}
		paths = append(paths, p)
		data = append(data, v)
	}
	return "ifupdown_static_ipv4", paths, data, nil
}
func parseNetworkSources(kind string, data [][]byte, resolver []byte, network Network) (StaticNetworkPlan, error) {
	if kind == "netplan_static_ipv4" && len(data) == 1 {
		return ParseStaticNetplan(data[0], resolver, network)
	}
	if kind == "ifupdown_static_ipv4" {
		return ParseStaticInterfaces(bytes.Join(data, []byte("\n")), resolver, network)
	}
	return StaticNetworkPlan{}, networkError()
}
func readNetworkCandidate(n Network, resolver []byte) string {
	kind, _, data, e := readNetworkSources()
	if e != nil {
		return ""
	}
	if _, e = parseNetworkSources(kind, data, resolver, n); e != nil {
		return ""
	}
	return kind
}
func verifyManagers(ctx context.Context, kind string) error {
	networkd, e := managerActive(ctx, "systemd-networkd.service")
	if e != nil {
		return e
	}
	interfaces, e := managerActive(ctx, "networking.service")
	if e != nil {
		return e
	}
	nm, e := managerActive(ctx, "NetworkManager.service")
	if e != nil || nm {
		return networkError()
	}
	if kind == "netplan_static_ipv4" && networkd && !interfaces || kind == "ifupdown_static_ipv4" && interfaces && !networkd {
		return nil
	}
	return networkError()
}
