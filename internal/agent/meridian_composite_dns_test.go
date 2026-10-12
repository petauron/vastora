package agent

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func compositeDNSFixture(now *time.Time, exchange func(context.Context, *dns.Msg, string) (*dns.Msg, error)) *compositeDNSServer {
	return &compositeDNSServer{cache: map[string]compositeDNSAnswer{}, upstreams: []string{"fixture:53"}, now: func() time.Time { return *now }, exchange: exchange}
}

func compositeDNSReply(query *dns.Msg, rcode int, answers ...dns.RR) *dns.Msg {
	reply := new(dns.Msg)
	reply.SetReply(query)
	reply.Rcode = rcode
	reply.Answer = answers
	reply.Ns = []dns.RR{&dns.SOA{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 20}, Ns: "ns.example.com.", Mbox: "hostmaster.example.com.", Minttl: 20}}
	return reply
}

func TestCompositeDNSUsesAAAAEvidenceBeforeAnyAQuery(t *testing.T) {
	for _, test := range []struct {
		name        string
		rcode       int
		address     string
		wantA       bool
		wantFailure bool
	}{
		{"dual stack", dns.RcodeSuccess, "2001:4860:4860::8888", false, false},
		{"A only", dns.RcodeSuccess, "", true, false},
		{"SERVFAIL with A available", dns.RcodeServerFailure, "", false, true},
		{"NXDOMAIN", dns.RcodeNameError, "", false, true},
		{"unsafe AAAA", dns.RcodeSuccess, "fd00::1", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Now()
			aQueries := 0
			server := compositeDNSFixture(&now, func(_ context.Context, query *dns.Msg, _ string) (*dns.Msg, error) {
				q := query.Question[0]
				if q.Qtype == dns.TypeA {
					aQueries++
					return compositeDNSReply(query, dns.RcodeSuccess, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.ParseIP("1.1.1.1")}), nil
				}
				var answers []dns.RR
				if test.address != "" {
					answers = append(answers, &dns.AAAA{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 30}, AAAA: net.ParseIP(test.address)})
				}
				return compositeDNSReply(query, test.rcode, answers...), nil
			})
			answer := server.lookup(context.Background(), "www.example.com")
			if (aQueries > 0) != test.wantA || (answer.rcode != dns.RcodeSuccess) != test.wantFailure {
				t.Fatalf("rcode=%d A queries=%d", answer.rcode, aQueries)
			}
			if !test.wantFailure && (len(answer.addresses) != 1 || answer.addresses[0].Is4() != test.wantA) {
				t.Fatal("selected DNS answer does not pin one family")
			}
		})
	}
}

func TestCompositeDNSCacheHonorsPositiveAndNegativeTTLAndFamilyChange(t *testing.T) {
	now := time.Now()
	six := false
	queries := 0
	server := compositeDNSFixture(&now, func(_ context.Context, query *dns.Msg, _ string) (*dns.Msg, error) {
		queries++
		q := query.Question[0]
		if q.Qtype == dns.TypeAAAA && six {
			return compositeDNSReply(query, 0, &dns.AAAA{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 30}, AAAA: net.ParseIP("2001:4860:4860::8888")}), nil
		}
		if q.Qtype == dns.TypeAAAA {
			return compositeDNSReply(query, 0), nil
		}
		return compositeDNSReply(query, 0, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 30}, A: net.ParseIP("1.1.1.1")}), nil
	})
	first := server.lookup(context.Background(), "www.example.com")
	if len(first.addresses) != 1 || !first.addresses[0].Is4() || first.expires.Sub(now) != 20*time.Second {
		t.Fatal("AAAA negative TTL was not the composite cache boundary")
	}
	six = true
	now = now.Add(19 * time.Second)
	if answer := server.lookup(context.Background(), "www.example.com"); !answer.addresses[0].Is4() || queries != 2 {
		t.Fatal("fresh selection was re-resolved")
	}
	now = now.Add(2 * time.Second)
	if answer := server.lookup(context.Background(), "www.example.com"); !answer.addresses[0].Is6() || queries != 3 {
		t.Fatal("expired NODATA pinned the old family")
	}
}

func TestCompositeDNSCNAMEAndTimeoutAreNotFallback(t *testing.T) {
	now := time.Now()
	requests := 0
	server := compositeDNSFixture(&now, func(_ context.Context, query *dns.Msg, _ string) (*dns.Msg, error) {
		requests++
		q := query.Question[0]
		if q.Name == "www.example.com." {
			return compositeDNSReply(query, 0, &dns.CNAME{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 10}, Target: "alias.example.com."}), nil
		}
		return compositeDNSReply(query, 0, &dns.AAAA{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 30}, AAAA: net.ParseIP("2001:4860:4860::8888")}), nil
	})
	answer := server.lookup(context.Background(), "www.example.com")
	if requests != 2 || answer.rcode != 0 || answer.expires.Sub(now) != 10*time.Second {
		t.Fatal("CNAME target or TTL not honored")
	}
	server = compositeDNSFixture(&now, func(_ context.Context, query *dns.Msg, _ string) (*dns.Msg, error) {
		if query.Question[0].Qtype == dns.TypeA {
			t.Error("AAAA timeout triggered an A query")
		}
		return nil, context.DeadlineExceeded
	})
	if answer := server.lookup(context.Background(), "www.example.com"); answer.rcode != dns.RcodeServerFailure || len(answer.addresses) != 0 {
		t.Fatal("timeout escaped fail closed")
	}
}

func TestCompositeDNSRejectsUnconfirmedNODATA(t *testing.T) {
	now := time.Now()
	for _, test := range []string{"no SOA", "unrelated SOA", "blocked A", "CNAME cycle", "unrelated answer"} {
		t.Run(test, func(t *testing.T) {
			server := compositeDNSFixture(&now, func(_ context.Context, query *dns.Msg, _ string) (*dns.Msg, error) {
				q := query.Question[0]
				reply := compositeDNSReply(query, 0)
				switch test {
				case "no SOA":
					reply.Ns = nil
				case "unrelated SOA":
					reply.Ns[0].Header().Name = "other.invalid."
				case "blocked A":
					if q.Qtype == dns.TypeA {
						reply.Answer = []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 20}, A: net.ParseIP("169.254.169.254")}}
					}
				case "CNAME cycle":
					reply.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 20}, Target: q.Name}}
				case "unrelated answer":
					reply.Ns = nil
					reply.Answer = []dns.RR{&dns.AAAA{Hdr: dns.RR_Header{Name: "other.example.com.", Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 20}, AAAA: net.ParseIP("2001:4860:4860::8888")}}
				}
				return reply, nil
			})
			if answer := server.lookup(context.Background(), "www.example.com"); answer.rcode != dns.RcodeServerFailure {
				t.Fatal("untrusted DNS evidence accepted")
			}
		})
	}
	server := compositeDNSFixture(&now, func(context.Context, *dns.Msg, string) (*dns.Msg, error) {
		t.Error("private or literal hostname reached public DNS")
		return nil, errors.New("unexpected")
	})
	for _, name := range []string{"localhost", "service.internal", "198.18.0.1", "bad..example.com", ""} {
		if answer := server.lookup(context.Background(), name); answer.rcode != dns.RcodeRefused {
			t.Fatal("invalid hostname not rejected")
		}
	}
}

func TestCompositeDNSZeroTTLIsUsableButNeverCached(t *testing.T) {
	now := time.Now()
	queries := 0
	server := compositeDNSFixture(&now, func(_ context.Context, query *dns.Msg, _ string) (*dns.Msg, error) {
		queries++
		return compositeDNSReply(query, 0, &dns.AAAA{Hdr: dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 0}, AAAA: net.ParseIP("2001:4860:4860::8888")}), nil
	})
	for i := 0; i < 2; i++ {
		answer := server.lookup(context.Background(), "www.example.com")
		if answer.rcode != dns.RcodeSuccess || len(answer.addresses) != 1 || !answer.expires.Equal(now) {
			t.Fatal("zero TTL was rejected or extended")
		}
	}
	if queries != 2 || len(server.cache) != 0 {
		t.Fatal("zero-TTL evidence was cached")
	}
}

type compositeDNSWriter struct{ reply *dns.Msg }

func (w *compositeDNSWriter) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10086}
}
func (w *compositeDNSWriter) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
}
func (w *compositeDNSWriter) WriteMsg(reply *dns.Msg) error     { w.reply = reply.Copy(); return nil }
func (w *compositeDNSWriter) Write(payload []byte) (int, error) { return len(payload), nil }
func (w *compositeDNSWriter) Close() error                      { return nil }
func (w *compositeDNSWriter) TsigStatus() error                 { return nil }
func (w *compositeDNSWriter) TsigTimersOnly(bool)               {}
func (w *compositeDNSWriter) Hijack()                           {}

func TestCompositeDNSWireQueryCannotMergeChangedFamilies(t *testing.T) {
	now := time.Now()
	six := false
	aQueries := 0
	server := compositeDNSFixture(&now, func(_ context.Context, query *dns.Msg, _ string) (*dns.Msg, error) {
		q := query.Question[0]
		if q.Qtype == dns.TypeAAAA && six {
			return compositeDNSReply(query, 0, &dns.AAAA{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 1}, AAAA: net.ParseIP("2001:4860:4860::8888")}), nil
		}
		if q.Qtype == dns.TypeA {
			aQueries++
			return compositeDNSReply(query, 0, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 1}, A: net.ParseIP("1.1.1.1")}), nil
		}
		return compositeDNSReply(query, 0), nil
	})
	question := new(dns.Msg)
	question.SetQuestion("www.example.com.", dns.TypeAAAA)
	writer := new(compositeDNSWriter)
	server.ServeDNS(writer, question)
	if writer.reply.Rcode != dns.RcodeSuccess || len(writer.reply.Answer) != 0 || len(writer.reply.Ns) != 1 || aQueries != 1 {
		t.Fatal("A-only target was not confirmed NODATA on the wire")
	}
	now = now.Add(2 * time.Second)
	six = true
	question.SetQuestion("www.example.com.", dns.TypeA)
	server.ServeDNS(writer, question)
	if writer.reply.Rcode != dns.RcodeSuccess || len(writer.reply.Answer) != 0 || aQueries != 1 {
		t.Fatal("expired A-only evidence escaped into a fresh IPv6 selection")
	}
	question.SetQuestion("www.example.com.", dns.TypeAAAA)
	server.ServeDNS(writer, question)
	if writer.reply.Rcode != dns.RcodeSuccess || len(writer.reply.Answer) != 1 {
		t.Fatal("fresh IPv6 selection was not returned")
	}
}

func TestCompositeXrayCapabilityCheckFailsBeforeConfigSwitch(t *testing.T) {
	for _, test := range []struct {
		banner    string
		supported bool
	}{
		{"Xray 26.10.10 (Xray, Penetrates Everything.)\nGlivelaTargetSelectionRevision: 1", true},
		{"Xray 26.10.11\nGlivelaTargetSelectionRevision: 1", true}, {"Xray 27.1.1\nGlivelaTargetSelectionRevision: 1", true},
		{"Xray 26.10.10", false}, {"Xray 27.1.1", false},
		{"Xray 26.10.10\nGlivelaTargetSelectionRevision: 2", false},
		{"Xray 26.9.8\nGlivelaTargetSelectionRevision: 1", false},
		{"Xray 26.9.8", false}, {"Xray 26.10.9", false}, {"Xray 26.10", false}, {"", false},
	} {
		if compositeXrayCapabilitiesSupported([]byte(test.banner)) != test.supported {
			t.Fatalf("capability result for %q", test.banner)
		}
	}
}
