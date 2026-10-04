package meridianruntime

import "testing"

func TestAcceptanceResultBindsCurrentRecoveryInputs(t *testing.T) {
	original := AcceptanceTask{VerifierFingerprint: "verifier-key", OperationID: "operation", PlanRevision: "revision", TargetAgentID: "target", VerifierAgentID: "verifier", IdentityFingerprint: "identity", Client: acceptanceFixture(), ExpectedExit: "1.1.1.1"}
	digest, err := original.Digest()
	if err != nil {
		t.Fatal(err)
	}
	result := AcceptanceResult{TaskDigest: digest}
	if err := result.Validate(original); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*AcceptanceTask){
		"operation":  func(v *AcceptanceTask) { v.OperationID = "new" },
		"plan":       func(v *AcceptanceTask) { v.PlanRevision = "new" },
		"identity":   func(v *AcceptanceTask) { v.IdentityFingerprint = "new" },
		"target":     func(v *AcceptanceTask) { v.TargetAgentID = "new" },
		"verifier":   func(v *AcceptanceTask) { v.VerifierAgentID = "new" },
		"exit":       func(v *AcceptanceTask) { v.ExpectedExit = "8.8.8.8" },
		"credential": func(v *AcceptanceTask) { v.Client.Material.Credential.ID = "new" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			change(&changed)
			if result.Validate(changed) == nil {
				t.Fatal("old evidence accepted after input changed")
			}
		})
	}
	for _, invalid := range []AcceptanceTask{
		{},
		{OperationID: "operation", PlanRevision: "revision", IdentityFingerprint: "identity", TargetAgentID: "target", VerifierAgentID: "target", Client: acceptanceFixture(), ExpectedExit: "1.1.1.1"},
		{OperationID: "operation", PlanRevision: "revision", IdentityFingerprint: "identity", TargetAgentID: "target", VerifierAgentID: "verifier", Client: acceptanceFixture(), ExpectedExit: "127.0.0.1"},
	} {
		if invalid.Validate() == nil {
			t.Fatal("invalid acceptance authority accepted")
		}
	}
}
