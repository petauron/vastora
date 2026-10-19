package senulun

import (
	"encoding/json"
	"testing"
)

func TestServiceAuthenticationConfiguration(t *testing.T) {
	for _, test := range []struct {
		name, origin string
		invalid      bool
	}{
		{name: "HTTPS origin", origin: "https://senulun.example.com"},
		{name: "HTTPS root", origin: "https://senulun.example.com/"},
		{name: "HTTPS port", origin: "https://senulun.example.com:8443"},
		{name: "missing origin", invalid: true},
		{name: "HTTP origin", origin: "http://127.0.0.1:18080", invalid: true},
		{name: "path", origin: "https://senulun.example.com/login", invalid: true},
		{name: "query", origin: "https://senulun.example.com?next=login", invalid: true},
		{name: "empty query", origin: "https://senulun.example.com?", invalid: true},
		{name: "fragment", origin: "https://senulun.example.com#", invalid: true},
		{name: "credentials", origin: "https://user:private@senulun.example.com", invalid: true},
		{name: "invalid port", origin: "https://senulun.example.com:65536", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, _ := json.Marshal(ServiceConfig{PublicURL: test.origin})
			_, err := DecodeServiceConfig(config)
			if (err != nil) != test.invalid {
				t.Fatalf("invalid=%v err=%v", test.invalid, err)
			}
		})
	}
	if _, err := DecodeServiceConfig(json.RawMessage(`[]`)); err == nil {
		t.Fatal("non-object configuration was accepted")
	}
}
