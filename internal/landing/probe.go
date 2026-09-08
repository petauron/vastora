package landing

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Credentials belong only in encrypted desired state and private runtime
// configuration. Neither errors nor BusinessResult include them.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c Credentials) valid() bool {
	for _, value := range []string{c.Username, c.Password} {
		if len(value) < 16 || len(value) > 128 {
			return false
		}
		for _, char := range value {
			if char < 33 || char > 126 {
				return false
			}
		}
	}
	return true
}

// Probe runs from the proxy host, outside the Docker forwarding gate. This
// allows fresh recovery probes while the selected application's data remains
// blocked. It never opens a direct connection to a public probe destination.
type Probe struct {
	Credentials Credentials
}

func (p Probe) Check(ctx context.Context, peer PeerIdentity, revision uint64) (BusinessResult, error) {
	result := BusinessResult{Peer: peer, Revision: revision, StartedAt: time.Now().UTC()}
	address, err := netip.ParseAddr(peer.Address)
	if err != nil || !netip.MustParsePrefix("100.64.0.0/10").Contains(address) || peer.ID == "" || peer.PublicKey == "" || revision == 0 || !p.Credentials.valid() {
		return result, errors.New("landing: invalid business probe identity")
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	endpoint := net.JoinHostPort(peer.Address, "1080")
	exit, err := p.tcp(ctx, endpoint)
	if err != nil {
		return result, errors.New("landing: authenticated TCP exchange failed")
	}
	result.TCP, result.ExitIPv4 = true, exit
	if err := p.udp(ctx, endpoint); err != nil {
		return result, errors.New("landing: authenticated UDP exchange failed")
	}
	result.UDP, result.UDPRelay, result.CheckedAt = true, endpoint, time.Now().UTC()
	return result, nil
}

func (p Probe) tcp(ctx context.Context, endpoint string) (string, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return p.dialTCP(ctx, endpoint, network, address)
	}, DisableKeepAlives: true, TLSHandshakeTimeout: CheckTimeout, ResponseHeaderTimeout: CheckTimeout}
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
	if response.StatusCode != http.StatusOK {
		return "", errors.New("landing: exit probe rejected")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil || len(body) > 4096 {
		return "", errors.New("landing: invalid exit probe response")
	}
	return traceExit(body)
}

// The existing x/net SOCKS dialer also offers unauthenticated access and cannot
// require RFC 1929 authentication. A health proof must reject that downgrade.
// This CONNECT implementation is intentionally restricted to the one HTTPS
// probe destination; UDP uses the same strict authentication exchange below.
func (p Probe) dialTCP(ctx context.Context, endpoint, network, address string) (_ net.Conn, returnedErr error) {
	const hostname = "www.cloudflare.com"
	if network != "tcp" || address != hostname+":443" {
		return nil, errors.New("landing: invalid TCP probe destination")
	}
	connection, err := (&net.Dialer{Timeout: CheckTimeout}).DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnedErr != nil {
			_ = connection.Close()
		}
	}()
	deadline := time.Now().Add(CheckTimeout)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if err := authenticateSOCKS(connection, p.Credentials); err != nil {
		return nil, err
	}
	message := append([]byte{5, 1, 0, 3, byte(len(hostname))}, hostname...)
	message = append(message, 1, 187) // 443, network byte order
	if err := writeSOCKS(connection, message); err != nil {
		return nil, err
	}
	var reply [4]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		return nil, err
	}
	if reply[0] != 5 || reply[1] != 0 || reply[2] != 0 {
		return nil, errors.New("landing: SOCKS CONNECT rejected")
	}
	length := 0
	switch reply[3] {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(connection, size[:]); err != nil || size[0] == 0 {
			return nil, errors.New("landing: malformed SOCKS CONNECT response")
		}
		length = int(size[0])
	default:
		return nil, errors.New("landing: malformed SOCKS CONNECT response")
	}
	if _, err := io.CopyN(io.Discard, connection, int64(length+2)); err != nil {
		return nil, err
	}
	return connection, nil
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
// RFC 1928/1929 exchange covers only our fixed, authenticated IPv4 relay; it is
// not a general-purpose SOCKS client and cannot follow arbitrary relay hosts.
func (p Probe) udp(ctx context.Context, endpoint string) error {
	control, err := (&net.Dialer{}).DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	defer control.Close()
	stop := context.AfterFunc(ctx, func() { _ = control.Close() })
	defer stop()
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("landing: UDP probe requires a deadline")
	}
	if err := control.SetDeadline(deadline); err != nil {
		return err
	}
	if err := authenticateSOCKS(control, p.Credentials); err != nil {
		return err
	}
	if err := writeSOCKS(control, []byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return err
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(control, reply[:4]); err != nil {
		return err
	}
	if reply[0] != 5 || reply[1] != 0 || reply[2] != 0 || reply[3] != 1 {
		return errors.New("landing: UDP relay rejected")
	}
	if _, err := io.ReadFull(control, reply[4:]); err != nil {
		return err
	}
	bound := netip.AddrPortFrom(netip.AddrFrom4([4]byte(reply[4:8])), binary.BigEndian.Uint16(reply[8:]))
	if bound.String() != endpoint {
		return errors.New("landing: unexpected UDP relay")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "udp4", endpoint)
	if err != nil {
		return err
	}
	defer connection.Close()
	stopUDP := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopUDP()
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	var identifier [2]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return err
	}
	question := dnsmessage.Question{Name: dnsmessage.MustNewName("www.cloudflare.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: binary.BigEndian.Uint16(identifier[:]), RecursionDesired: true}, Questions: []dnsmessage.Question{question}}
	packet, err := query.Pack()
	if err != nil {
		return err
	}
	header := []byte{0, 0, 0, 1, 1, 1, 1, 1, 0, 53}
	if err := writeSOCKS(connection, append(header, packet...)); err != nil {
		return err
	}
	buffer := make([]byte, 4097)
	n, err := connection.Read(buffer)
	if err != nil || n > 4096 || n <= len(header) || string(buffer[:len(header)]) != string(header) {
		return errors.New("landing: invalid UDP payload")
	}
	var answer dnsmessage.Message
	if err := answer.Unpack(buffer[len(header):n]); err != nil || answer.ID != query.ID || !answer.Response || answer.Truncated || answer.RCode != dnsmessage.RCodeSuccess || len(answer.Questions) != 1 || answer.Questions[0] != question {
		return errors.New("landing: UDP DNS exchange mismatch")
	}
	for _, resource := range answer.Answers {
		if record, ok := resource.Body.(*dnsmessage.AResource); ok && publicIPv4(netip.AddrFrom4(record.A)) {
			return nil
		}
	}
	return errors.New("landing: UDP DNS exchange returned no public answer")
}

func authenticateSOCKS(connection io.ReadWriter, credentials Credentials) error {
	if !credentials.valid() {
		return errors.New("landing: invalid SOCKS credentials")
	}
	if err := writeSOCKS(connection, []byte{5, 1, 2}); err != nil {
		return err
	}
	var reply [2]byte
	if _, err := io.ReadFull(connection, reply[:]); err != nil {
		return err
	}
	if reply != [2]byte{5, 2} {
		return errors.New("landing: SOCKS authentication downgrade rejected")
	}
	message := append([]byte{1, byte(len(credentials.Username))}, credentials.Username...)
	message = append(message, byte(len(credentials.Password)))
	message = append(message, credentials.Password...)
	if err := writeSOCKS(connection, message); err != nil {
		return err
	}
	if _, err := io.ReadFull(connection, reply[:]); err != nil || reply != [2]byte{1, 0} {
		return errors.New("landing: SOCKS authentication rejected")
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
