package controlplane

import (
	"strings"
	"testing"
)

func TestSafeErrorRemovesCredentialShapedMaterial(t *testing.T) {
	value := SafeError(`request failed Authorization: Bearer abcdefghijklmnopqrstuvwxyz123456 token=short-secret {"password":"tiny","credential":"cred","apiKey":"key"} https://user:pass@example.test/path?access_token=query-secret&safe=no`)
	for _, secret := range []string{"abcdefghijklmnopqrstuvwxyz", "short-secret", `:"tiny"`, `:"cred"`, `:"key"`, "user:pass", "query-secret", "safe=no"} {
		if strings.Contains(value, secret) {
			t.Fatalf("unsafe error contains %q: %q", secret, value)
		}
	}
	if strings.Count(value, "[redacted]") < 6 {
		t.Fatalf("unsafe error = %q", value)
	}
}
