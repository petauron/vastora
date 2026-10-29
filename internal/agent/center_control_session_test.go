package agent

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/petauron/vastora/internal/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
)

// Keep the original SNI, origin and peer-pinning checks on the gRPC transport.
type controlSessionProbeFixture struct {
	agentpb.UnimplementedAgentControlServer
	t        *testing.T
	requests atomic.Int32
}

func (s *controlSessionProbeFixture) Probe(ctx context.Context, _ *agentpb.ProbeRequest) (*agentpb.ProbeResponse, error) {
	s.requests.Add(1)
	p, ok := peer.FromContext(ctx)
	if !ok {
		s.t.Error("missing peer")
		return &agentpb.ProbeResponse{Ready: true}, nil
	}
	verified, ok := p.AuthInfo.(credentials.TLSInfo)
	md, _ := metadata.FromIncomingContext(ctx)
	authority := md.Get(":authority")
	if !ok || verified.State.ServerName != "center.example.test" || len(authority) != 1 || !strings.HasPrefix(authority[0], "center.example.test:") {
		s.t.Error("Center URL or TLS SNI changed")
	}
	return &agentpb.ProbeResponse{Ready: true}, nil
}

func TestManagedUpgradeControlSessionRetainsVerifiedPeerAndSNI(t *testing.T) {
	certificate, ca := testPrivateCenterTLSCertificate(t)
	service := &controlSessionProbeFixture{t: t}
	grpcServer := grpc.NewServer()
	agentpb.RegisterAgentControlServer(grpcServer, service)
	server := httptest.NewUnstartedServer(grpcServer)
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	connection := Connection{CenterURL: "https://center.example.test:" + parsed.Port(), CAFingerprint: certificatePublicKeyFingerprint(ca), CACertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))}
	client, release, err := NewCenterControlClient(connection, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	config, err := pinnedTLSConfig(connection.CAFingerprint, connection.CACertificatePEM, "center.example.test")
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	pinned := client.centerRPC.peer
	connectionForTest, err := grpc.NewClient("passthrough:///center.example.test:"+parsed.Port(),
		grpc.WithNoProxy(), grpc.WithTransportCredentials(grpcPinCredentials{TransportCredentials: credentials.NewTLS(config), peer: pinned}),
		grpc.WithContextDialer(func(ctx context.Context, address string) (net.Conn, error) {
			pinned.mu.Lock()
			current := pinned.address
			pinned.mu.Unlock()
			if current == "" {
				if lookups.Add(1) != 1 {
					return nil, errors.New("resolver no longer available")
				}
				current = parsed.Host
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", current)
		}))
	if err != nil {
		t.Fatal(err)
	}
	_ = client.centerRPC.conn.Close()
	client.centerRPC.conn = connectionForTest
	probe := func(candidate Connection) error {
		conn, done, err := client.grpcConnection(candidate, false)
		if err != nil {
			return err
		}
		defer done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err = agentpb.NewAgentControlClient(conn).Probe(ctx, &agentpb.ProbeRequest{})
		return err
	}
	for range 4 {
		if err := probe(connection); err != nil {
			t.Fatal(err)
		}
	}
	if lookups.Load() != 1 || service.requests.Load() != 4 {
		t.Fatalf("control session re-resolved Center: lookups=%d requests=%d", lookups.Load(), service.requests.Load())
	}
	for _, mutation := range []string{"origin", "pin", "certificate"} {
		candidate := connection
		switch mutation {
		case "origin":
			candidate.CenterURL = server.URL
		case "pin":
			candidate.CAFingerprint = strings.Repeat("0", 64)
		case "certificate":
			candidate.CACertificatePEM = ""
		}
		if err := probe(candidate); err == nil {
			t.Fatal("changed control identity was accepted: " + mutation)
		}
	}
	server.Close()
	if err := probe(connection); err == nil || lookups.Load() != 1 {
		t.Fatal("lost peer did not fail closed without resolver fallback")
	}
}

func TestManagedUpgradeControlSessionRejectsRedirectAndUntrustedTLS(t *testing.T) {
	certificate, ca := testPrivateCenterTLSCertificate(t)
	for _, scenario := range []string{"redirect", "untrusted"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
			}))
			if scenario == "redirect" {
				server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
			}
			server.EnableHTTP2 = true
			server.StartTLS()
			defer server.Close()
			connection := Connection{CenterURL: server.URL, CAFingerprint: certificatePublicKeyFingerprint(ca), CACertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))}
			client, release, err := NewCenterControlClient(connection, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			conn, done, err := client.grpcConnection(connection, false)
			if err != nil {
				t.Fatal(err)
			}
			defer done()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := agentpb.NewAgentControlClient(conn).Probe(ctx, &agentpb.ProbeRequest{}); err == nil {
				t.Fatal("unsafe control connection was accepted")
			}
			client.centerRPC.peer.mu.Lock()
			pinned := client.centerRPC.peer.address
			client.centerRPC.peer.mu.Unlock()
			if scenario == "untrusted" && pinned != "" {
				t.Fatal("unverified TLS peer was pinned")
			}
		})
	}
}
