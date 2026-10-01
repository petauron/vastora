package pulse

import (
	"errors"
	"strings"
)

const InspectionKind = "pulse.enrollment.inspect"
const MaxInspectionEnrollments = 64

type InspectionTask struct {
	ApplicationID string   `json:"applicationId"`
	DeploymentID  string   `json:"deploymentId"`
	EnrollmentIDs []string `json:"enrollmentIds"`
}

type EnrollmentRecord struct {
	ID               string  `json:"id"`
	ExpiresAtUnixMS  int64   `json:"expires_at_unix_ms"`
	ConsumedAtUnixMS *int64  `json:"consumed_at_unix_ms"`
	NodeID           *string `json:"node_id"`
	NodeActive       bool    `json:"node_active"`
}

type InspectionResult struct {
	Records []EnrollmentRecord `json:"records"`
}

func (task InspectionTask) Validate() error {
	if task.ApplicationID == "" || task.DeploymentID == "" || len(task.EnrollmentIDs) == 0 || len(task.EnrollmentIDs) > MaxInspectionEnrollments {
		return errors.New("pulse: invalid enrollment inspection task")
	}
	seen := map[string]bool{}
	for _, id := range task.EnrollmentIDs {
		if id == "" || len(id) > 128 || strings.ContainsAny(id, "\r\n\x00") || strings.TrimSpace(id) != id || seen[id] {
			return errors.New("pulse: invalid enrollment inspection identity")
		}
		seen[id] = true
	}
	return nil
}

func (result InspectionResult) Validate(task InspectionTask) error {
	if task.Validate() != nil || len(result.Records) != len(task.EnrollmentIDs) {
		return errors.New("pulse: incomplete enrollment inspection")
	}
	for i, record := range result.Records {
		if record.ID != task.EnrollmentIDs[i] || record.ExpiresAtUnixMS < 0 ||
			(record.ConsumedAtUnixMS != nil && *record.ConsumedAtUnixMS < 0) ||
			(record.NodeID != nil && (*record.NodeID == "" || len(*record.NodeID) > 128 || record.ConsumedAtUnixMS == nil)) ||
			(record.NodeActive && record.NodeID == nil) {
			return errors.New("pulse: invalid enrollment inspection result")
		}
	}
	return nil
}

// A consumed enrollment whose node has since been revoked/deleted must be
// reviewed, even if another record still points to an active node.
func (result InspectionResult) OriginalNodeID(task InspectionTask) (string, error) {
	if err := result.Validate(task); err != nil {
		return "", err
	}
	id := ""
	for _, record := range result.Records {
		if record.ConsumedAtUnixMS == nil {
			continue
		}
		if record.NodeID == nil || !record.NodeActive || (id != "" && id != *record.NodeID) {
			return "", errors.New("pulse: original monitoring identity requires review")
		}
		id = *record.NodeID
	}
	if id == "" {
		return "", errors.New("pulse: no consumed enrollment identifies an active monitoring node")
	}
	return id, nil
}
