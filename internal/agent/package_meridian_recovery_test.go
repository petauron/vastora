package agent

import (
	"context"
	"testing"
)

func TestMeridianPackageRecoveryRetainedContainerGuard(t *testing.T) {
	for _, tc := range []struct {
		name          string
		containerName string
		reviewedID    string
		allowed       bool
	}{
		{"unreviewed", meridianXrayCandidateContainer, "", false},
		{"changed identity", meridianXrayCandidateContainer, "different", false},
		{"reviewed candidate", meridianXrayCandidateContainer, "old", true},
		{"backup is not package candidate", meridianXrayBackupContainer, "old", false},
		{"cleanup is not package candidate", meridianXrayCleanupContainer, "old", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := newFakeThreeXUIContainerEngine(t, true)
			delete(engine.names, threeXUIContainer)
			engine.names[tc.containerName] = "old"
			engine.containers["old"].name = tc.containerName
			labels := applicationResourceLabels(meridianKey, "xray", "app", "deployment")
			labels[xrayWorkerRuntimeLabel] = "xray"
			engine.containers["old"].labels = labels
			err := requireNoUnreviewedXrayWorkerDeploy(context.Background(), engine, tc.reviewedID)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
		})
	}
}
