package meridianruntime

import (
	"encoding/base64"
	"testing"
)

func validLegacyExportResult() (LegacyExportCommand, LegacyExportResult) {
	const applicationID = "legacy-controller"
	x25519 := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	command := LegacyExportCommand{ApplicationID: applicationID}
	result := LegacyExportResult{Export: LegacyExport{
		ControllerApplicationID: applicationID,
		Endpoints: []LegacyEndpoint{{
			InboundID: 1, Tag: "node-1", DisplayName: "US node", Listen: "0.0.0.0", Port: 443,
			Enabled: true, Target: "www.example.com:443", ServerNames: []string{"www.example.com"},
			PrivateKey: x25519, PublicKey: x25519, ShortIDs: []string{"ab"}, Fingerprint: "chrome", VLESSEnabled: true,
		}},
		Clients: []LegacyClient{{
			Email: "account@example.test", UUID: "11111111-2222-4333-8444-555555555555", SubscriptionToken: "subscription-token",
			Enabled: true, InboundIDs: []int{1}, HY2AuthByInbound: map[int]string{},
		}},
	}}
	return command, result
}

func TestLegacyExportRejectsHysteriaCredentialForUnmanagedEntry(t *testing.T) {
	command, result := validLegacyExportResult()
	if err := result.Validate(command); err != nil {
		t.Fatalf("valid export=%v", err)
	}
	result.Export.Clients[0].HY2AuthByInbound[2] = "unmanaged-secret"
	if err := result.Validate(command); err == nil {
		t.Fatal("legacy export accepted a Hysteria credential outside the client's managed entries")
	}
}
