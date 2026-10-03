package pulse

import "testing"

func TestPulseInspectionRequiresCompleteUnambiguousActiveIdentity(t *testing.T) {
	consumed := int64(100)
	first, second := "original-node", "another-node"
	task := InspectionTask{ApplicationID: "service", DeploymentID: "deployment", EnrollmentIDs: []string{"unused", "used"}}
	base := []EnrollmentRecord{{ID: "unused", ExpiresAtUnixMS: 99}, {ID: "used", ExpiresAtUnixMS: 99, ConsumedAtUnixMS: &consumed, NodeID: &first, NodeActive: true}}
	for _, mode := range []string{"expired-valid", "same-node", "unconsumed", "partial", "reordered", "revoked", "deleted", "ambiguous", "invalid-active"} {
		t.Run(mode, func(t *testing.T) {
			records := append([]EnrollmentRecord(nil), base...)
			switch mode {
			case "same-node":
				records[0].ConsumedAtUnixMS = &consumed
				records[0].NodeID = &first
				records[0].NodeActive = true
			case "unconsumed":
				records[1].ConsumedAtUnixMS = nil
				records[1].NodeID = nil
				records[1].NodeActive = false
			case "partial":
				records = records[:1]
			case "reordered":
				records[0], records[1] = records[1], records[0]
			case "revoked":
				records[1].NodeActive = false
			case "deleted":
				records[1].NodeID = nil
				records[1].NodeActive = false
			case "ambiguous":
				records[0].ConsumedAtUnixMS = &consumed
				records[0].NodeID = &second
				records[0].NodeActive = true
			case "invalid-active":
				records[0].NodeActive = true
			}
			id, err := (InspectionResult{Records: records}).OriginalNodeID(task)
			valid := mode == "expired-valid" || mode == "same-node"
			if valid && (err != nil || id != first) {
				t.Fatalf("lost historical identity: %q %v", id, err)
			}
			if !valid && (err == nil || id != "") {
				t.Fatalf("selected incomplete or ambiguous identity: %q %v", id, err)
			}
		})
	}
}
