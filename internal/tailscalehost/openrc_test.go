package tailscalehost

import (
	"strings"
	"testing"
)

func TestOpenRCRuntimeEvidenceIncludesOnlyCheckedSettings(t *testing.T) {
	raw := normalizedOpenRCTailscaleEnvironment([]byte("SECRET=private\x00TS_NO_LOGS_NO_SUPPORT=true\x00"), []byte("/usr/sbin/tailscaled\x00--port=41641\x00--config=/etc/vastora/tailscaled.json\x00--auth-key=private\x00"))
	value := string(raw)
	for _, want := range []string{"TS_NO_LOGS_NO_SUPPORT=true", "PORT=41641", "FLAGS=--config=/etc/vastora/tailscaled.json"} {
		if !strings.Contains(value, want) {
			t.Fatal("missing checked runtime setting")
		}
	}
	if strings.Contains(value, "private") || strings.Contains(value, "auth-key") {
		t.Fatal("exposed unrelated daemon secrets")
	}
}
