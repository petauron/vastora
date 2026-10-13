package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/miekg/dns"
	"github.com/petauron/glivela"
	"golang.org/x/net/proxy"
)

// This is an opt-in, real-core lab, not a mocked Xray receipt. Run the compiled
// test inside an isolated Linux network namespace with only loopback enabled.
// Public-shaped target literals are policy fixtures; peers map them to local
// IPv4/IPv6 TLS targets and MUST NOT dial an external destination.
func TestCompositeRuntimeLab(t *testing.T) {
	if os.Getenv("GLIVELA_COMPOSITE_LAB") != "1" {
		t.Skip("separate controlled runtime acceptance requires GLIVELA_COMPOSITE_LAB=1")
	}
	binaryPath := os.Getenv("GLIVELA_XRAY_BINARY")
	if !filepath.IsAbs(binaryPath) {
		t.Fatal("an explicit audited Xray binary is required")
	}
	version, err := exec.Command(binaryPath, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "Xray ") {
		t.Fatal("controlled lab requires an explicitly supplied official Xray binary")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback == 0 {
			t.Fatal("controlled lab requires a disposable network namespace with only loopback")
		}
	}
	var targetMu sync.Mutex
	targetFamilies := []string{}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "anonymous lab"}, DNSNames: []string{"*.example.com"}, IPAddresses: []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("2001:4860:4860::8888")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	parsed, _ := x509.ParseCertificate(der)
	pool.AddCert(parsed)
	targetListener, err := net.Listen("tcp", "[::]:0")
	if err != nil {
		t.Fatal("lab requires loopback IPv6")
	}
	targetPort := targetListener.Addr().(*net.TCPAddr).Port
	targetServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expectedHost, _, _ := net.SplitHostPort(r.Host)
		if _, err := netip.ParseAddr(expectedHost); err == nil {
			expectedHost = ""
		}
		if r.TLS == nil || r.TLS.ServerName != expectedHost {
			t.Error("TLS SNI changed")
		}
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		family := "ipv6"
		if netip.MustParseAddr(host).Unmap().Is4() {
			family = "ipv4"
		}
		targetMu.Lock()
		targetFamilies = append(targetFamilies, family)
		targetMu.Unlock()
		status := 200
		if r.URL.Path == "/401" {
			status = 401
		}
		if r.URL.Path == "/403" {
			status = 403
		}
		if r.URL.Path == "/404" {
			status = 404
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "anonymous target response")
	}), ReadHeaderTimeout: 3 * time.Second}
	go func() {
		_ = targetServer.Serve(tls.NewListener(targetListener, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}))
	}()
	t.Cleanup(func() { _ = targetServer.Close() })
	six := newCompositeLabPeer(t, false, targetPort)
	four := newCompositeLabPeer(t, true, targetPort)
	changed := atomic.Bool{}
	// This is only an anonymous DNS fixture. Production uses official Xray's
	// DNS client directly, not an Agent resolver or evidence-selection service.
	resolver := dns.HandlerFunc(func(writer dns.ResponseWriter, query *dns.Msg) {
		q := query.Question[0]
		name := strings.TrimSuffix(q.Name, ".")
		reply := new(dns.Msg)
		reply.SetReply(query)
		if name == "timeout.example.com" {
			return
		}
		if name == "servfail.example.com" {
			reply.Rcode = dns.RcodeServerFailure
			_ = writer.WriteMsg(reply)
			return
		}
		if name == "missing.example.com" {
			reply.Rcode = dns.RcodeNameError
			_ = writer.WriteMsg(reply)
			return
		}
		if name == "alias.example.com" {
			reply.Answer = append(reply.Answer, &dns.CNAME{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 1}, Target: "dual.example.com."})
			q.Name = "dual.example.com."
		}
		isSix := name != "v4.example.com" && (name != "change.example.com" || changed.Load())
		if q.Qtype == dns.TypeAAAA && isSix {
			ip := "2001:4860:4860::8888"
			if name == "private.example.com" {
				ip = "fd00::1"
			}
			reply.Answer = append(reply.Answer, &dns.AAAA{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 1}, AAAA: net.ParseIP(ip)})
		}
		if q.Qtype == dns.TypeA && name != "v6.example.com" {
			reply.Answer = append(reply.Answer, &dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 1}, A: net.ParseIP("1.1.1.1")})
		}
		_ = writer.WriteMsg(reply)
	})
	dnsListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	dnsServer := &dns.Server{Listener: dnsListener, Handler: resolver, NotifyStartedFunc: func() { close(started) }}
	go func() { _ = dnsServer.ActivateAndServe() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("DNS fixture did not start")
	}
	t.Cleanup(func() { _ = dnsServer.Shutdown() })
	baseID, routedID := uuid.NewString(), uuid.NewString()
	base := meridian.Credential{ID: "base", AccountID: "account", EntryID: "entry", User: "native", Kind: meridian.NativeCredential, Identity: meridian.Identity(baseID), Enabled: true}
	routed := meridian.Credential{ID: "route", AccountID: "account", EntryID: "entry", EgressID: "six", User: "routed", Kind: meridian.RouteCredential, Identity: meridian.Identity(routedID), Enabled: true}
	plan := meridian.XrayPlan{Revision: 1,
		RealityEndpoints: []meridian.RealityEndpoint{{ID: "endpoint", EntryID: "entry", InboundTag: "entry", ListenAddress: "100.64.0.2", ListenPort: 10443, AdvertiseHost: "entry.example.com", AdvertisePort: 443, Target: "dual.example.com:443", ServerNames: []string{"dual.example.com"}, PrivateKey: "fixture", PublicKey: "fixture", ShortIDs: []string{"0123456789abcdef"}}},
		Credentials:      []meridian.CredentialMaterial{{Credential: base, ProtocolID: baseID}, {Credential: routed, ProtocolID: routedID}},
		Grants:           []meridian.RouteGrant{{ID: "combo", AccountID: "account", EntryID: "entry", EgressID: "six", IPv4EgressID: "four", InboundTag: "entry", Base: base, Route: routed, Mode: meridian.FixedMode, Enabled: true, DesiredRev: 1, AppliedRev: 1, RuntimeGood: true}},
		Peers:            []meridian.RoutePeer{{EgressID: "six", Address: "100.64.0.6", Port: 1080, ExitFamily: meridian.EgressIPv6Only, Ready: true}, {EgressID: "four", Address: "100.64.0.4", Port: 1080, ExitFamily: meridian.EgressIPv4Only, Ready: true}},
	}
	artifact, err := meridian.BuildDesiredArtifact(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	var config map[string]any
	if err := json.Unmarshal(artifact.Config, &config); err != nil {
		t.Fatal(err)
	}
	// The lab rebases sockets and removes REALITY only. It does not alter the
	// generated routing rules, counter policy, credential scope, or guards.
	apiPort, entryPort, clientPort := compositeLabPort(t), compositeLabPort(t), compositeLabPort(t)
	inbounds := config["inbounds"].([]any)
	inbounds[0].(map[string]any)["port"] = apiPort
	entryInbound := inbounds[1].(map[string]any)
	entryInbound["listen"], entryInbound["port"] = "127.0.0.1", entryPort
	delete(entryInbound, "streamSettings")
	for _, value := range entryInbound["settings"].(map[string]any)["clients"].([]any) {
		delete(value.(map[string]any), "flow")
	}
	for _, value := range config["outbounds"].([]any) {
		out := value.(map[string]any)
		if out["protocol"] != "socks" {
			continue
		}
		peer := six
		if strings.HasSuffix(out["tag"].(string), "-ipv4") {
			peer = four
		}
		server := out["settings"].(map[string]any)["servers"].([]any)[0].(map[string]any)
		server["address"], server["port"] = "127.0.0.1", peer.listener.Addr().(*net.TCPAddr).Port
	}
	// Disable fixture caching to observe an explicit DNS family change. This
	// does not assert custom/atomic DNS evidence or production cache semantics.
	config["dns"].(map[string]any)["servers"] = []any{map[string]any{"address": "tcp+local://" + dnsListener.Addr().String(), "disableCache": true}}
	stopServer := startCompositeLabCore(t, binaryPath, directory, "server", config, entryPort)
	clientConfig := map[string]any{"log": map[string]any{"loglevel": "warning"}, "inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": clientPort, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}}, "outbounds": []any{map[string]any{"protocol": "vless", "settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": entryPort, "users": []any{map[string]any{"id": routedID, "encryption": "none"}}}}}}}}
	startCompositeLabCore(t, binaryPath, directory, "client", clientConfig, clientPort)
	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(clientPort)), nil, &net.Dialer{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	request := func(target, path, expected string, status int) {
		t.Helper()
		before6, before4 := six.count(), four.count()
		response, requestErr := client.Get("https://" + net.JoinHostPort(target, strconv.Itoa(targetPort)) + path)
		if expected == "blocked" {
			if response != nil {
				_ = response.Body.Close()
			}
			if requestErr == nil || six.count() != before6 || four.count() != before4 {
				t.Fatal("blocked selection reached an exit or returned success")
			}
			return
		}
		if requestErr != nil {
			t.Fatalf("controlled %s TLS request failed: %v (selected-peer attempts ipv6=%d ipv4=%d)", expected, requestErr, six.count()-before6, four.count()-before4)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != status {
			t.Fatal("TLS and HTTP status were conflated")
		}
		want6, want4 := before6, before4
		if expected == "ipv6" {
			want6++
		} else {
			want4++
		}
		if six.count() != want6 || four.count() != want4 {
			t.Fatal("wrong exit, duplicate transmission, or cross-exit HTTP retry")
		}
		targetMu.Lock()
		observed := targetFamilies[len(targetFamilies)-1]
		targetMu.Unlock()
		if observed != expected {
			t.Fatal("target-side socket family differs from the selected exit")
		}
	}
	beforeStats := compositeLabStats(t, binaryPath, apiPort, plan.Credentials[1])
	// Observe an open, completed HTTP exchange. Closing a TLS connection may
	// legitimately accept a close-notify at ingress after the target has closed;
	// those discarded bytes are not evidence of a second statistics wrapper.
	ordinaryClient := client
	meterTransport := transport.Clone()
	meterTransport.DisableKeepAlives = false
	t.Cleanup(meterTransport.CloseIdleConnections)
	client = &http.Client{Transport: meterTransport, Timeout: 5 * time.Second}
	request("dual.example.com", "/", "ipv6", 200)
	afterStats := compositeLabStats(t, binaryPath, apiPort, plan.Credentials[1])
	if afterStats.UpBytes-beforeStats.UpBytes != six.up.Load() || afterStats.DownBytes-beforeStats.DownBytes != six.down.Load() {
		t.Fatalf("user traffic was not counted exactly once: core up/down=%d/%d, peer up/down=%d/%d", afterStats.UpBytes-beforeStats.UpBytes, afterStats.DownBytes-beforeStats.DownBytes, six.up.Load(), six.down.Load())
	}
	meterTransport.CloseIdleConnections()
	client = ordinaryClient
	deadline := time.Now().Add(time.Second)
	for six.finished.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if six.finished.Load() != 1 {
		t.Fatal("counter observation did not finish")
	}
	for _, test := range []struct{ target, family string }{{"v6.example.com", "ipv6"}, {"v4.example.com", "ipv4"}, {"alias.example.com", "ipv6"}, {"1.1.1.1", "ipv4"}, {"2001:4860:4860::8888", "ipv6"}} {
		request(test.target, "/", test.family, 200)
	}
	for _, status := range []int{401, 403, 404} {
		request("dual.example.com", "/"+strconv.Itoa(status), "ipv6", status)
	}
	for _, target := range []string{"missing.example.com", "servfail.example.com", "timeout.example.com", "private.example.com", "198.18.0.1", "127.0.0.1", "::1"} {
		request(target, "/", "blocked", 0)
	}
	request("change.example.com", "/", "ipv4", 200)
	changed.Store(true)
	request("change.example.com", "/", "ipv6", 200)
	stopServer()
	startCompositeLabCore(t, binaryPath, directory, "server", config, entryPort)
	request("dual.example.com", "/", "ipv6", 200)
	request("v4.example.com", "/", "ipv4", 200)
	// Transport failure with AAAA must attempt only the selected IPv6 peer.
	six.fail.Store(true)
	before6, before4 := six.count(), four.count()
	response, requestErr := client.Get("https://" + net.JoinHostPort("dual.example.com", strconv.Itoa(targetPort)) + "/")
	if response != nil {
		_ = response.Body.Close()
	}
	if requestErr == nil || six.count() != before6+1 || four.count() != before4 {
		t.Fatal("IPv6 failure was replayed through IPv4")
	}
	four.fail.Store(true)
	before6, before4 = six.count(), four.count()
	response, requestErr = client.Get("https://" + net.JoinHostPort("v4.example.com", strconv.Itoa(targetPort)) + "/")
	if response != nil {
		_ = response.Body.Close()
	}
	if requestErr == nil || four.count() != before4+1 || six.count() != before6 {
		t.Fatal("IPv4 failure escaped to another exit")
	}
}

type compositeLabPeer struct {
	listener           net.Listener
	four               bool
	fail               atomic.Bool
	attempts           atomic.Int64
	up, down, finished atomic.Int64
}

func (p *compositeLabPeer) count() int64 { return p.attempts.Load() }
func newCompositeLabPeer(t *testing.T, four bool, targetPort int) *compositeLabPeer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &compositeLabPeer{listener: listener, four: four}
	var active sync.WaitGroup
	acceptStopped := make(chan struct{})
	t.Cleanup(func() { _ = listener.Close(); <-acceptStopped; active.Wait() })
	go func() {
		defer close(acceptStopped)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			active.Add(1)
			go func() {
				defer active.Done()
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(8 * time.Second))
				greeting := make([]byte, 2)
				if _, err := io.ReadFull(connection, greeting); err != nil {
					return
				}
				if greeting[0] != 5 {
					return
				}
				if _, err := io.CopyN(io.Discard, connection, int64(greeting[1])); err != nil {
					return
				}
				_, _ = connection.Write([]byte{5, 0})
				header := make([]byte, 4)
				if _, err := io.ReadFull(connection, header); err != nil {
					return
				}
				size := 4
				if header[3] == 4 {
					size = 16
				} else if header[3] != 1 {
					t.Error("SOCKS received an unpinned domain")
					return
				}
				ip := make([]byte, size)
				if _, err := io.ReadFull(connection, ip); err != nil {
					return
				}
				port := make([]byte, 2)
				if _, err := io.ReadFull(connection, port); err != nil {
					return
				}
				address, _ := netip.AddrFromSlice(ip)
				want := netip.MustParseAddr("2001:4860:4860::8888")
				if four {
					want = netip.MustParseAddr("1.1.1.1")
				}
				if address != want || int(binary.BigEndian.Uint16(port)) != targetPort {
					t.Error("exit received wrong family or unsafe target")
					return
				}
				p.attempts.Add(1)
				if p.fail.Load() {
					_, _ = connection.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				host, network := "::1", "tcp6"
				if four {
					host, network = "127.0.0.1", "tcp4"
				}
				target, err := net.DialTimeout(network, net.JoinHostPort(host, strconv.Itoa(targetPort)), time.Second)
				if err != nil {
					t.Error("anonymous target unavailable")
					return
				}
				defer target.Close()
				_ = target.SetDeadline(time.Now().Add(8 * time.Second))
				_, _ = connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				copied := make(chan struct{}, 1)
				go func() { _, _ = io.Copy(target, compositeLabMeter{connection, &p.up}); copied <- struct{}{} }()
				_, _ = io.Copy(connection, compositeLabMeter{target, &p.down})
				_ = connection.Close()
				<-copied
				p.finished.Add(1)
			}()
		}
	}()
	return p
}

type compositeLabMeter struct {
	io.Reader
	bytes *atomic.Int64
}

func (m compositeLabMeter) Read(p []byte) (int, error) {
	n, err := m.Reader.Read(p)
	m.bytes.Add(int64(n))
	return n, err
}

func compositeLabPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}
func startCompositeLabCore(t *testing.T, binaryPath, directory, name string, config map[string]any, port int) func() {
	t.Helper()
	contents, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(directory, name+".json")
	if err := os.WriteFile(filename, contents, 0600); err != nil {
		t.Fatal(err)
	}
	validation, cancelValidation := context.WithTimeout(context.Background(), 10*time.Second)
	err = exec.CommandContext(validation, binaryPath, "run", "-test", "-c", filename).Run()
	cancelValidation()
	if err != nil {
		t.Fatal("audited core rejected controlled configuration")
	}
	ctx, cancel := context.WithCancel(context.Background())
	command := exec.CommandContext(ctx, binaryPath, "run", "-c", filename)
	ready := make(chan struct{})
	diagnostics := &compositeLabDiagnostics{ready: ready}
	command.Stdout = diagnostics
	command.Stderr = diagnostics
	if err := command.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { _ = command.Wait(); close(stopped) }()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); <-stopped }) }
	t.Cleanup(stop)
	// Wait for the core's completed-start signal, not merely an open TCP port.
	select {
	case <-ready:
	case <-stopped:
		t.Fatal("audited core stopped during initialization")
	case <-time.After(5 * time.Second):
		t.Fatal("audited core initialization did not complete")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return stop
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("audited core failed to become ready")
	return stop
}

// Observe startup only; never retain raw core logs or identities.
type compositeLabDiagnostics struct {
	ready chan struct{}
	once  sync.Once
}

func (d *compositeLabDiagnostics) Write(contents []byte) (int, error) {
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.Contains(line, "Xray ") && strings.Contains(line, " started") {
			d.once.Do(func() { close(d.ready) })
		}
	}
	return len(contents), nil
}

func compositeLabStats(t *testing.T, binaryPath string, port int, material meridian.CredentialMaterial) meridian.CounterSnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binaryPath, "api", "statsquery", "--server="+net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "-pattern", "user>>>routed>>>traffic", "-reset=false").Output()
	if err != nil {
		t.Fatal("controlled statistics unavailable")
	}
	counters, err := meridian.ParseXrayUserCounters([]meridian.CredentialMaterial{material}, output)
	if err != nil || len(counters) != 1 {
		t.Fatal("invalid controlled counters")
	}
	return counters[0]
}
