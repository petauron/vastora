package pulse

import "testing"

func TestPulseReportingRequiresFreshSampleAfterRotation(t *testing.T) {
	task := ReportingTask{ApplicationID: "monitor", DeploymentID: "deployment", Credentials: RestoreCredentials{NodeID: "11111111-1111-4111-8111-111111111111", Token: "test-rotated-credential-never-publish"}}
	for _, mode := range []string{"fresh", "missing", "before", "equal", "stale", "future", "wrong-node"} {
		t.Run(mode, func(t *testing.T) {
			rotation, sample := int64(1000), int64(2000)
			result := ReportingResult{NodeID: task.Credentials.NodeID, ObservedAt: 3000, RotatedAt: &rotation, LastSeenAt: &sample}
			switch mode {
			case "missing":
				result.LastSeenAt = nil
			case "before":
				sample = 999
			case "equal":
				sample = rotation
			case "stale":
				result.ObservedAt = sample + 600001
			case "future":
				sample = 3001
			case "wrong-node":
				result.NodeID = "other"
			}
			if result.FreshAfterRotation(task) != (mode == "fresh") {
				t.Fatal("retained, missing or stale report counted as recovery")
			}
		})
	}
}
