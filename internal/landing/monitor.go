package landing

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

// BusinessResult must describe actual authenticated TCP and UDP exchanges
// through this peer's SOCKS listener. UDPRelay must be the peer's fixed private
// address and port, not an arbitrary address returned by a SOCKS server.
// Ping alone, a listening port, or successful UDP ASSOCIATE is insufficient.
type BusinessResult struct {
	Peer      PeerIdentity
	Revision  uint64
	TCP       bool
	UDP       bool
	UDPRelay  string
	ExitIPv4  string
	StartedAt time.Time
	CheckedAt time.Time
}

type MonitorStatus struct {
	Revision      uint64    `json:"revision"`
	State         string    `json:"state"`
	LinkState     string    `json:"linkState"`
	Reason        string    `json:"reason"`
	TCP           bool      `json:"tcp"`
	UDP           bool      `json:"udp"`
	ExitIPv4      string    `json:"exitIpv4,omitempty"`
	CheckedAt     time.Time `json:"checkedAt"`
	LastHealthyAt time.Time `json:"lastHealthyAt,omitempty"`
	AllowedUntil  time.Time `json:"allowedUntil,omitempty"`
}

// Monitor is only for a route which has already been applied. Initial enable
// must check before changing the old direct configuration. StopConnections
// must terminate old connections in the selected proxy instance (not HAProxy,
// the controller, or other nodes), and verify that termination before returning.
// Callers must disclose the resulting instance-wide interruption.
type Monitor struct {
	Gate            *BridgeGate
	Links           *LinkChecker
	CheckBusiness   func(context.Context, PeerIdentity, uint64) (BusinessResult, error)
	StopConnections func(context.Context) error
	Report          func(MonitorStatus)
}

func (m *Monitor) Run(ctx context.Context) error {
	if m.Gate == nil || m.Links == nil || m.CheckBusiness == nil || m.StopConnections == nil {
		return errors.New("landing: incomplete runtime monitor")
	}
	// No permission survives process restart. A kernel boot fence is separately
	// required: a monitor cannot retroactively close a pre-start boot window.
	if err := m.Gate.Install(ctx); err != nil {
		return errors.Join(err, m.stop())
	}
	if err := m.stop(); err != nil {
		return err
	}
	defer func() { _ = m.close(); _ = m.stop() }()
	ticker := time.NewTicker(CheckInterval)
	defer ticker.Stop()
	var lastHealthy time.Time
	wasAllowed := false
	for {
		status := MonitorStatus{Revision: m.Gate.revision, State: "blocked", LinkState: "unknown", Reason: "check_failed", LastHealthyAt: lastHealthy}
		before := m.Links.Check(ctx, m.Gate.peer)
		status.LinkState, status.Reason = before.State, before.Reason
		var business BusinessResult
		var after LinkResult
		var checkErr error
		if before.State == "direct" {
			businessCtx, cancel := context.WithTimeout(ctx, CheckTimeout)
			business, checkErr = m.CheckBusiness(businessCtx, m.Gate.peer, m.Gate.revision)
			if businessCtx.Err() != nil {
				checkErr = errors.New("landing: business probe expired")
			}
			cancel()
			if checkErr == nil {
				after = m.Links.Check(ctx, m.Gate.peer)
				status.LinkState = after.State
			}
		}
		until, healthy := leaseDeadline(m.Gate, before, business, after, time.Now())
		attemptedRenewal := false
		if healthy && checkErr == nil && ctx.Err() == nil {
			attemptedRenewal = true
			checkErr = m.Gate.renew(ctx, until)
			if checkErr == nil {
				status.State, status.Reason = "healthy", "direct_and_business_ready"
				status.TCP, status.UDP, status.ExitIPv4 = true, true, business.ExitIPv4
				status.AllowedUntil = until.UTC()
				lastHealthy = time.Now().UTC()
				status.LastHealthyAt = lastHealthy
			}
		}
		if status.State != "healthy" {
			// A failed renewal can have committed despite a lost response. Block
			// and stop connections even if it was the first renewal attempt.
			if err := m.close(); err != nil {
				return errors.Join(err, m.stop())
			}
			if wasAllowed || attemptedRenewal {
				if err := m.stop(); err != nil {
					return err
				}
			}
			if before.State == "direct" {
				status.Reason = "business_or_link_check_failed"
			}
		}
		wasAllowed = status.State == "healthy"
		status.CheckedAt = time.Now().UTC()
		if m.Report != nil {
			m.Report(status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Monitor) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*nftTimeout)
	defer cancel()
	return m.Gate.Block(ctx)
}

func (m *Monitor) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.StopConnections(ctx)
}

func leaseDeadline(gate *BridgeGate, before LinkResult, business BusinessResult, after LinkResult, now time.Time) (time.Time, bool) {
	validLink := func(result LinkResult) bool {
		return result.State == "direct" && result.Reason == "fresh_disco_direct_response" && !result.StartedAt.IsZero() &&
			!result.CheckedAt.Before(result.StartedAt) && result.CheckedAt.Sub(result.StartedAt) <= CheckTimeout && !result.CheckedAt.After(now)
	}
	if !validLink(before) || !validLink(after) || business.Peer != gate.peer || business.Revision != gate.revision || !business.TCP || !business.UDP ||
		business.StartedAt.Before(before.CheckedAt) || business.CheckedAt.Before(business.StartedAt) || business.CheckedAt.Sub(business.StartedAt) > CheckTimeout ||
		after.StartedAt.Before(business.CheckedAt) || business.UDPRelay != netip.AddrPortFrom(netip.MustParseAddr(gate.peer.Address), SOCKSPort).String() {
		return time.Time{}, false
	}
	address, err := netip.ParseAddr(business.ExitIPv4)
	if err != nil || !address.Is4() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || !address.IsGlobalUnicast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(address) {
		return time.Time{}, false
	}
	deadline := before.StartedAt.Add(AllowLifetime)
	return deadline, deadline.After(now.Add(nftTimeout + time.Second))
}
