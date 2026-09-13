package controlplane

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// LegacyReceipt is evidence for one-time protocol cutover, never a command or
// a request to replay an operation. Completion may contain generated secrets.
type LegacyReceipt struct {
	TaskID            string          `json:"taskId"`
	Kind              string          `json:"kind"`
	Attempt           int64           `json:"attempt"`
	RuntimeGeneration int             `json:"runtimeGeneration"`
	TaskHash          []byte          `json:"taskHash"`
	State             string          `json:"state"`
	Completion        json.RawMessage `json:"completion,omitempty"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
}

const LegacyReceiptMaxCompletionBytes = 2 << 20
const LegacyReceiptMaxPayloadBytes = 3 << 20

type LegacyReceiptImport struct {
	SessionID string        `json:"sessionId"`
	Digest    string        `json:"digest"`
	Receipt   LegacyReceipt `json:"receipt"`
}

func LegacyReceiptArchiveID(agentID, taskID string, attempt int64) string {
	identity, _ := json.Marshal([]any{agentID, taskID, attempt})
	digest := sha256.Sum256(identity)
	return "legacy-receipt-" + hex.EncodeToString(digest[:])
}

func EncodeLegacyReceipt(item LegacyReceipt) ([]byte, string, error) {
	invalid := errors.New("invalid legacy receipt evidence")
	if item.TaskID == "" || len(item.TaskID) > 512 || item.Kind == "" || len(item.Kind) > 128 || item.Attempt <= 0 || item.RuntimeGeneration < 0 || len(item.TaskHash) != sha256.Size || len(item.Completion) > LegacyReceiptMaxCompletionBytes {
		return nil, "", invalid
	}
	switch item.State {
	case "processing", "completed", "acknowledged", "reconciliation_required", "reconciliation_acknowledged":
	default:
		return nil, "", invalid
	}
	for _, stamp := range []string{item.CreatedAt, item.UpdatedAt} {
		if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			return nil, "", invalid
		}
	}
	if len(item.Completion) > 0 {
		var identity struct {
			TaskID  string `json:"taskId"`
			Attempt int64  `json:"attempt"`
		}
		if json.Unmarshal(item.Completion, &identity) != nil || identity.TaskID != item.TaskID || identity.Attempt != item.Attempt {
			return nil, "", invalid
		}
	} else if item.State != "processing" && item.State != "acknowledged" {
		return nil, "", invalid
	}
	raw, err := json.Marshal(item)
	if err != nil {
		return nil, "", invalid
	}
	digest := sha256.Sum256(raw)
	return raw, hex.EncodeToString(digest[:]), nil
}
