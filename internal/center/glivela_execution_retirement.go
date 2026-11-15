package center

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const applicationExecutionRetirementSchema = `CREATE TABLE application_execution_retirements (
 application_id TEXT PRIMARY KEY REFERENCES applications(id) ON DELETE RESTRICT,
 replacement_application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE RESTRICT,
 evidence_json TEXT NOT NULL,
 verified_at TEXT NOT NULL,
 created_at TEXT NOT NULL
)`

// GlivelaExecutionRetirement records an explicit local operator inspection. It
// is not an Agent receipt, an uninstall, or evidence that backups were restored.
type GlivelaExecutionRetirement struct {
	ApplicationID            string    `json:"applicationId"`
	ReplacementApplicationID string    `json:"replacementApplicationId"`
	SourceReceiptSHA256      string    `json:"sourceReceiptSha256"`
	ReplacementReceiptSHA256 string    `json:"replacementReceiptSha256"`
	LocalSourceReceiptSHA256 string    `json:"localSourceReceiptSha256,omitempty"`
	HandoverProofSHA256      string    `json:"handoverProofSha256,omitempty"`
	CheckpointSHA256         string    `json:"checkpointSha256,omitempty"`
	NativeConfigSHA256       string    `json:"nativeConfigSha256"`
	LegacyContainerID        string    `json:"legacyContainerId,omitempty"`
	LegacyState              string    `json:"legacyState"`
	LegacyStopped            bool      `json:"legacyStopped"`
	RestartDisabled          bool      `json:"restartDisabled"`
	NativeReady              bool      `json:"nativeReady"`
	VerifiedAt               time.Time `json:"verifiedAt"`
}

func sha256Text(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha256.Size
}

func legacyGlivelaReceipt(raw []byte) bool {
	var receipt struct {
		AppKey    string `json:"appKey"`
		Runtime   string `json:"runtime"`
		Resources []struct {
			Kind      string `json:"kind"`
			Component string `json:"component"`
		} `json:"resources"`
	}
	if json.Unmarshal(raw, &receipt) != nil || receipt.AppKey != meridianAppKey || receipt.Runtime != "docker" {
		return false
	}
	xray := false
	for _, resource := range receipt.Resources {
		if resource.Kind == "container" && resource.Component == "controller" {
			return false
		}
		xray = xray || resource.Kind == "container" && resource.Component == "xray"
	}
	return xray
}

// RetireGlivelaExecutionRecord archives only the old execution registration.
// Public services, account references, credentials and traffic stay attached to
// their original IDs. No node task or container operation is queued.
func (s *Store) RetireGlivelaExecutionRecord(ctx context.Context, evidence GlivelaExecutionRetirement) error {
	now := s.now().UTC()
	if evidence.ApplicationID == "" || evidence.ReplacementApplicationID == "" || evidence.ApplicationID == evidence.ReplacementApplicationID || !sha256Text(evidence.SourceReceiptSHA256) || !sha256Text(evidence.ReplacementReceiptSHA256) || !sha256Text(evidence.NativeConfigSHA256) || !evidence.NativeReady || !evidence.LegacyStopped || !evidence.RestartDisabled || evidence.VerifiedAt.IsZero() || now.Sub(evidence.VerifiedAt) > 15*time.Minute || evidence.VerifiedAt.After(now.Add(time.Minute)) {
		return errors.New("center: current explicit stopped-source and ready-replacement inspection required")
	}
	if evidence.LegacyState == "execution-retired" {
		if !sha256Text(evidence.LocalSourceReceiptSHA256) || !sha256Text(evidence.HandoverProofSHA256) || !sha256Text(evidence.CheckpointSHA256) || !sha256Text(evidence.LegacyContainerID) {
			return errors.New("center: exact retired execution receipt, handover and stopped container required")
		}
	} else if evidence.LegacyState != "absent-after-reinstall" || evidence.LegacyContainerID != "" || evidence.LocalSourceReceiptSHA256 != "" || evidence.HandoverProofSHA256 != "" || evidence.CheckpointSHA256 != "" {
		return errors.New("center: unsupported legacy execution retirement evidence")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldNode, newNode, oldKey, newKey, oldState, newState, oldAdoption, newAdoption string
	var oldRaw, newRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT old.node_id,new.node_id,old.app_key,new.app_key,old.status,new.status,old_resources.adoption_state,new_resources.adoption_state,old_resources.resources_json,new_resources.resources_json
 FROM applications old JOIN applications new ON new.id=?
 JOIN application_resources old_resources ON old_resources.application_id=old.id
 JOIN application_resources new_resources ON new_resources.application_id=new.id
 JOIN agents node ON node.id=old.node_id AND node.status='active' AND node.credential_revoked_at=''
 WHERE old.id=?`, evidence.ReplacementApplicationID, evidence.ApplicationID).Scan(&oldNode, &newNode, &oldKey, &newKey, &oldState, &newState, &oldAdoption, &newAdoption, &oldRaw, &newRaw)
	if err != nil || oldNode != newNode || oldKey != meridianAppKey || newKey != "vastora-official/glivela-entry" || newState != "running" || oldAdoption != "ready" || newAdoption != "ready" || !legacyGlivelaReceipt(oldRaw) || rawManifestDigest(oldRaw) != evidence.SourceReceiptSHA256 || rawManifestDigest(newRaw) != evidence.ReplacementReceiptSHA256 {
		return errors.New("center: inspected legacy execution and same-node native replacement no longer match")
	}
	var replacement struct {
		ApplicationID string `json:"applicationId"`
		Runtime       string `json:"runtime"`
		State         string `json:"state"`
	}
	if json.Unmarshal(newRaw, &replacement) != nil || replacement.ApplicationID != evidence.ReplacementApplicationID || replacement.Runtime != "systemd" || replacement.State != "ready" {
		return errors.New("center: verified ready native replacement receipt required")
	}
	if evidence.LegacyState == "absent-after-reinstall" {
		if oldState != "pending" {
			return errors.New("center: absent source is only valid for an unfulfilled reinstall record")
		}
	} else {
		var source struct {
			Resources []struct {
				Kind string `json:"kind"`
				ID   string `json:"id"`
			} `json:"resources"`
		}
		if json.Unmarshal(oldRaw, &source) != nil {
			return errors.New("center: invalid retained source receipt")
		}
		matched := false
		for _, resource := range source.Resources {
			if resource.Kind == "container" {
				if resource.ID != evidence.LegacyContainerID {
					return errors.New("center: legacy container identity differs from the retained receipt")
				}
				matched = true
			}
		}
		if !matched {
			return errors.New("center: retained source container ownership required")
		}
	}
	// This changes only installation metadata. Fence work for these exact two
	// applications, without claiming or resolving unrelated node operations.
	var active int
	err = tx.QueryRowContext(ctx, `SELECT
 (SELECT COUNT(*) FROM deployments WHERE application_id IN (?,?) AND (state IN ('pending','running') OR reconciliation_required=1))+
 (SELECT COUNT(*) FROM application_commands WHERE application_id IN (?,?) AND (state IN ('pending','running') OR reconciliation_required=1))+
 (SELECT COUNT(*) FROM application_adoptions WHERE application_id IN (?,?) AND state IN ('pending','running'))+
 (SELECT COUNT(*) FROM application_maintenance WHERE application_id IN (?,?) AND (state IN ('pending','running') OR reconciliation_required=1))`, evidence.ApplicationID, evidence.ReplacementApplicationID, evidence.ApplicationID, evidence.ReplacementApplicationID, evidence.ApplicationID, evidence.ReplacementApplicationID, evidence.ApplicationID, evidence.ReplacementApplicationID).Scan(&active)
	if err != nil {
		return err
	}
	if active != 0 {
		return errors.New("center: resolve unfinished source or replacement application operations before archiving")
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT replacement_application_id FROM application_execution_retirements WHERE application_id=?`, evidence.ApplicationID).Scan(&existing)
	if err == nil {
		if existing != evidence.ReplacementApplicationID {
			return errors.New("center: legacy execution was retired to a different replacement")
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO application_execution_retirements(application_id,replacement_application_id,evidence_json,verified_at,created_at) VALUES(?,?,?,?,?)`, evidence.ApplicationID, evidence.ReplacementApplicationID, raw, evidence.VerifiedAt.UTC().Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}
