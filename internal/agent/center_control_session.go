package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"
)

// An update helper needs one control connection independent of the service it
// stops. Reconnect to the peer proved by the first TLS handshake, without another
// DNS lookup. The original URL, SNI, CA pin and every execution authorization are
// retained. A lost peer fails closed; no alternate resolver or endpoint is used.
type centerControlSession struct {
	client      *http.Client
	origin      url.URL
	fingerprint string
	certificate string
	mu          sync.Mutex
	peer        string
}

// NewCenterControlClient is scoped to one helper invocation. It neither persists
// an IP override nor changes the normal Agent resolver or proxy configuration.
func NewCenterControlClient(connection Connection, timeout time.Duration) (Client, func(), error) {
	client, err := CenterHTTPClient(connection, timeout)
	if err != nil {
		return Client{}, nil, err
	}
	return newCenterControlClient(connection, client)
}

func newCenterControlClient(connection Connection, client *http.Client) (Client, func(), error) {
	origin, err := url.Parse(connection.CenterURL)
	if err != nil || origin.Hostname() == "" || origin.User != nil {
		client.CloseIdleConnections()
		return Client{}, nil, errors.New("agent: invalid Center control origin")
	}
	session := &centerControlSession{client: client, origin: *origin, fingerprint: normalizeCAFingerprint(connection.CAFingerprint), certificate: strings.TrimSpace(connection.CACertificatePEM)}
	transport := client.Transport.(*http.Transport)
	transport.Proxy = nil
	dial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		session.mu.Lock()
		peer := session.peer
		session.mu.Unlock()
		if peer != "" {
			address = peer
		}
		return dial(ctx, network, address)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("agent: Center control requests cannot follow redirects")
	}
	return Client{centerControl: session}, client.CloseIdleConnections, nil
}

func (s *centerControlSession) do(request *http.Request, fingerprint, certificate string) (*http.Response, error) {
	if request.URL.Scheme != s.origin.Scheme || !strings.EqualFold(request.URL.Host, s.origin.Host) || request.URL.User != nil || normalizeCAFingerprint(fingerprint) != s.fingerprint || strings.TrimSpace(certificate) != s.certificate {
		return nil, errors.New("agent: Center control request changed its origin or trust")
	}
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		// GotConn runs only after TLS validation (or approved loopback HTTP).
		if peer, ok := info.Conn.RemoteAddr().(*net.TCPAddr); ok {
			s.mu.Lock()
			if s.peer == "" {
				s.peer = peer.String()
			}
			s.mu.Unlock()
		}
	}}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	return s.client.Do(request)
}
