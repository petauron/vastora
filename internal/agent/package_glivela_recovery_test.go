package agent

import (
	"context"
	"testing"
)

func TestGlivelaPackageRecoveryRetainedContainerGuard(t *testing.T) {
	for _, tc := range []struct {
		name          string
		containerName string
		reviewedID    string
		allowed       bool
	}{
		{"unreviewed", legacyXrayCandidateContainer, "", false},
		{"changed identity", legacyXrayCandidateContainer, "different", false},
		{"reviewed candidate", legacyXrayCandidateContainer, "old", true},
		{"backup is not package candidate", glivelaXrayBackupContainer, "old", false},
		{"cleanup is not package candidate", glivelaXrayCleanupContainer, "old", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := newFakeThreeXUIContainerEngine(t, true)
			delete(engine.names, threeXUIContainer)
			engine.names[tc.containerName] = "old"
			engine.containers["old"].name = tc.containerName
			labels := applicationResourceLabels(legacyProxyAppKey, "xray", "app", "deployment")
			labels[xrayWorkerRuntimeLabel] = "xray"
			engine.containers["old"].labels = labels
			err := requireNoUnreviewedXrayWorkerDeploy(context.Background(), engine, tc.reviewedID)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
		})
	}
}
