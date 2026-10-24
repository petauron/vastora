package landing

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

// BusinessResult must describe actual authenticated TCP and UDP exchanges
// through this peer's SOCKS listener. UDPRelay must be the peer's private
// address and configured Dante port range, not an arbitrary returned endpoint.
// Ping alone, a listening port, or successful UDP ASSOCIATE is insufficient.
type BusinessResult struct {
	Peer      PeerIdentity
	Revision  uint64
	TCP       bool
	UDP       bool
	UDPRelay  string
	ExitIP    string
	StartedAt time.Time
	CheckedAt time.Time
}

type MonitorStatus struct {
	Revision             uint64    `json:"revision"`
	State                string    `json:"state"`
	LinkState            string    `json:"linkState"`
	Reason               string    `json:"reason"`
	TCP                  bool      `json:"tcp"`
	UDP                  bool      `json:"udp"`
	ExitIP               string    `json:"exitIp,omitempty"`
	CheckedAt            time.Time `json:"checkedAt"`
	StartedAt            time.Time `json:"startedAt"`
	DurationMS           int64     `json:"durationMs"`
	Stage                string    `json:"stage"`
	HTTPStatus           int       `json:"httpStatus,omitempty"`
	LastHealthyAt        time.Time `json:"lastHealthyAt,omitempty"`
	AllowedUntil         time.Time `json:"allowedUntil,omitempty"`
	SecurityCheckedAt    time.Time `json:"securityCheckedAt,omitempty"`
	ConsecutiveFailures  int       `json:"consecutiveFailures,omitempty"`
	ConsecutiveSuccesses int       `json:"consecutiveSuccesses,omitempty"`
}

// Monitor is only for a route which has already been applied. Initial enable
// must check before changing the old direct configuration. A configuration task
// closes the gate and terminates old connections synchronously before Run.
// The check runs once after activation; no periodic probe or expiring gate
// may interrupt an already accepted route.
type Monitor struct {
	Gate          *TrafficGate
	Links         *LinkChecker
	CheckBusiness func(context.Context, PeerIdentity, uint64) (BusinessResult, error)
	Report        func(MonitorStatus)
	TCPOnly       bool
}

// Run owns Links for this monitor's lifetime, including validation and setup
// failures. A checker passed here must not be shared with another monitor.
func (m *Monitor) Run(ctx context.Context) (result error) {
	if m.Links != nil {
		defer m.Links.Close()
	}
	defer func() {
		if result != nil && m.Report != nil && m.Gate != nil {
			m.Report(MonitorStatus{Revision: m.Gate.revision, State: "blocked", Stage: "startup", Reason: "startup_failed", CheckedAt: time.Now().UTC()})
		}
	}()
	if m.Gate == nil || m.Links == nil || m.CheckBusiness == nil {
		return errors.New("landing: incomplete startup check")
	}
	if err := m.Gate.Install(ctx); err != nil {
		return err
	}
	started := time.Now()
	checkLink := m.Links.Check

	before := checkLink(ctx, m.Gate.peer)
	if before.State != "direct" {
		return errors.New("landing: startup peer identity unavailable")
	}
	businessCtx, cancel := context.WithTimeout(ctx, businessCheckTimeout(m.TCPOnly))
	defer cancel()
	business, err := m.CheckBusiness(businessCtx, m.Gate.peer, m.Gate.revision)
	if err != nil {
		return err
	}
	after := checkLink(ctx, m.Gate.peer)
	_, valid := leaseDeadlineForTransport(m.Gate, before, business, after, time.Now(), m.TCPOnly)
	if !valid || ctx.Err() != nil {
		return errors.New("landing: startup route proof invalid")
	}
	// Acceptance removes only this owned closed startup fence. There is no
	// expiring permission and no background lease renewal or traffic shutdown.
	if err := m.Gate.Remove(ctx); err != nil {
		return err
	}
	if m.Report != nil {
		m.Report(MonitorStatus{Revision: m.Gate.revision, State: "healthy", LinkState: after.State, Stage: "startup", Reason: "startup_ready", TCP: business.TCP, UDP: business.UDP, ExitIP: business.ExitIP, StartedAt: started.UTC(), CheckedAt: time.Now().UTC(), DurationMS: time.Since(started).Milliseconds()})
	}
	return nil
}

func (m *Monitor) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*nftTimeout)
	defer cancel()
	return m.Gate.Block(ctx)
}

func leaseDeadline(gate *TrafficGate, before LinkResult, business BusinessResult, after LinkResult, now time.Time) (time.Time, bool) {
	return leaseDeadlineForTransport(gate, before, business, after, now, false)
}

func businessCheckTimeout(tcpOnly bool) time.Duration {
	if tcpOnly {
		return 20 * time.Second
	}
	return CheckTimeout
}

func leaseDeadlineForTransport(gate *TrafficGate, before LinkResult, business BusinessResult, after LinkResult, now time.Time, tcpOnly bool) (time.Time, bool) {
	validLink := func(result LinkResult) bool {
		return result.State == "direct" && result.Reason == "fresh_disco_direct_response" && !result.StartedAt.IsZero() &&
			!result.CheckedAt.Before(result.StartedAt) && result.CheckedAt.Sub(result.StartedAt) <= CheckTimeout && !result.CheckedAt.After(now)
	}
	if !validLink(before) || !validLink(after) || business.Peer != gate.peer || business.Revision != gate.revision || !business.TCP ||
		business.StartedAt.Before(before.CheckedAt) || business.CheckedAt.Before(business.StartedAt) || business.CheckedAt.Sub(business.StartedAt) > businessCheckTimeout(tcpOnly) ||
		after.StartedAt.Before(business.CheckedAt) || (!tcpOnly && (!business.UDP || !validUDPRelay(business.UDPRelay, gate.peer.Address))) {
		return time.Time{}, false
	}
	address, err := netip.ParseAddr(business.ExitIP)
	if err != nil || !PublicIP(address) {
		return time.Time{}, false
	}
	deadline := before.StartedAt.Add(AllowLifetime)
	return deadline, deadline.After(now.Add(nftTimeout + time.Second))
}
