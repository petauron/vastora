package landing

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.org/x/net/proxy"
)

// Probe runs from the proxy host, outside the Docker forwarding gate. This
// allows fresh recovery probes while the selected application's data remains
// blocked. It never opens a direct connection to a public probe destination.
type Probe struct{ TCPOnly bool }

func (p Probe) Check(ctx context.Context, peer PeerIdentity, revision uint64) (BusinessResult, error) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	return p.check(ctx, peer, revision)
}

func (p Probe) check(ctx context.Context, peer PeerIdentity, revision uint64) (BusinessResult, error) {
	result := BusinessResult{Peer: peer, Revision: revision, StartedAt: time.Now().UTC()}
	address, err := netip.ParseAddr(peer.Address)
	if err != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(address) || peer.ID == "" || peer.PublicKey == "" || revision == 0 {
		return result, errors.New("landing: invalid business probe identity")
	}
	endpoint := net.JoinHostPort(peer.Address, "1080")
	exit, err := p.tcp(ctx, endpoint)
	if err != nil {
		return result, err
	}
	result.TCP, result.ExitIPv4 = true, exit
	if p.TCPOnly {
		result.CheckedAt = time.Now().UTC()
		return result, nil
	}
	relay, err := p.udp(ctx, endpoint)
	if err != nil {
		return result, err
	}
	result.UDP, result.UDPRelay, result.CheckedAt = true, relay, time.Now().UTC()
	return result, nil
}

func (p Probe) tcp(ctx context.Context, endpoint string) (exit string, err error) {
	var stage atomic.Value
	stage.Store("socks_connect")
	defer func() {
		if err != nil {
			err = probeFailure(stage.Load().(string), err)
		}
	}()
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		TLSHandshakeStart: func() { stage.Store("tls_handshake") },
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				stage.Store("http_response")
			}
		},
	})
	// This transport handles one request only. Use the bounded request context
	// explicitly: net/http detaches the context passed to DialContext from the
	// request deadline so a connection can normally be reused by other requests.
	// A pending SOCKS handshake must not outlive this probe's 3s/8s budget.
	transport := &http.Transport{DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
		return p.dialTCP(ctx, endpoint, network, address)
	}, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// The domain is passed unchanged to SOCKS. TLS certificate verification is
	// retained; a local DNS lookup or forged relay response is not a proof.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.cloudflare.com/cdn-cgi/trace", nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	stage.Store("http_response")
	if response.StatusCode != http.StatusOK {
		return "", &probeError{stage: "http_response", reason: "status_" + strconv.Itoa(response.StatusCode)}
	}
	stage.Store("http_body")
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return "", err
	}
	if len(body) > 4096 {
		return "", errors.New("landing: invalid exit probe response")
	}
	stage.Store("exit_address")
	return traceExit(body)
}

// Use the existing maintained SOCKS dialer; authorization is enforced by
// exact source membership on the private Dante endpoint.
func (p Probe) dialTCP(ctx context.Context, endpoint, network, address string) (net.Conn, error) {
	if network != "tcp" || address != "www.cloudflare.com:443" {
		return nil, errors.New("landing: invalid TCP probe destination")
	}
	dialer, err := proxy.SOCKS5("tcp", endpoint, nil, &net.Dialer{})
	if err != nil {
		return nil, err
	}
	contextual, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("landing: SOCKS context support is unavailable")
	}
	return contextual.DialContext(ctx, network, address)
}

func traceExit(body []byte) (string, error) {
	var exit string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "ip=") {
			ip, err := netip.ParseAddr(strings.TrimPrefix(line, "ip="))
			if err != nil || !publicIPv4(ip) || exit != "" {
				return "", errors.New("landing: invalid exit address")
			}
			exit = ip.String()
		}
	}
	if exit == "" {
		return "", errors.New("landing: missing exit address")
	}
	return exit, nil
}

// x/net/proxy implements SOCKS CONNECT but not UDP ASSOCIATE. This bounded
// RFC 1928 exchange covers only the configured private IPv4 relay range; it is
// not a general-purpose SOCKS client and cannot follow arbitrary relay hosts.
func (p Probe) udp(ctx context.Context, endpoint string) (relay string, err error) {
	stage := "udp_control_connect"
	defer func() {
		if err != nil {
			err = probeFailure(stage, err)
		}
	}()
	control, err := (&net.Dialer{}).DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return "", err
	}
	defer control.Close()
	stop := context.AfterFunc(ctx, func() { _ = control.Close() })
	defer stop()
	deadline, ok := ctx.Deadline()
	if !ok {
		return "", errors.New("landing: UDP probe requires a deadline")
	}
	if err := control.SetDeadline(deadline); err != nil {
		return "", err
	}
	stage = "udp_socks_negotiate"
	if err := negotiateSOCKS(control); err != nil {
		return "", err
	}
	stage = "udp_associate"
	if err := writeSOCKS(control, []byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return "", err
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(control, reply[:4]); err != nil {
		return "", err
	}
	if reply[0] != 5 || reply[1] != 0 || reply[2] != 0 || reply[3] != 1 {
		return "", errors.New("landing: UDP relay rejected")
	}
	if _, err := io.ReadFull(control, reply[4:]); err != nil {
		return "", err
	}
	bound := netip.AddrPortFrom(netip.AddrFrom4([4]byte(reply[4:8])), binary.BigEndian.Uint16(reply[8:]))
	stage = "udp_relay_validation"
	host, _, splitErr := net.SplitHostPort(endpoint)
	if splitErr != nil || !validUDPRelay(bound.String(), host) {
		return "", errors.New("landing: unexpected UDP relay")
	}
	stage = "udp_connect"
	connection, err := (&net.Dialer{}).DialContext(ctx, "udp4", bound.String())
	if err != nil {
		return "", err
	}
	defer connection.Close()
	stopUDP := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopUDP()
	if err := connection.SetDeadline(deadline); err != nil {
		return "", err
	}
	var identifier [2]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return "", err
	}
	question := dnsmessage.Question{Name: dnsmessage.MustNewName("www.cloudflare.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: binary.BigEndian.Uint16(identifier[:]), RecursionDesired: true}, Questions: []dnsmessage.Question{question}}
	packet, err := query.Pack()
	if err != nil {
		return "", err
	}
	header := []byte{0, 0, 0, 1, 1, 1, 1, 1, 0, 53}
	stage = "udp_dns_exchange"
	if err := writeSOCKS(connection, append(header, packet...)); err != nil {
		return "", err
	}
	buffer := make([]byte, 4097)
	n, err := connection.Read(buffer)
	if err != nil {
		return "", err
	}
	stage = "udp_dns_response"
	if n > 4096 || n <= len(header) || string(buffer[:len(header)]) != string(header) {
		return "", errors.New("landing: invalid UDP payload")
	}
	var answer dnsmessage.Message
	if err := answer.Unpack(buffer[len(header):n]); err != nil || answer.ID != query.ID || !answer.Response || answer.Truncated || answer.RCode != dnsmessage.RCodeSuccess || len(answer.Questions) != 1 || answer.Questions[0] != question {
		return "", errors.New("landing: UDP DNS exchange mismatch")
	}
	for _, resource := range answer.Answers {
		if record, ok := resource.Body.(*dnsmessage.AResource); ok && publicIPv4(netip.AddrFrom4(record.A)) {
			return bound.String(), nil
		}
	}
	return "", errors.New("landing: UDP DNS exchange returned no public answer")
}

func negotiateSOCKS(connection io.ReadWriter) error {
	if err := writeSOCKS(connection, []byte{5, 1, 0}); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		return err
	}
	if reply != [2]byte{5, 0} {
		return errors.New("landing: SOCKS negotiation rejected")
	}
	return nil
}

func writeSOCKS(writer io.Writer, data []byte) error {
	n, err := writer.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
