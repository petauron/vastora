package pulse

import "testing"

func TestPulseRestoreRequiresCanonicalImporterIdentity(t *testing.T) {
	for _, nodeID := range []string{
		"11111111-1111-4111-8111-111111111111",
		"original-node",
		"11111111111141118111111111111111",
		"urn:uuid:11111111-1111-4111-8111-111111111111",
		"AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA",
	} {
		t.Run(nodeID, func(t *testing.T) {
			credentials := RestoreCredentials{NodeID: nodeID, Token: "test-rotated-credential-never-publish"}
			valid := nodeID == "11111111-1111-4111-8111-111111111111"
			if (credentials.Validate() == nil) != valid {
				t.Fatal("restore identity does not match the importer contract")
			}
		})
	}
}
