package main

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"syscall"

	"github.com/petauron/vastora/internal/networking"
)

// A host operator can pin the existing Headscale hostname to a verified IPv6
// address when an assigned IPv4 address no longer provides connectivity. This
// changes neither the Center's origin nor other Agents' desired state.
func readAgentHeadscaleControlIPv6(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("agent: cannot inspect local Headscale control address")
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(owner.Uid) != os.Geteuid() || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() <= 0 || info.Size() > 4096 {
		return "", errors.New("agent: unsafe local Headscale control address file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("agent: cannot read local Headscale control address")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", errors.New("agent: local Headscale control address file changed")
	}
	var config struct {
		IPv6Address string `json:"ipv6Address"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return "", errors.New("agent: invalid local Headscale control address")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", errors.New("agent: trailing local Headscale control address content")
	}
	ip := net.ParseIP(config.IPv6Address)
	if ip == nil || ip.To4() != nil || ip.String() != config.IPv6Address || networking.Classify("control", ip) != networking.KindPublic {
		return "", errors.New("agent: local Headscale control address requires a canonical public IPv6")
	}
	return ip.String(), nil
}
