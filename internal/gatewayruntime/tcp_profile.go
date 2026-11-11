package gatewayruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/moby/moby/client"
)

const (
	tcpCongestionControl = "net.ipv4.tcp_congestion_control"
	tcpReceiveMemory     = "net.ipv4.tcp_rmem"
	tcpSendMemory        = "net.ipv4.tcp_wmem"
)

var tcpProfileKeys = []string{tcpCongestionControl, tcpReceiveMemory, tcpSendMemory}
var tcpProfilePaths = []string{
	"/proc/sys/net/ipv4/tcp_congestion_control",
	"/proc/sys/net/ipv4/tcp_rmem",
	"/proc/sys/net/ipv4/tcp_wmem",
}

// HostTCPProfile must be called from the native Agent, not from a bridged
// control-plane container. Only network-namespaced settings belong here; fq
// remains on the host's actual egress device.
func HostTCPProfile() (map[string]string, error) {
	var values []string
	for _, path := range tcpProfilePaths {
		value, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("gateway: read host TCP profile: %w", err)
		}
		values = append(values, strings.TrimSpace(string(value)))
	}
	return parseTCPProfile(strings.Join(values, "\n"))
}

func parseTCPProfile(encoded string) (map[string]string, error) {
	lines := strings.Split(strings.TrimSpace(encoded), "\n")
	if len(lines) != len(tcpProfileKeys) {
		return nil, errors.New("gateway: TCP profile must contain exactly three values")
	}
	algorithm := strings.TrimSpace(lines[0])
	if len(algorithm) == 0 || len(algorithm) > 32 {
		return nil, errors.New("gateway: invalid TCP congestion control")
	}
	for _, c := range algorithm {
		if c != '_' && c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return nil, errors.New("gateway: invalid TCP congestion control")
		}
	}
	profile := map[string]string{tcpCongestionControl: algorithm}
	for index, key := range tcpProfileKeys[1:] {
		values, err := tcpMemoryValues(lines[index+1])
		if err != nil {
			return nil, fmt.Errorf("gateway: invalid %s: %w", key, err)
		}
		profile[key] = formatTCPMemory(values)
	}
	return profile, nil
}

func tcpMemoryValues(encoded string) ([3]int64, error) {
	var result [3]int64
	fields := strings.Fields(encoded)
	if len(fields) != len(result) {
		return result, errors.New("expected minimum, default and maximum")
	}
	for index, field := range fields {
		value, err := strconv.ParseInt(field, 10, 32)
		if err != nil || value <= 0 || index > 0 && value < result[index-1] {
			return result, errors.New("expected ordered positive 32-bit buffer sizes")
		}
		result[index] = value
	}
	return result, nil
}

func formatTCPMemory(values [3]int64) string {
	return fmt.Sprintf("%d %d %d", values[0], values[1], values[2])
}

// ContainerTCPProfile takes the existing namespace as the buffer baseline.
// It preserves minimum/default sizes and raises only caps below the host.
// Persisted values cover a stopped container, whose old namespace no longer
// exists. Invalid/unreadable live state fails before replacing any gateway.
func ContainerTCPProfile(ctx context.Context, docker *client.Client, host map[string]string, current *client.ContainerInspectResult) (map[string]string, bool, error) {
	baseline := host
	if current != nil {
		if current.Container.State != nil && current.Container.State.Running {
			live, err := ReadContainerTCPProfile(ctx, docker, current.Container.ID)
			if err != nil {
				return nil, false, err
			}
			baseline = live
		} else if current.Container.HostConfig != nil {
			baseline = current.Container.HostConfig.Sysctls
		}
	}
	result := map[string]string{tcpCongestionControl: host[tcpCongestionControl]}
	if current != nil && current.Container.HostConfig != nil {
		for key, value := range current.Container.HostConfig.Sysctls {
			result[key] = value
		}
		result[tcpCongestionControl] = host[tcpCongestionControl]
	}
	for _, key := range tcpProfileKeys[1:] {
		hostValues, err := tcpMemoryValues(host[key])
		if err != nil {
			return nil, false, err
		}
		values := hostValues
		if value, found := baseline[key]; found {
			values, err = tcpMemoryValues(value)
			if err != nil {
				return nil, false, err
			}
		}
		values[2] = max(values[2], hostValues[2])
		result[key] = formatTCPMemory(values)
	}
	return result, TCPProfileMatches(baseline, result), nil
}

// TCPProfileMatches compares normalized managed keys, leaving unrelated Docker
// sysctls alone. The same comparison is used for immutable options and readback.
func TCPProfileMatches(actual, expected map[string]string) bool {
	for _, key := range tcpProfileKeys {
		if strings.Join(strings.Fields(actual[key]), " ") != expected[key] || expected[key] == "" {
			return false
		}
	}
	return true
}

func VerifyContainerTCPProfile(ctx context.Context, docker *client.Client, id string, expected map[string]string) error {
	actual, err := ReadContainerTCPProfile(ctx, docker, id)
	if err != nil {
		return err
	}
	if !TCPProfileMatches(actual, expected) {
		return errors.New("gateway: effective TCP profile differs from the host-aligned managed profile")
	}
	return nil
}

// CheckContainerTCPProfile reads the effective namespace once per health check.
// Both persisted Docker options and host policy must agree with that readback.
func CheckContainerTCPProfile(ctx context.Context, docker *client.Client, current client.ContainerInspectResult) error {
	host, err := HostTCPProfile()
	if err != nil {
		return err
	}
	actual, err := ReadContainerTCPProfile(ctx, docker, current.Container.ID)
	if err != nil {
		return err
	}
	if current.Container.HostConfig == nil || !TCPProfileMatches(current.Container.HostConfig.Sysctls, actual) || actual[tcpCongestionControl] != host[tcpCongestionControl] {
		return errors.New("gateway: persisted or effective TCP profile has drifted from the host")
	}
	for _, key := range tcpProfileKeys[1:] {
		hostValues, _ := tcpMemoryValues(host[key])
		actualValues, _ := tcpMemoryValues(actual[key])
		if actualValues[2] < hostValues[2] {
			return errors.New("gateway: effective TCP buffer cap is below the host")
		}
	}
	return nil
}
