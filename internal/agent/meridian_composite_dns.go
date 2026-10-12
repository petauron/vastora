package agent

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/petauron/glivela"
	"golang.org/x/sync/singleflight"
)

// This resolver lives in the existing Agent. It has no configuration API,
// credentials, public listener, or authority to change a route. Xray's
// immutable artifact owns route selection; DNS supplies bounded evidence.
type compositeDNSServer struct {
	udp, tcp  *dns.Server
	mu        sync.Mutex
	cache     map[string]compositeDNSAnswer
	queries   singleflight.Group
	upstreams []string
	now       func() time.Time
	exchange  func(context.Context, *dns.Msg, string) (*dns.Msg, error)
	failed    bool
}

type compositeDNSAnswer struct {
	addresses []netip.Addr
	expires   time.Time
	rcode     int
}

func (s *Store) ensureCompositeDNS(artifact meridian.DesiredArtifact) error {
	if len(artifact.Files) == 0 {
		return nil
	}
	s.compositeDNSMu.Lock()
	defer s.compositeDNSMu.Unlock()
	if s.compositeDNSClosed {
		return errors.New("agent: managed composite DNS is closing")
	}
	if s.compositeDNS != nil {
		s.compositeDNS.mu.Lock()
		failed := s.compositeDNS.failed
		s.compositeDNS.mu.Unlock()
		if failed {
			return errors.New("agent: managed composite DNS stopped; restart the Agent before retrying")
		}
		return nil
	}
	udp, err := net.ListenPacket("udp4", meridian.CompositeDNSAddress)
	if err != nil {
		return errors.New("agent: managed composite DNS port is unavailable")
	}
	tcp, err := net.Listen("tcp4", meridian.CompositeDNSAddress)
	if err != nil {
		_ = udp.Close()
		return errors.New("agent: managed composite DNS TCP port is unavailable")
	}
	server := &compositeDNSServer{cache: map[string]compositeDNSAnswer{}, upstreams: []string{"1.1.1.1:53", "1.0.0.1:53"}, now: time.Now, exchange: exchangeCompositeDNS}
	started, stopped := make(chan struct{}, 2), make(chan struct{}, 2)
	notify := func() { started <- struct{}{} }
	server.udp = &dns.Server{PacketConn: udp, Handler: server, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, NotifyStartedFunc: notify}
	server.tcp = &dns.Server{Listener: tcp, Handler: server, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxTCPQueries: 16, NotifyStartedFunc: notify}
	s.compositeDNS = server
	serve := func(listener *dns.Server) {
		_ = listener.ActivateAndServe()
		server.mu.Lock()
		server.failed = true
		server.mu.Unlock()
		stopped <- struct{}{}
	}
	go serve(server.udp)
	go serve(server.tcp)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for listeners := 0; listeners < 2; listeners++ {
		select {
		case <-started:
		case <-stopped:
			_ = udp.Close()
			_ = tcp.Close()
			return errors.New("agent: managed composite DNS failed to start")
		case <-deadline.C:
			_ = udp.Close()
			_ = tcp.Close()
			server.mu.Lock()
			server.failed = true
			server.mu.Unlock()
			return errors.New("agent: managed composite DNS startup timed out")
		}
	}
	server.mu.Lock()
	failed := server.failed
	server.mu.Unlock()
	if failed {
		_ = udp.Close()
		_ = tcp.Close()
		return errors.New("agent: managed composite DNS stopped during startup")
	}
	return nil
}

func (s *Store) stopCompositeDNS() error {
	s.compositeDNSMu.Lock()
	s.compositeDNSClosed = true
	server := s.compositeDNS
	s.compositeDNSMu.Unlock()
	if server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return errors.Join(server.udp.ShutdownContext(ctx), server.tcp.ShutdownContext(ctx))
}

func exchangeCompositeDNS(ctx context.Context, query *dns.Msg, upstream string) (*dns.Msg, error) {
	client := &dns.Client{Net: "udp", Timeout: 2 * time.Second}
	response, _, err := client.ExchangeContext(ctx, query, upstream)
	if err == nil && response.Truncated {
		response, _, err = (&dns.Client{Net: "tcp", Timeout: 2 * time.Second}).ExchangeContext(ctx, query, upstream)
	}
	return response, err
}

func (s *compositeDNSServer) ServeDNS(writer dns.ResponseWriter, request *dns.Msg) {
	reply := new(dns.Msg)
	reply.SetReply(request)
	reply.RecursionAvailable = true
	host, _, err := net.SplitHostPort(writer.RemoteAddr().String())
	address, parseErr := netip.ParseAddr(host)
	if err != nil || parseErr != nil || !address.IsLoopback() || request.Opcode != dns.OpcodeQuery || len(request.Question) != 1 {
		reply.Rcode = dns.RcodeRefused
		_ = writer.WriteMsg(reply)
		return
	}
	question := request.Question[0]
	if question.Qclass != dns.ClassINET || question.Qtype != dns.TypeA && question.Qtype != dns.TypeAAAA {
		reply.Rcode = dns.RcodeRefused
		_ = writer.WriteMsg(reply)
		return
	}
	name := strings.ToLower(strings.TrimSuffix(question.Name, "."))
	ctx, cancel := context.WithTimeout(context.Background(), 4500*time.Millisecond)
	defer cancel()
	answer := s.lookup(ctx, name)
	reply.Rcode = answer.rcode
	ttl := uint32(max(0, int(answer.expires.Sub(s.now()).Seconds())))
	for _, ip := range answer.addresses {
		header := dns.RR_Header{Name: question.Name, Rrtype: question.Qtype, Class: dns.ClassINET, Ttl: ttl}
		if question.Qtype == dns.TypeA && ip.Is4() {
			reply.Answer = append(reply.Answer, &dns.A{Hdr: header, A: net.IP(ip.AsSlice())})
		}
		if question.Qtype == dns.TypeAAAA && ip.Is6() {
			reply.Answer = append(reply.Answer, &dns.AAAA{Hdr: header, AAAA: net.IP(ip.AsSlice())})
		}
	}
	if len(reply.Answer) == 0 && (reply.Rcode == dns.RcodeSuccess || reply.Rcode == dns.RcodeNameError) {
		reply.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: ttl}, Ns: "ns.invalid.", Mbox: "hostmaster.invalid.", Serial: 1, Minttl: ttl}}
	}
	_ = writer.WriteMsg(reply)
}

func (s *compositeDNSServer) lookup(ctx context.Context, name string) compositeDNSAnswer {
	if !meridian.CompositeHostnameAllowed(name) {
		return compositeDNSAnswer{rcode: dns.RcodeRefused, expires: s.now().Add(time.Second)}
	}
	s.mu.Lock()
	cached, ok := s.cache[name]
	s.mu.Unlock()
	if ok && cached.expires.After(s.now()) {
		return cached
	}
	result, err, _ := s.queries.Do(name, func() (any, error) {
		s.mu.Lock()
		cached, ok = s.cache[name]
		s.mu.Unlock()
		if ok && cached.expires.After(s.now()) {
			return cached, nil
		}
		aaaa, ttl6 := s.resolve(ctx, name, dns.TypeAAAA, nil, 300)
		a := meridian.FamilyAnswer{Status: meridian.DNSNoData}
		ttl := ttl6
		if aaaa.Status == meridian.DNSNoData {
			var ttl4 uint32
			a, ttl4 = s.resolve(ctx, name, dns.TypeA, nil, 300)
			ttl = min(ttl, ttl4)
		}
		selection, selectionErr := meridian.SelectCompositeDestination(name, 443, aaaa, a)
		answer := compositeDNSAnswer{rcode: dns.RcodeServerFailure, expires: s.now().Add(time.Second)}
		if selectionErr == nil {
			answer.addresses = slices.Clone(selection.Addresses)
			answer.rcode = dns.RcodeSuccess
			answer.expires = s.now().Add(time.Duration(ttl) * time.Second)
		}
		if aaaa.Status == meridian.DNSNotFound || aaaa.Status == meridian.DNSNoData && a.Status == meridian.DNSNotFound {
			answer.rcode = dns.RcodeNameError
			answer.expires = s.now().Add(time.Duration(ttl) * time.Second)
		}
		// Failure evidence is bounded; it is never turned into NODATA. Xray
		// queries AAAA first and only advances on confirmed successful-empty.
		if answer.rcode == dns.RcodeServerFailure {
			ttl = 1
		}
		if ttl > 0 {
			s.mu.Lock()
			// Bound memory without extending any answer's lifetime.
			if len(s.cache) >= 4096 {
				for key, value := range s.cache {
					if !value.expires.After(s.now()) {
						delete(s.cache, key)
					}
				}
				if len(s.cache) >= 4096 {
					clear(s.cache)
				}
			}
			s.cache[name] = answer
			s.mu.Unlock()
		}
		return answer, nil
	})
	if err != nil {
		return compositeDNSAnswer{rcode: dns.RcodeServerFailure, expires: s.now().Add(time.Second)}
	}
	return result.(compositeDNSAnswer)
}

func (s *compositeDNSServer) resolve(ctx context.Context, name string, qtype uint16, visited []string, ttl uint32) (meridian.FamilyAnswer, uint32) {
	// Validate labels before issuing a query. Reserved/literal addresses are
	// rejected by the domain authority and never sent to the upstream resolver.
	if !meridian.CompositeHostnameAllowed(name) || len(visited) >= 8 || slices.Contains(visited, name) {
		return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
	}
	query := new(dns.Msg)
	query.SetQuestion(dns.Fqdn(name), qtype)
	var response *dns.Msg
	var err error
	for _, upstream := range s.upstreams {
		response, err = s.exchange(ctx, query, upstream)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return meridian.FamilyAnswer{Status: meridian.DNSTimeout}, 0
		}
	}
	if err != nil {
		var timeout net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
			return meridian.FamilyAnswer{Status: meridian.DNSTimeout}, 0
		}
		return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
	}
	if response == nil || response.Truncated || !response.Response || len(response.Question) != 1 || response.Question[0] != query.Question[0] {
		return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
	}
	negativeConfirmed := false
	for _, record := range response.Ns {
		if soa, ok := record.(*dns.SOA); ok && soa.Hdr.Class == dns.ClassINET && dns.IsSubDomain(soa.Hdr.Name, dns.Fqdn(name)) {
			ttl = min(ttl, soa.Hdr.Ttl, soa.Minttl)
			negativeConfirmed = true
		}
	}
	if response.Rcode == dns.RcodeNameError {
		return meridian.FamilyAnswer{Status: meridian.DNSNotFound}, ttl
	}
	if response.Rcode != dns.RcodeSuccess {
		return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
	}
	current := dns.Fqdn(name)
	for hop := 0; hop < 8; hop++ {
		var addresses []netip.Addr
		alias := ""
		for _, record := range response.Answer {
			if !strings.EqualFold(record.Header().Name, current) {
				continue
			}
			if record.Header().Class != dns.ClassINET {
				return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
			}
			switch value := record.(type) {
			case *dns.AAAA:
				if qtype == dns.TypeAAAA {
					if ip, ok := netip.AddrFromSlice(value.AAAA); ok {
						addresses = append(addresses, ip)
						ttl = min(ttl, value.Hdr.Ttl)
					}
				}
			case *dns.A:
				if qtype == dns.TypeA {
					if ip, ok := netip.AddrFromSlice(value.A); ok {
						addresses = append(addresses, ip.Unmap())
						ttl = min(ttl, value.Hdr.Ttl)
					}
				}
			case *dns.CNAME:
				if alias != "" && alias != value.Target {
					return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
				}
				alias = value.Target
				ttl = min(ttl, value.Hdr.Ttl)
			}
		}
		if len(addresses) > 0 {
			if alias != "" {
				return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
			}
			return meridian.FamilyAnswer{Status: meridian.DNSSuccess, Addresses: addresses}, ttl
		}
		if alias == "" {
			if current != dns.Fqdn(name) {
				return s.resolve(ctx, strings.ToLower(strings.TrimSuffix(current, ".")), qtype, append(visited, name), ttl)
			}
			if !negativeConfirmed {
				return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
			}
			return meridian.FamilyAnswer{Status: meridian.DNSNoData}, ttl
		}
		if strings.EqualFold(alias, current) {
			return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
		}
		current = alias
	}
	return meridian.FamilyAnswer{Status: meridian.DNSFailure}, 0
}
