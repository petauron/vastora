package center

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/petauron/vastora/internal/networking"
)

// This optional host-owned setting does not replace the IPv4 gateway binding
// or reconfigure listeners. It declares an independently provisioned HTTPS
// control endpoint, consumed by bootstrap and ongoing Agent reconciliation.
func readHeadscaleControlIPv6(dataDir string) (string, error) {
	payload, err := os.ReadFile(filepath.Join(dataDir, "headscale-control-addresses.json"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("center: read Headscale control addresses: %w", err)
	}
	var config struct {
		IPv6Address string `json:"ipv6Address"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return "", fmt.Errorf("center: invalid Headscale control addresses: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", errors.New("center: trailing Headscale control addresses content")
	}
	address := net.ParseIP(strings.TrimSpace(config.IPv6Address))
	if address == nil || address.To4() != nil || networking.Classify("control", address) != networking.KindPublic {
		return "", errors.New("center: Headscale control IPv6 must be a public unicast IPv6 address")
	}
	return address.String(), nil
}

// Overlay IPv4 addresses do not establish native IPv4 connectivity. Preserve
// the existing control address for all hosts with an ordinary IPv4 address,
// including NAT hosts, and select IPv6 only for an observed IPv6-only underlay.
func (s *Store) agentHasOnlyIPv6Underlay(ctx context.Context, agentID string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT address FROM agent_network_candidates WHERE agent_id = ? AND kind IN ('lan', 'public')`, agentID)
	if err != nil {
		return false, fmt.Errorf("center: read Agent control address family: %w", err)
	}
	defer rows.Close()
	ipv4, ipv6 := false, false
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return false, err
		}
		address := net.ParseIP(value)
		if address == nil {
			return false, errors.New("center: invalid Agent network candidate")
		}
		if address.To4() != nil {
			ipv4 = true
		} else {
			ipv6 = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return ipv6 && !ipv4, nil
}
