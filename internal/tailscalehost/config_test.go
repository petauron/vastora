package tailscalehost

import "testing"

func TestRenderConfigUsesPinnedAlphaSchema(t *testing.T) {
	endpoint, err := StaticEndpoint("203.0.113.10")
	if err != nil {
		t.Fatal(err)
	}
	payload, err := RenderConfig([]string{endpoint, endpoint})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"version\":\"alpha0\",\"locked\":false,\"staticEndpoints\":[\"203.0.113.10:41641\"]}\n"
	if string(payload) != want {
		t.Fatalf("config = %q, want %q", payload, want)
	}
}

func TestRenderConfigRejectsWrongPortAndIPv6(t *testing.T) {
	for _, endpoint := range []string{"203.0.113.10:443", "[2001:db8::1]:41641", "invalid"} {
		if _, err := RenderConfig([]string{endpoint}); err == nil {
			t.Fatalf("invalid endpoint %q was accepted", endpoint)
		}
	}
}
