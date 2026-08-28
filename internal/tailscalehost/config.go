package tailscalehost

import (
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
)

const (
	SupportedVersion = "1.102.3"
	FixedPort        = 41641
)

type ConfigVAlpha struct {
	Version         string   `json:"version"`
	Locked          bool     `json:"locked"`
	StaticEndpoints []string `json:"staticEndpoints"`
}

func StaticEndpoint(publicAddress string) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(publicAddress))
	if ip == nil || ip.To4() == nil {
		return "", errors.New("tailscale static endpoint requires a public IPv4 address")
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(FixedPort)), nil
}

func RenderConfig(staticEndpoints []string) ([]byte, error) {
	validated := make([]string, 0, len(staticEndpoints))
	seen := make(map[string]struct{}, len(staticEndpoints))
	for _, endpoint := range staticEndpoints {
		host, port, err := net.SplitHostPort(strings.TrimSpace(endpoint))
		if err != nil || port != strconv.Itoa(FixedPort) {
			return nil, errors.New("tailscale static endpoint must use UDP port 41641")
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.To4() == nil {
			return nil, errors.New("tailscale static endpoint requires an IPv4 address")
		}
		normalized := net.JoinHostPort(ip.String(), strconv.Itoa(FixedPort))
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		validated = append(validated, normalized)
	}
	payload, err := json.Marshal(ConfigVAlpha{Version: "alpha0", Locked: false, StaticEndpoints: validated})
	if err != nil {
		return nil, err
	}
	return append(payload, '\n'), nil
}
