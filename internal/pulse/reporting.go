package pulse

import (
	"errors"
	"time"
)

const ReportingKind = "pulse.node.reporting"

type ReportingTask struct {
	ApplicationID string             `json:"applicationId"`
	DeploymentID  string             `json:"deploymentId"`
	Credentials   RestoreCredentials `json:"credentials"`
}

func (task ReportingTask) Validate() error {
	if task.ApplicationID == "" || task.DeploymentID == "" || task.Credentials.Validate() != nil {
		return errors.New("pulse: invalid original monitoring reporting task")
	}
	return nil
}

// These are Service-local timestamps. A retained sample is not evidence of
// reporting under the rotated credential until it is strictly after rotation.
type ReportingResult struct {
	NodeID     string `json:"node_id"`
	ObservedAt int64  `json:"observed_at_unix_ms"`
	RotatedAt  *int64 `json:"rotated_at_unix_ms"`
	LastSeenAt *int64 `json:"last_seen_at_unix_ms"`
}

func (result ReportingResult) Validate(task ReportingTask) error {
	if task.Validate() != nil || result.NodeID != task.Credentials.NodeID || result.ObservedAt <= 0 ||
		(result.RotatedAt != nil && (*result.RotatedAt <= 0 || *result.RotatedAt > result.ObservedAt)) ||
		(result.LastSeenAt != nil && (*result.LastSeenAt <= 0 || *result.LastSeenAt > result.ObservedAt)) {
		return errors.New("pulse: invalid original monitoring reporting response")
	}
	return nil
}

func (result ReportingResult) FreshAfterRotation(task ReportingTask) bool {
	return result.Validate(task) == nil && result.RotatedAt != nil && result.LastSeenAt != nil &&
		*result.LastSeenAt > *result.RotatedAt && result.ObservedAt-*result.LastSeenAt <= (10*time.Minute).Milliseconds()
}
