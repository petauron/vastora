package agent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/petauron/vastora/internal/agentpb"
	"github.com/petauron/vastora/internal/controlplane"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// grpcPeer records a helper's first authenticated TCP peer. Reconnection may
// only use this address, retaining the original TLS name and CA verification.
type grpcPeer struct {
	mu      sync.Mutex
	address string
}

type grpcPinCredentials struct {
	credentials.TransportCredentials
	peer *grpcPeer
}

func (c grpcPinCredentials) ClientHandshake(ctx context.Context, authority string, raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	conn, info, err := c.TransportCredentials.ClientHandshake(ctx, authority, raw)
	if err != nil {
		return nil, nil, err
	}
	c.peer.mu.Lock()
	defer c.peer.mu.Unlock()
	address := raw.RemoteAddr().String()
	if c.peer.address != "" && c.peer.address != address {
		_ = conn.Close()
		return nil, nil, errors.New("agent: Center peer changed during control session")
	}
	c.peer.address = address
	return conn, info, nil
}

func (c grpcPinCredentials) Clone() credentials.TransportCredentials {
	return grpcPinCredentials{TransportCredentials: c.TransportCredentials.Clone(), peer: c.peer}
}

// newAgentGRPCConn never permits plaintext remote traffic. Only the existing
// co-located loopback Center may use h2c. Public cleanup callbacks use normal
// system-root TLS because their private network is intentionally already gone.
func newAgentGRPCConn(centerURL, fingerprint, certificate string, systemRoots bool, peer *grpcPeer) (*grpc.ClientConn, error) {
	parsed, err := url.Parse(strings.TrimSpace(centerURL))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("agent: invalid Center gRPC origin")
	}
	var transport credentials.TransportCredentials
	switch {
	case parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()):
		transport = insecure.NewCredentials()
	case parsed.Scheme == "https" && strings.TrimSpace(fingerprint) != "":
		config, err := pinnedTLSConfig(fingerprint, certificate)
		if err != nil {
			return nil, err
		}
		transport = credentials.NewTLS(config)
	case parsed.Scheme == "https" && systemRoots:
		transport = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	default:
		return nil, errors.New("agent: Center gRPC requires validated TLS or loopback")
	}
	options := []grpc.DialOption{
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(controlplane.MaxEnvelopeWire), grpc.MaxCallSendMsgSize(controlplane.MaxEnvelopeWire)),
	}
	prefix := strings.TrimRight(parsed.EscapedPath(), "/")
	if prefix != "" {
		options = append(options,
			grpc.WithUnaryInterceptor(func(ctx context.Context, method string, request, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
				return invoker(ctx, prefix+method, request, reply, cc, options...)
			}),
			grpc.WithStreamInterceptor(func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
				return streamer(ctx, desc, cc, prefix+method, options...)
			}),
		)
	}
	if peer != nil {
		options = append(options, grpc.WithNoProxy())
		if parsed.Scheme == "https" {
			transport = grpcPinCredentials{TransportCredentials: transport, peer: peer}
		}
		dialer := &net.Dialer{}
		options = append(options, grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			peer.mu.Lock()
			pinned := peer.address
			peer.mu.Unlock()
			if pinned != "" {
				address = pinned
			}
			conn, err := dialer.DialContext(ctx, "tcp", address)
			if err == nil && parsed.Scheme == "http" {
				peer.mu.Lock()
				if peer.address == "" {
					peer.address = conn.RemoteAddr().String()
				}
				peer.mu.Unlock()
			}
			return conn, err
		}))
	}
	options = append(options, grpc.WithTransportCredentials(transport))
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	// The helper resolves only its first verified peer. Using the passthrough
	// resolver ensures later reconnects reach the dialer even if DNS goes away;
	// the dialer then uses that peer instead of consulting DNS again.
	target := "dns:///" + net.JoinHostPort(parsed.Hostname(), port)
	if peer != nil {
		target = "passthrough:///" + net.JoinHostPort(parsed.Hostname(), port)
	}
	return grpc.NewClient(target, options...)
}

func grpcAuthenticatedContext(ctx context.Context, credential string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+credential)
}

// Translate protocol status into the existing execution-control error class;
// an authorization or lease failure must still cancel the active execution.
func agentGRPCError(err error) error {
	if err == nil {
		return nil
	}
	code := status.Code(err)
	httpStatus := 0
	switch code {
	case codes.Unauthenticated:
		httpStatus = http.StatusUnauthorized
	case codes.PermissionDenied:
		httpStatus = http.StatusForbidden
	case codes.FailedPrecondition, codes.Aborted:
		httpStatus = http.StatusConflict
	case codes.InvalidArgument:
		httpStatus = http.StatusBadRequest
	case codes.NotFound:
		httpStatus = http.StatusNotFound
	case codes.Unavailable:
		httpStatus = http.StatusServiceUnavailable
	}
	if httpStatus != 0 {
		return &centerResponseError{status: httpStatus, message: status.Convert(err).Message(), cause: err}
	}
	return fmt.Errorf("agent: Center gRPC request: %w", err)
}

func agentGRPCClient(conn *grpc.ClientConn) agentpb.AgentControlClient {
	return agentpb.NewAgentControlClient(conn)
}

// grpcControlSession shares a Center connection. An update helper is strict:
// it remains bound to its original Center and first authenticated TCP peer.
type grpcControlSession struct {
	mu          sync.Mutex
	origin      string
	fingerprint string
	certificate string
	conn        *grpc.ClientConn
	peer        *grpcPeer
	timeout     time.Duration
	strict      bool
	closed      bool
}

func newGRPCControlSession(connection Connection) (*grpcControlSession, error) {
	if _, _, err := normalizeCenterTrust(connection.CenterURL, connection.CAFingerprint, connection.CACertificatePEM); err != nil {
		return nil, err
	}
	if err := validateCAFingerprint(connection.CenterURL, connection.CAFingerprint); err != nil {
		return nil, err
	}
	peer := &grpcPeer{}
	conn, err := newAgentGRPCConn(connection.CenterURL, connection.CAFingerprint, connection.CACertificatePEM, false, peer)
	if err != nil {
		return nil, err
	}
	return &grpcControlSession{origin: strings.TrimRight(connection.CenterURL, "/"), fingerprint: normalizeCAFingerprint(connection.CAFingerprint), certificate: strings.TrimSpace(connection.CACertificatePEM), conn: conn, peer: peer, strict: true}, nil
}

func (s *grpcControlSession) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.conn != nil {
		_ = s.conn.Close()
		s.conn = nil
	}
}

// UseCenterGRPCSession shares a single connection among task, heartbeat and
// reporting operations. A confirmed Center URL change replaces the connection.
func (c *Client) UseCenterGRPCSession() func() {
	session := &grpcControlSession{}
	c.centerRPC = session
	return session.close
}

func (c Client) grpcConnection(connection Connection, systemRoots bool) (*grpc.ClientConn, func(), error) {
	if c.centerRPC != nil && !systemRoots {
		session := c.centerRPC
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.closed {
			return nil, nil, errors.New("agent: Center gRPC session is closed")
		}
		origin := strings.TrimRight(connection.CenterURL, "/")
		fingerprint := normalizeCAFingerprint(connection.CAFingerprint)
		certificate := strings.TrimSpace(connection.CACertificatePEM)
		changed := origin != session.origin || fingerprint != session.fingerprint || certificate != session.certificate
		if session.strict && changed {
			return nil, nil, errors.New("agent: Center gRPC request changed its origin or trust")
		}
		if session.conn == nil || changed {
			conn, err := newAgentGRPCConn(connection.CenterURL, fingerprint, certificate, false, nil)
			if err != nil {
				return nil, nil, err
			}
			if session.conn != nil {
				_ = session.conn.Close()
			}
			session.origin, session.fingerprint, session.certificate, session.conn = origin, fingerprint, certificate, conn
		}
		return session.conn, func() {}, nil
	}
	conn, err := newAgentGRPCConn(connection.CenterURL, connection.CAFingerprint, connection.CACertificatePEM, systemRoots, nil)
	if err != nil {
		return nil, nil, err
	}
	return conn, func() { _ = conn.Close() }, nil
}

type agentRPCOperation uint8

const (
	rpcHeartbeat agentRPCOperation = iota + 1
	rpcLandingLatency
	rpcLegacyReceipts
	rpcExecutionSession
	rpcExecutionTransition
	rpcTaskLease
	rpcTaskCompletion
	rpcBeginAgentUpdate
	rpcBeginTailscaleUpdate
	rpcBeginDecommission
	rpcDecommissionCallback
)

func (c Client) callAgentRPC(ctx context.Context, connection Connection, operation agentRPCOperation, objectID string, payload, target any, limit int, systemRoots bool) (err error) {
	defer c.abortExecutionOnError(&err)
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("agent: encode Center request: %w", err)
	}
	if len(body) > limit {
		return errors.New("agent: Center request exceeds the allowed size")
	}
	conn, release, err := c.grpcConnection(connection, systemRoots)
	if err != nil {
		return err
	}
	defer release()
	timeout := 15 * time.Second
	if c.centerRPC != nil && c.centerRPC.timeout > 0 {
		timeout = c.centerRPC.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ctx = grpcAuthenticatedContext(ctx, connection.Credential)
	client := agentGRPCClient(conn)
	var result *agentpb.Document
	switch operation {
	case rpcHeartbeat:
		result, err = client.Heartbeat(ctx, &agentpb.AgentDocument{AgentId: connection.AgentID, Json: body})
	case rpcLandingLatency:
		result, err = client.ReportLandingLatency(ctx, &agentpb.AgentDocument{AgentId: connection.AgentID, Json: body})
	case rpcLegacyReceipts:
		result, err = client.ImportLegacyReceipts(ctx, &agentpb.AgentDocument{AgentId: connection.AgentID, Json: body})
	case rpcExecutionSession:
		result, err = client.RegisterExecutionSession(ctx, &agentpb.AgentDocument{AgentId: connection.AgentID, Json: body})
	case rpcExecutionTransition:
		result, err = client.TransitionExecution(ctx, &agentpb.ExecutionDocument{AgentId: connection.AgentID, ExecutionId: objectID, Json: body})
	case rpcTaskLease:
		result, err = client.RenewTaskLease(ctx, &agentpb.TaskDocument{AgentId: connection.AgentID, TaskId: objectID, Json: body})
	case rpcTaskCompletion:
		result, err = client.CompleteTask(ctx, &agentpb.TaskDocument{AgentId: connection.AgentID, TaskId: objectID, Json: body})
	case rpcBeginAgentUpdate:
		result, err = client.BeginAgentUpdate(ctx, &agentpb.TaskDocument{AgentId: connection.AgentID, TaskId: objectID, Json: body})
	case rpcBeginTailscaleUpdate:
		result, err = client.BeginTailscaleUpdate(ctx, &agentpb.TaskDocument{AgentId: connection.AgentID, TaskId: objectID, Json: body})
	case rpcBeginDecommission:
		result, err = client.BeginDecommission(ctx, &agentpb.AgentDocument{AgentId: connection.AgentID, Json: body})
	case rpcDecommissionCallback:
		result, err = client.DecommissionCallback(ctx, &agentpb.DecommissionRequest{TaskId: objectID, Json: body})
	default:
		return errors.New("agent: unsupported Center gRPC operation")
	}
	if err != nil {
		return agentGRPCError(err)
	}
	if result == nil || len(result.Json) > controlplane.MaxEnvelopeWire {
		return errors.New("agent: Center returned an invalid gRPC result")
	}
	if target != nil && json.Unmarshal(result.Json, target) != nil {
		return errors.New("agent: Center returned invalid JSON")
	}
	return nil
}

func (c Client) probeAgentRPC(ctx context.Context, connection Connection) error {
	conn, release, err := (Client{}).grpcConnection(connection, false)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	result, err := agentGRPCClient(conn).Probe(ctx, &agentpb.ProbeRequest{})
	if err != nil {
		return agentGRPCError(err)
	}
	if result == nil || !result.Ready {
		return errors.New("agent: Center probe is not OK")
	}
	return nil
}
