// Package landing contains the node-local boundary for optional SOCKS5 egress.
// A successful check describes one instant, not a direct-only transport promise.
package landing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"time"
)

const (
	CheckInterval = 5 * time.Second
	CheckTimeout  = 3 * time.Second
	AllowLifetime = 15 * time.Second
	DefaultSocket = "/var/run/tailscale/tailscaled.sock"
)

type PeerIdentity struct {
	ID        string `json:"id"`
	PublicKey string `json:"publicKey"`
	Address   string `json:"address"`
}

type LinkResult struct {
	LatencyMS *float64  `json:"latencyMs,omitempty"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	StartedAt time.Time `json:"startedAt"`
	CheckedAt time.Time `json:"checkedAt"`
}

type localPeer struct {
	ID           string
	PublicKey    string
	TailscaleIPs []string
	Online       *bool
}

type localStatus struct {
	BackendState string
	Self         *localPeer
	Peer         map[string]*localPeer
}

// Only disco ping supplies current transport evidence. Status.Relay, CurAddr,
// Online and historical handshakes are intentionally absent from pingResult.
// Contract: tailscale v1.102.3 ipn/ipnstate.PingResult and client/local.Ping.
type pingResult struct {
	LatencySeconds *float64
	IP             string
	NodeIP         string
	Endpoint       string
	PeerRelay      string
	DERPRegionID   int
	DERPRegionCode string
	Err            string
	IsLocalIP      bool
}

type LinkChecker struct{ HTTPClient *http.Client }

func NewLinkChecker() *LinkChecker {
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", DefaultSocket)
	}}
	return &LinkChecker{HTTPClient: &http.Client{Transport: transport, Timeout: CheckTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// SelfIdentity is reported by the landing Agent through its authenticated
// control channel; callers must not obtain a peer identity from an untrusted UI.
func (c *LinkChecker) SelfIdentity(ctx context.Context, expectedAddress string) (PeerIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	var status localStatus
	if err := c.read(ctx, http.MethodGet, "/localapi/v0/status?peers=false", &status); err != nil {
		return PeerIdentity{}, err
	}
	if status.BackendState != "Running" || status.Self == nil || status.Self.ID == "" || status.Self.PublicKey == "" || !slices.Contains(status.Self.TailscaleIPs, expectedAddress) {
		return PeerIdentity{}, errors.New("landing: private node identity is unavailable")
	}
	return PeerIdentity{ID: status.Self.ID, PublicKey: status.Self.PublicKey, Address: expectedAddress}, nil
}

func (c *LinkChecker) Check(ctx context.Context, expected PeerIdentity) LinkResult {
	started := time.Now()
	result := LinkResult{State: "unknown", Reason: "check_failed", StartedAt: started.UTC()}
	finish := func(state, reason string) LinkResult {
		result.State, result.Reason, result.CheckedAt = state, reason, time.Now().UTC()
		return result
	}
	address, err := netip.ParseAddr(expected.Address)
	if err != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(address) || expected.ID == "" || expected.PublicKey == "" {
		return finish("unknown", "invalid_peer_identity")
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	var before localStatus
	if err := c.read(ctx, http.MethodGet, "/localapi/v0/status?peers=true", &before); err != nil {
		return finish("unknown", "local_status_unavailable")
	}
	if before.BackendState != "Running" {
		return finish("disconnected", "private_network_disconnected")
	}
	if before.Self != nil && (before.Self.ID == expected.ID || slices.Contains(before.Self.TailscaleIPs, expected.Address)) {
		return finish("unknown", "local_target_is_not_remote")
	}
	peer := matchPeer(before, expected)
	if peer == nil {
		return finish("unknown", "peer_identity_mismatch")
	}
	if peer.Online != nil && !*peer.Online {
		return finish("disconnected", "peer_disconnected")
	}
	var ping pingResult
	query := url.Values{"ip": {address.String()}, "type": {"disco"}, "size": {"0"}}
	if err := c.read(ctx, http.MethodPost, "/localapi/v0/ping?"+query.Encode(), &ping); err != nil {
		return finish("unknown", "probe_failed")
	}
	if time.Since(started) > CheckTimeout || ctx.Err() != nil {
		return finish("unknown", "probe_expired")
	}
	state, reason := classifyPing(ping, expected.Address)
	if state != "direct" {
		return finish(state, reason)
	}
	var after localStatus
	if err := c.read(ctx, http.MethodGet, "/localapi/v0/status?peers=true", &after); err != nil || after.BackendState != "Running" || matchPeer(after, expected) == nil {
		return finish("unknown", "peer_identity_changed")
	}
	if peer := matchPeer(after, expected); peer.Online != nil && !*peer.Online {
		return finish("disconnected", "peer_disconnected")
	}
	if time.Since(started) > CheckTimeout || ctx.Err() != nil {
		return finish("unknown", "probe_expired")
	}
	if ping.LatencySeconds != nil && !math.IsNaN(*ping.LatencySeconds) && !math.IsInf(*ping.LatencySeconds, 0) && *ping.LatencySeconds > 0 && *ping.LatencySeconds <= CheckTimeout.Seconds() {
		ms := *ping.LatencySeconds * 1000
		result.LatencyMS = &ms
	}
	return finish("direct", "fresh_disco_direct_response")
}

func matchPeer(status localStatus, expected PeerIdentity) *localPeer {
	var found *localPeer
	for _, peer := range status.Peer {
		if peer == nil || !slices.Contains(peer.TailscaleIPs, expected.Address) {
			continue
		}
		if found != nil || peer.ID != expected.ID || peer.PublicKey != expected.PublicKey {
			return nil
		}
		found = peer
	}
	return found
}

func classifyPing(ping pingResult, expectedAddress string) (string, string) {
	if ping.IsLocalIP || ping.IP != expectedAddress || ping.NodeIP != expectedAddress {
		return "unknown", "probe_target_mismatch"
	}
	if ping.Err != "" {
		return "unknown", "probe_failed"
	}
	if ping.PeerRelay != "" {
		return "peer_relay", "peer_relay_detected"
	}
	if ping.DERPRegionID != 0 || ping.DERPRegionCode != "" {
		return "derp", "derp_detected"
	}
	endpoint, err := netip.ParseAddrPort(ping.Endpoint)
	if err != nil || endpoint.Port() == 0 || endpoint.Addr().IsUnspecified() || endpoint.Addr().IsMulticast() || endpoint.Addr().IsLoopback() {
		return "unknown", "direct_endpoint_unavailable"
	}
	return "direct", "fresh_disco_direct_response"
}

func (c *LinkChecker) read(ctx context.Context, method, path string, target any) error {
	if c == nil || c.HTTPClient == nil {
		return errors.New("landing: local checker is unavailable")
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://local-tailscaled.sock"+path, nil)
	if err != nil {
		return errors.New("landing: local request is invalid")
	}
	response, err := c.HTTPClient.Do(request)
	if err != nil {
		return errors.New("landing: local request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("landing: local request was rejected")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, target) != nil {
		return errors.New("landing: local response is invalid")
	}
	return nil
}
