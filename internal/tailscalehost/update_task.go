package tailscalehost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const UpdateKind = "tailscale.update"
const UpdateTaskPrefix = "tailscale-update-"

type UpdateTask struct {
	TargetVersion string `json:"targetVersion"`
}

func (t UpdateTask) Validate() error {
	value, err := CompatibleVersion(t.TargetVersion)
	if err != nil || value != t.TargetVersion {
		return errors.New("invalid stable Tailscale target version")
	}
	return nil
}

// Unit names are derived, never interpolated from a task into a shell.
func UpdateUnitName(taskID string) string {
	if !strings.HasPrefix(taskID, UpdateTaskPrefix) || len(taskID) > 128 {
		return ""
	}
	hash := sha256.Sum256([]byte(taskID))
	return "vastora-tailscale-update-" + hex.EncodeToString(hash[:12]) + ".service"
}

type UpdateObservation struct {
	TaskID           string `json:"taskId"`
	State            string `json:"state"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	RunningVersion   string `json:"runningVersion,omitempty"`
}
