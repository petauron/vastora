package catalog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchRejectsSourceURLCredentialsBeforeNetworkAccess(t *testing.T) {
	_, err := Fetch(context.Background(), FetchConfig{URL: "https://catalog-user:catalog-password@example.invalid/catalog.json"})
	if err == nil || !strings.Contains(err.Error(), "without credentials") {
		t.Fatalf("catalog source URL credentials were accepted: %v", err)
	}
}

func TestFetchUsesBearerCustomCAAndConditionalValidators(t *testing.T) {
	publicKey, _, rawEnvelope := signedFetchFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer private-catalog-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if requests.Add(1) == 1 {
			writer.Header().Set("ETag", `"catalog-v1"`)
			writer.Header().Set("Last-Modified", "Sat, 30 Aug 2026 00:00:00 GMT")
			_, _ = writer.Write(rawEnvelope)
			return
		}
		if request.Header.Get("If-None-Match") != `"catalog-v1"` || request.Header.Get("If-Modified-Since") != "Sat, 30 Aug 2026 00:00:00 GMT" {
			t.Errorf("conditional headers = %q %q", request.Header.Get("If-None-Match"), request.Header.Get("If-Modified-Since"))
		}
		writer.Header().Set("ETag", `"catalog-v1-confirmed"`)
		writer.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	config := FetchConfig{URL: server.URL, PublicKey: publicKey, BearerToken: "private-catalog-token", CustomCAPEM: testServerCAPEM(t, server)}
	first, err := Fetch(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if first.NotModified || first.ETag != `"catalog-v1"` || first.LastModified == "" || len(first.Catalog.Apps) != 1 {
		t.Fatalf("first fetch = %#v", first)
	}
	config.ETag, config.LastModified = first.ETag, first.LastModified
	second, err := Fetch(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if !second.NotModified || second.ETag != `"catalog-v1-confirmed"` || second.LastModified != first.LastModified {
		t.Fatalf("304 fetch = %#v", second)
	}
}

func TestFetchRejectsUnsolicitedNotModified(t *testing.T) {
	publicKey, _, _ := signedFetchFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	_, err := Fetch(context.Background(), FetchConfig{URL: server.URL, PublicKey: publicKey, CustomCAPEM: testServerCAPEM(t, server)})
	if err == nil || !strings.Contains(err.Error(), "304 without a conditional request") {
		t.Fatalf("unsolicited 304 was accepted: %v", err)
	}
}

func TestFetchRejectsUnsafeRedirectTargets(t *testing.T) {
	publicKey, _, _ := signedFetchFixture(t)
	httpTargetCalled := false
	httpTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { httpTargetCalled = true }))
	defer httpTarget.Close()

	tests := []struct {
		name     string
		location func(string) string
	}{
		{name: "HTTPS downgrade", location: func(string) string { return httpTarget.URL }},
		{name: "redirect credentials", location: func(origin string) string {
			return strings.Replace(origin, "https://", "https://user:password@", 1) + "/target"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				http.Redirect(writer, request, test.location(server.URL), http.StatusFound)
			}))
			defer server.Close()
			_, err := Fetch(context.Background(), FetchConfig{URL: server.URL, PublicKey: publicKey, CustomCAPEM: testServerCAPEM(t, server)})
			if err == nil || !strings.Contains(err.Error(), "redirect target must be absolute HTTPS without credentials") {
				t.Fatalf("unsafe redirect was followed: %v", err)
			}
		})
	}
	if httpTargetCalled {
		t.Fatal("HTTPS downgrade reached the HTTP target")
	}
}

func TestFetchLimitsRedirectChains(t *testing.T) {
	publicKey, _, _ := signedFetchFixture(t)
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		next := 1
		if _, err := fmt.Sscanf(request.URL.Path, "/%d", &next); err == nil {
			next++
		}
		http.Redirect(writer, request, fmt.Sprintf("%s/%d", server.URL, next), http.StatusFound)
	}))
	defer server.Close()
	_, err := Fetch(context.Background(), FetchConfig{URL: server.URL + "/0", PublicKey: publicKey, CustomCAPEM: testServerCAPEM(t, server)})
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("unbounded redirect chain was accepted: %v", err)
	}
}

func TestFetchStripsBearerOnCrossOriginRedirect(t *testing.T) {
	publicKey, _, rawEnvelope := signedFetchFixture(t)
	target := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "" {
			t.Errorf("cross-origin authorization leaked: %q", request.Header.Get("Authorization"))
		}
		_, _ = writer.Write(rawEnvelope)
	}))
	defer target.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer source-secret" {
			t.Errorf("origin authorization = %q", request.Header.Get("Authorization"))
		}
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	caBundle := append(testServerCAPEM(t, origin), testServerCAPEM(t, target)...)
	if _, err := Fetch(context.Background(), FetchConfig{URL: origin.URL, PublicKey: publicKey, BearerToken: "source-secret", CustomCAPEM: caBundle}); err != nil {
		t.Fatal(err)
	}
}

func TestFetchRejectsOversizeBodiesAndBadSignatures(t *testing.T) {
	publicKey, _, rawEnvelope := signedFetchFixture(t)
	t.Run("content length", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Length", fmt.Sprint(MaxEnvelopeBytes+1))
			writer.WriteHeader(http.StatusOK)
		}))
		defer server.Close()
		_, err := Fetch(context.Background(), FetchConfig{URL: server.URL, PublicKey: publicKey, CustomCAPEM: testServerCAPEM(t, server)})
		if err == nil || !strings.Contains(err.Error(), "maximum size") {
			t.Fatalf("oversize content length accepted: %v", err)
		}
	})
	t.Run("chunked", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.(http.Flusher).Flush()
			chunk := strings.Repeat("x", 64<<10)
			for written := int64(0); written <= MaxEnvelopeBytes; written += int64(len(chunk)) {
				_, _ = writer.Write([]byte(chunk))
			}
		}))
		defer server.Close()
		_, err := Fetch(context.Background(), FetchConfig{URL: server.URL, PublicKey: publicKey, CustomCAPEM: testServerCAPEM(t, server)})
		if err == nil || !strings.Contains(err.Error(), "maximum size") {
			t.Fatalf("oversize chunked body accepted: %v", err)
		}
	})
	t.Run("signature", func(t *testing.T) {
		var badEnvelope Envelope
		if err := json.Unmarshal(rawEnvelope, &badEnvelope); err != nil {
			t.Fatal(err)
		}
		badEnvelope.Signature = base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
		badEnvelopeRaw, err := MarshalEnvelope(badEnvelope)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(badEnvelopeRaw) }))
		defer server.Close()
		_, err = Fetch(context.Background(), FetchConfig{URL: server.URL, PublicKey: publicKey, CustomCAPEM: testServerCAPEM(t, server)})
		if err == nil || !strings.Contains(err.Error(), "signature") {
			t.Fatalf("bad signature was accepted: %v", err)
		}
	})
}

func signedFetchFixture(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := MarshalCatalog(validCatalog())
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Sign("fetch-test", privateKey, payload)
	if err != nil {
		t.Fatal(err)
	}
	rawEnvelope, err := MarshalEnvelope(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return publicKey, privateKey, rawEnvelope
}

func testServerCAPEM(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	certificate, err := x509.ParseCertificate(server.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
}
