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
	if task.Inspection.Validate() != nil || !validNodeID(task.NodeID) {
		return errors.New("pulse: invalid original node rotation task")
	}
	return nil
}

func (result RotationResult) Validate(task RotationTask) error {
	if task.Validate() != nil || result.NodeID != task.NodeID || !validAgentToken(result.Token) {
		return errors.New("pulse: invalid original node rotation result")
	}
	return nil
}

func validNodeID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, " \t\r\n\x00") && !strings.HasPrefix(id, "-")
}
func validAgentToken(token string) bool {
	return len(token) >= 32 && len(token) <= 512 && !strings.ContainsAny(token, " \t\r\n\x00")
}
