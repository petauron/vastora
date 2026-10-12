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
)

func TestManagedUpgradeControlSessionRetainsVerifiedPeerAndSNI(t *testing.T) {
	certificate, ca := testPrivateCenterTLSCertificate(t)
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.TLS == nil || r.TLS.ServerName != "center.example.test" || !strings.HasPrefix(r.Host, "center.example.test:") {
			t.Error("Center URL or TLS SNI changed")
		}
		w.Header().Set("Connection", "close") // Force a fresh TCP connection per authorization.
		_, _ = w.Write([]byte(`{}`))
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	connection := Connection{CenterURL: "https://center.example.test:" + parsed.Port(), CAFingerprint: certificatePublicKeyFingerprint(ca), CACertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))}
	transportClient, err := CenterHTTPClient(connection, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int32
	transportClient.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if strings.HasPrefix(address, "center.example.test:") {
			if lookups.Add(1) != 1 {
				return nil, errors.New("resolver no longer available")
			}
			address = parsed.Host
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	client, release, err := newCenterControlClient(connection, transportClient)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	for range 4 {
		if err := client.post(context.Background(), connection.CenterURL+"/api/authorize", struct{}{}, "", connection.CAFingerprint, connection.CACertificatePEM, nil); err != nil {
			t.Fatal(err)
		}
	}
	if lookups.Load() != 1 || requests.Load() != 4 {
		t.Fatalf("control session re-resolved Center: lookups=%d requests=%d", lookups.Load(), requests.Load())
	}
	for _, mutation := range []string{"origin", "pin", "certificate"} {
		endpoint, pin, cert := connection.CenterURL+"/api/authorize", connection.CAFingerprint, connection.CACertificatePEM
		switch mutation {
		case "origin":
			endpoint = server.URL
		case "pin":
			pin = strings.Repeat("0", 64)
		case "certificate":
			cert = ""
		}
		if err := client.post(context.Background(), endpoint, struct{}{}, "", pin, cert, nil); err == nil {
			t.Fatal("changed control identity was accepted: " + mutation)
		}
	}
	server.Close()
	if err := client.post(context.Background(), connection.CenterURL+"/api/authorize", struct{}{}, "", connection.CAFingerprint, connection.CACertificatePEM, nil); err == nil || lookups.Load() != 1 {
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
			server.StartTLS()
			defer server.Close()
			connection := Connection{CenterURL: server.URL, CAFingerprint: certificatePublicKeyFingerprint(ca), CACertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))}
			client, release, err := NewCenterControlClient(connection, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			if err := client.post(context.Background(), server.URL, struct{}{}, "", connection.CAFingerprint, connection.CACertificatePEM, nil); err == nil {
				t.Fatal("unsafe control connection was accepted")
			}
			if scenario == "untrusted" && client.centerControl.peer != "" {
				t.Fatal("unverified TLS peer was pinned")
			}
		})
	}
}
