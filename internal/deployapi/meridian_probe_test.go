package deployapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMeridianProbeRequiresExplicitHelperAcknowledgement(t *testing.T) {
	for _, test := range []struct {
		name      string
		code      int
		body      string
		wantError bool
	}{
		{name: "verified", code: http.StatusOK, body: `{"status":"verified"}`},
		{name: "plain HTTP success is not proof", code: http.StatusOK, body: `{}`, wantError: true},
		{name: "wrong status", code: http.StatusOK, body: `{"status":"ready"}`, wantError: true},
		{name: "malformed acknowledgement", code: http.StatusOK, body: `not JSON`, wantError: true},
		{name: "failed probe", code: http.StatusBadGateway, body: `{"error":"proxy verification did not pass"}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/meridian/verify" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("unexpected proxy verification transport")
				}
				w.WriteHeader(test.code)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			client := &Client{http: server.Client()}
			client.http.Transport = probeTestTransport{target: server.URL, delegate: http.DefaultTransport}
			err := client.VerifyMeridianEntry(context.Background(), MeridianProbeRequest{ExpectedExit: "192.0.2.10"})
			if (err != nil) != test.wantError {
				t.Fatalf("verification acknowledgement error=%v, wantError=%v", err, test.wantError)
			}
		})
	}
}

type probeTestTransport struct {
	target   string
	delegate http.RoundTripper
}

func (transport probeTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	target, err := http.NewRequest(request.Method, transport.target, nil)
	if err != nil {
		return nil, err
	}
	request.URL.Scheme, request.URL.Host = target.URL.Scheme, target.URL.Host
	return transport.delegate.RoundTrip(request)
}
