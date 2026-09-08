package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/recovery"
)

// ExternalRecoveryEvidence is an explicit operator attestation. Vastora checks
// the downloaded artifact digest and ownership contract; it does not claim to
// decrypt arbitrary third-party formats or to have performed the restore drill.
type ExternalRecoveryEvidence struct {
	FormatVersion     int       `json:"formatVersion"`
	ApplicationID     string    `json:"applicationId"`
	NodeID            string    `json:"nodeId"`
	SiteID            string    `json:"siteId"`
	AppKey            string    `json:"appKey"`
	AppVersion        string    `json:"appVersion"`
	PolicyVersion     int       `json:"policyVersion"`
	Consistency       string    `json:"consistency"`
	Volumes           []string  `json:"volumes"`
	Reference         string    `json:"reference"`
	ArtifactDigest    string    `json:"artifactDigest"`
	Encrypted         bool      `json:"encrypted"`
	CreatedAt         time.Time `json:"createdAt"`
	RestoreVerifiedAt time.Time `json:"restoreVerifiedAt"`
}

var recoveryReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)

func (s *Store) RegisterExternalRecoveryEvidence(ctx context.Context, value ExternalRecoveryEvidence, actualDigest string) error {
	if value.FormatVersion != recovery.FormatVersion || value.ArtifactDigest != actualDigest || !value.Encrypted || !recoveryReferencePattern.MatchString(value.Reference) || value.CreatedAt.IsZero() || value.CreatedAt.Before(s.now().Add(-recoveryMaxAge)) || value.RestoreVerifiedAt.Before(value.CreatedAt) || value.RestoreVerifiedAt.After(s.now().Add(time.Minute)) {
		return errors.New("center: external backup requires current encrypted artifact, digest and operator restore evidence")
	}
	var nodeID, siteID, appKey, version string
	err := s.db.QueryRowContext(ctx, `SELECT a.node_id, a.site_id, a.app_key, COALESCE((SELECT app_version FROM deployments d WHERE d.application_id = a.id AND d.state = 'succeeded' AND d.operation <> 'uninstall' ORDER BY d.created_at DESC, d.id DESC LIMIT 1), '') FROM applications a WHERE a.id = ? AND (a.status <> 'stopped' OR COALESCE((SELECT d.delete_data FROM deployments d WHERE d.application_id = a.id AND d.state = 'succeeded' AND d.operation = 'uninstall' ORDER BY d.created_at DESC, d.id DESC LIMIT 1), 0) = 0)`, value.ApplicationID).Scan(&nodeID, &siteID, &appKey, &version)
	if err != nil {
		return errors.New("center: application recovery ownership is unavailable")
	}
	policy, supported := catalog.OfficialRecoveryPolicy(appKey, version)
	if !supported || policy.Consistency == "reconstructible" {
		return errors.New("center: application does not declare this external backup contract")
	}
	expectedVolumes := slices.Clone(policy.Volumes)
	actualVolumes := slices.Clone(value.Volumes)
	slices.Sort(expectedVolumes)
	slices.Sort(actualVolumes)
	if nodeID != value.NodeID || siteID != value.SiteID || appKey != value.AppKey || version != value.AppVersion || policy.Version != value.PolicyVersion || policy.Consistency != value.Consistency || !slices.Equal(expectedVolumes, actualVolumes) {
		return errors.New("center: application backup identity, version or volume coverage does not match")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO recovery_evidence(component_key, artifact_json, verified_at) VALUES(?, ?, ?) ON CONFLICT(component_key) DO UPDATE SET artifact_json = excluded.artifact_json, verified_at = excluded.verified_at`, "application:"+value.ApplicationID, encoded, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) evaluateExternalRecoveryEvidence(ctx context.Context, component *RecoveryComponent, policy catalog.RecoveryPolicy, appKey, version string, now time.Time) error {
	var encoded []byte
	var verifiedAt string
	if err := s.db.QueryRowContext(ctx, `SELECT artifact_json, verified_at FROM recovery_evidence WHERE component_key = ?`, component.Key).Scan(&encoded, &verifiedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	var value ExternalRecoveryEvidence
	verified, err := time.Parse(time.RFC3339Nano, verifiedAt)
	if err != nil || json.Unmarshal(encoded, &value) != nil {
		component.Reason = "invalid_external_backup_evidence"
		return nil
	}
	component.LastVerifiedAt, component.ArtifactDigest = &verified, value.ArtifactDigest
	expected, actual := slices.Clone(policy.Volumes), slices.Clone(value.Volumes)
	slices.Sort(expected)
	slices.Sort(actual)
	if value.FormatVersion != recovery.FormatVersion || value.ApplicationID != component.ID || value.NodeID != component.NodeID || value.SiteID != component.SiteID || value.AppKey != appKey || value.AppVersion != version || value.PolicyVersion != policy.Version || value.Consistency != policy.Consistency || !slices.Equal(expected, actual) || !value.Encrypted || !recoveryReferencePattern.MatchString(value.Reference) {
		component.Reason = "external_backup_contract_changed"
		return nil
	}
	if value.CreatedAt.IsZero() || now.Sub(value.CreatedAt) > recoveryMaxAge || value.RestoreVerifiedAt.Before(value.CreatedAt) || value.RestoreVerifiedAt.After(now.Add(time.Minute)) || verified.After(now.Add(time.Minute)) || now.Sub(verified) > recoveryMaxAge {
		component.Reason = "external_backup_evidence_expired"
		return nil
	}
	component.State, component.Reason = "ready", "operator_restore_attested_and_artifact_digest_checked"
	return nil
}
