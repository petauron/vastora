package senulun

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestServiceAuthenticationConfiguration(t *testing.T) {
	const token = "test-only-pulse-setup-token-0000000000"
	for _, test := range []struct {
		name, origin, token string
		invalid             bool
	}{
		{name: "HTTPS origin", origin: "https://senulun.example.com", token: token},
		{name: "HTTPS root", origin: "https://senulun.example.com/", token: token},
		{name: "HTTPS port", origin: "https://senulun.example.com:8443", token: token},
		{name: "missing origin", token: token, invalid: true},
		{name: "HTTP origin", origin: "http://127.0.0.1:18080", token: token, invalid: true},
		{name: "path", origin: "https://senulun.example.com/login", token: token, invalid: true},
		{name: "query", origin: "https://senulun.example.com?next=login", token: token, invalid: true},
		{name: "empty query", origin: "https://senulun.example.com?", token: token, invalid: true},
		{name: "fragment", origin: "https://senulun.example.com#", token: token, invalid: true},
		{name: "credentials", origin: "https://user:private@senulun.example.com", token: token, invalid: true},
		{name: "invalid port", origin: "https://senulun.example.com:65536", token: token, invalid: true},
		{name: "missing token", origin: "https://senulun.example.com", invalid: true},
		{name: "short token", origin: "https://senulun.example.com", token: "too-short", invalid: true},
		{name: "long token", origin: "https://senulun.example.com", token: strings.Repeat("x", 4097), invalid: true},
		{name: "line break", origin: "https://senulun.example.com", token: token + "\n", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, _ := json.Marshal(ServiceConfig{PublicURL: test.origin})
			secrets, _ := json.Marshal(ServiceSecrets{SetupToken: test.token})
			_, _, err := DecodeServiceConfig(config, secrets)
			if (err != nil) != test.invalid {
				t.Fatalf("invalid=%v err=%v", test.invalid, err)
			}
			if err != nil && test.token != "" && strings.Contains(err.Error(), test.token) {
				t.Fatal("validation error exposed a setup token")
			}
		})
	}
	if _, _, err := DecodeServiceConfig(json.RawMessage(`[]`), json.RawMessage(`{}`)); err == nil {
		t.Fatal("non-object configuration was accepted")
	}
}
