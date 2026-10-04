package meridianruntime

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/petauron/meridian"
)

func acceptanceFixture() AcceptanceClient {
	id := "00000000-0000-4000-8000-000000000001"
	return AcceptanceClient{Protocol: meridian.VLESSReality,
		Reality:  &meridian.RealityEndpoint{ID: "endpoint", EntryID: "entry", AdvertiseHost: "entry.example.test", AdvertisePort: 443, ServerNames: []string{"www.example.com"}, ShortIDs: []string{"0123456789abcdef"}, PublicKey: base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
		Material: meridian.CredentialMaterial{Credential: meridian.Credential{ID: "credential", AccountID: "account", EntryID: "entry", User: "test", Kind: meridian.NativeCredential, Identity: meridian.Identity(id), Enabled: true}, ProtocolID: id, HysteriaAuth: "synthetic-hy2", HysteriaIdentity: meridian.Identity("synthetic-hy2")},
	}
}

func TestAcceptanceClientPreservesProtocolIdentityWithoutFallback(t *testing.T) {
	for _, protocol := range []meridian.ProtocolKind{meridian.VLESSReality, meridian.Hysteria2} {
		for _, routed := range []bool{false, true} {
			c := acceptanceFixture()
			c.Protocol = protocol
			if routed {
				c.Material.Credential.Kind = meridian.RouteCredential
				c.Material.Credential.EgressID = "landing"
				if protocol == meridian.VLESSReality {
					c.Material.HysteriaAuth, c.Material.HysteriaIdentity = "", ""
				}
			}
			if protocol == meridian.Hysteria2 {
				c.Reality = nil
				c.Hysteria = &meridian.HysteriaEndpoint{ID: "endpoint", EntryID: "entry", AdvertiseHost: "entry.example.test", AdvertisePort: 443, ServerName: "entry.example.test"}
			}
			encoded, err := c.Config(18080)
			if protocol == meridian.Hysteria2 && routed {
				if err == nil {
					t.Fatal("unsupported routed Hysteria credential accepted")
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Inbounds  []map[string]any
				Outbounds []map[string]any
			}
			if json.Unmarshal(encoded, &config) != nil || len(config.Inbounds) != 1 || len(config.Outbounds) != 1 {
				t.Fatal("unexpected client topology")
			}
			if config.Inbounds[0]["listen"] != "127.0.0.1" || config.Inbounds[0]["port"] != float64(18080) || strings.Contains(string(encoded), "freedom") || strings.Contains(string(encoded), "fallback") {
				t.Fatal("client escaped isolated proxy path")
			}
			out := config.Outbounds[0]
			settings := out["settings"].(map[string]any)
			if settings["address"] != "entry.example.test" || settings["port"] != float64(443) {
				t.Fatal("original entry changed")
			}
			if protocol == meridian.VLESSReality && settings["id"] != c.Material.ProtocolID {
				t.Fatal("original VLESS identity changed")
			}
			if protocol == meridian.Hysteria2 {
				stream := out["streamSettings"].(map[string]any)
				if stream["hysteriaSettings"].(map[string]any)["auth"] != c.Material.HysteriaAuth || stream["tlsSettings"].(map[string]any)["allowInsecure"] != false {
					t.Fatal("Hysteria identity or TLS changed")
				}
			}
		}
	}
}

func TestAcceptanceClientRejectsDisabledAndPrivateIdentity(t *testing.T) {
	for _, mode := range []string{"disabled", "private", "mismatch", "unsupported", "privileged-port"} {
		c := acceptanceFixture()
		port := 18080
		switch mode {
		case "disabled":
			c.Material.Credential.Enabled = false
		case "private":
			c.Reality.PrivateKey = "never-send-server-key"
		case "mismatch":
			c.Material.ProtocolID = "00000000-0000-4000-8000-000000000002"
		case "unsupported":
			c.Protocol = "unknown"
		case "privileged-port":
			port = 80
		}
		if _, err := c.Config(port); err == nil {
			t.Fatalf("accepted %s", mode)
		}
	}
}
