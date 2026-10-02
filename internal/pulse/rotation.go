package pulse

import (
	"errors"
	"strings"
)

const RotationKind = "pulse.node.rotate"

// Rotation keeps the existing monitoring node. Enrollment evidence is re-read
// on the managed Service immediately before invoking its fixed rotation CLI.
type RotationTask struct {
	Inspection InspectionTask `json:"inspection"`
	NodeID     string         `json:"nodeId"`
}

type RotationResult struct {
	NodeID string `json:"nodeId"`
	Token  string `json:"token"`
}

func (task RotationTask) Validate() error {
	if task.Inspection.Validate() != nil || task.NodeID == "" || len(task.NodeID) > 128 || strings.ContainsAny(task.NodeID, " \t\r\n\x00") || strings.HasPrefix(task.NodeID, "-") {
		return errors.New("pulse: invalid original node rotation task")
	}
	return nil
}

func (result RotationResult) Validate(task RotationTask) error {
	if task.Validate() != nil || result.NodeID != task.NodeID || len(result.Token) < 32 || len(result.Token) > 512 || strings.ContainsAny(result.Token, " \t\r\n\x00") {
		return errors.New("pulse: invalid original node rotation result")
	}
	return nil
}
