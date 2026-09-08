package center

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/agent"
	"github.com/petauron/vastora/internal/backupcrypto"
	"github.com/petauron/vastora/internal/catalog"
	"github.com/petauron/vastora/internal/recovery"
	"github.com/petauron/vastora/internal/secret"
)

const recoveryMaxAge = 24 * time.Hour

type RecoveryComponent struct {
	Key            string                  `json:"key"`
	Kind           string                  `json:"kind"`
	ID             string                  `json:"id"`
	SiteID         string                  `json:"siteId,omitempty"`
	NodeID         string                  `json:"nodeId,omitempty"`
	Classification string                  `json:"classification"`
	State          string                  `json:"state"`
	Reason         string                  `json:"reason"`
	Release        string                  `json:"release"`
	SchemaVersion  int                     `json:"schemaVersion,omitempty"`
	IdentityHash   string                  `json:"identityHash,omitempty"`
	Dependencies   []string                `json:"dependencies"`
	LastVerifiedAt *time.Time              `json:"lastVerifiedAt,omitempty"`
	ArtifactDigest string                  `json:"artifactDigest,omitempty"`
	BackupPolicy   *catalog.RecoveryPolicy `json:"backupPolicy,omitempty"`
}

type RecoveryReadiness struct {
	FormatVersion int                 `json:"formatVersion"`
	Release       string              `json:"release"`
	State         string              `json:"state"`
	CheckedAt     time.Time           `json:"checkedAt"`
	Components    []RecoveryComponent `json:"components"`
}

// RecoveryReadiness does not probe hosts or read arbitrary artifact paths. An
// authenticated read reports previously verified local evidence, with expiry.
func (s *Store) RecoveryReadiness(ctx context.Context) (RecoveryReadiness, error) {
	result := RecoveryReadiness{FormatVersion: recovery.FormatVersion, Release: Version, State: "ready", CheckedAt: s.now().UTC()}
	result.Components = append(result.Components, RecoveryComponent{Key: "center", Kind: "center", ID: "center", Classification: "authoritative", Release: Version, SchemaVersion: centerSchemaVersion, IdentityHash: recovery.Digest(s.key), Dependencies: []string{}})
	var headscaleMode, headscaleEndpoint, headscaleStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT mode, endpoint, status FROM network_integrations WHERE kind = 'headscale' AND endpoint <> ''`).Scan(&headscaleMode, &headscaleEndpoint, &headscaleStatus); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if headscaleEndpoint != "" {
		component := RecoveryComponent{Key: "headscale", Kind: "headscale", ID: strings.TrimSuffix(headscaleEndpoint, "/"), Classification: "authoritative", Release: Version, Dependencies: []string{"center"}}
		if headscaleMode != "builtin" {
			component.State, component.Reason = "unsupported", "external_headscale_requires_operator_recovery"
		} else if headscaleStatus != "configured" {
			component.State, component.Reason = "action_required", "headscale_configuration_unavailable"
		}
		result.Components = append(result.Components, component)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, site_id, version, x25519_public_key, capabilities_json FROM agents WHERE status = 'active' ORDER BY site_id, id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id, site, version string
		var publicKey, caps []byte
		if err := rows.Scan(&id, &site, &version, &publicKey, &caps); err != nil {
			rows.Close()
			return result, err
		}
		dependencies := []string{"center"}
		if headscaleEndpoint != "" {
			dependencies = append(dependencies, "headscale")
		}
		component := RecoveryComponent{Key: "agent:" + id, Kind: "agent", ID: id, SiteID: site, NodeID: id, Classification: "authoritative", Release: version, SchemaVersion: agent.CurrentSchemaVersion(), IdentityHash: recovery.Digest(publicKey), Dependencies: dependencies}
		if len(publicKey) == 0 || version != Version {
			component.State, component.Reason = "action_required", "matching_agent_release_required"
		}
		result.Components = append(result.Components, component)
		var capabilities NodeCapabilities
		if json.Unmarshal(caps, &capabilities) != nil {
			rows.Close()
			return result, errors.New("center: recovery inventory contains invalid node capabilities")
		}
		if capabilities.Gateway || capabilities.Docker || capabilities.Tunnel {
			result.Components = append(result.Components, RecoveryComponent{Key: "ingress:" + id, Kind: "ingress", ID: id, SiteID: site, NodeID: id, Classification: "reconstructible", Release: version, Dependencies: []string{"center", "agent:" + id}})
		}
	}
	readErr := rows.Err()
	if err := rows.Close(); err != nil {
		return result, err
	}
	if readErr != nil {
		return result, readErr
	}
	appMetadata := map[string][2]string{}
	rows, err = s.db.QueryContext(ctx, `SELECT a.id, a.node_id, a.site_id, a.app_key,
		COALESCE((SELECT app_version FROM deployments d WHERE d.application_id = a.id AND d.state = 'succeeded' AND d.operation <> 'uninstall' ORDER BY d.created_at DESC, d.id DESC LIMIT 1), '')
		FROM applications a WHERE a.status <> 'stopped' OR COALESCE((SELECT d.delete_data FROM deployments d WHERE d.application_id = a.id AND d.state = 'succeeded' AND d.operation = 'uninstall' ORDER BY d.created_at DESC, d.id DESC LIMIT 1), 0) = 0 ORDER BY a.site_id, a.node_id, a.id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var id, node, site, appKey, version string
		if err := rows.Scan(&id, &node, &site, &appKey, &version); err != nil {
			rows.Close()
			return result, err
		}
		appMetadata[id] = [2]string{appKey, version}
		component := RecoveryComponent{Key: "application:" + id, Kind: "application", ID: id, SiteID: site, NodeID: node, Classification: "application_owned", State: "action_required", Reason: "external_application_backup_and_restore_evidence_required", Release: Version, Dependencies: []string{"center", "agent:" + node}}
		if policy, supported := catalog.OfficialRecoveryPolicy(appKey, version); supported {
			component.BackupPolicy = &policy
			if policy.Consistency == "reconstructible" {
				component.Classification = "reconstructible"
			}
		} else {
			component.State, component.Reason = "unsupported", "application_backup_policy_not_declared"
		}
		result.Components = append(result.Components, component)
	}
	readErr = rows.Err()
	if err := rows.Close(); err != nil {
		return result, err
	}
	if readErr != nil {
		return result, readErr
	}
	states := map[string]string{}
	for index := range result.Components {
		component := &result.Components[index]
		if component.State == "" && component.Classification == "authoritative" {
			if err := s.evaluateRecoveryEvidence(ctx, component, result.CheckedAt); err != nil {
				return result, err
			}
		}
		if component.Classification == "application_owned" && component.BackupPolicy != nil {
			metadata := appMetadata[component.ID]
			if err := s.evaluateExternalRecoveryEvidence(ctx, component, *component.BackupPolicy, metadata[0], metadata[1], result.CheckedAt); err != nil {
				return result, err
			}
		}
		if component.Classification == "reconstructible" {
			component.State, component.Reason = "ready", "rebuild_from_desired_state_and_verify_before_publication"
		}
		for _, dependency := range component.Dependencies {
			if states[dependency] != "ready" && component.State == "ready" {
				component.State, component.Reason = "action_required", "required_dependency_not_protected"
			}
		}
		states[component.Key] = component.State
		if component.State != "ready" {
			result.State = "action_required"
		}
	}
	return result, nil
}

func (s *Store) evaluateRecoveryEvidence(ctx context.Context, component *RecoveryComponent, now time.Time) error {
	component.State, component.Reason = "action_required", "verified_backup_required"
	var encoded []byte
	var verifiedAt string
	err := s.db.QueryRowContext(ctx, `SELECT artifact_json, verified_at FROM recovery_evidence WHERE component_key = ?`, component.Key).Scan(&encoded, &verifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var artifact recovery.Artifact
	verified, err := time.Parse(time.RFC3339Nano, verifiedAt)
	if err != nil || json.Unmarshal(encoded, &artifact) != nil {
		component.Reason = "invalid_backup_evidence"
		return nil
	}
	component.LastVerifiedAt, component.ArtifactDigest = &verified, artifact.Digest
	manifest := artifact.Manifest
	if manifest.Release != component.Release || manifest.ComponentID != component.ID || component.IdentityHash != "" && manifest.IdentityHash != component.IdentityHash || component.Kind != "headscale" && manifest.SchemaVersion != component.SchemaVersion {
		component.Reason = "backup_identity_or_version_changed"
		return nil
	}
	if verified.After(now.Add(time.Minute)) || manifest.CreatedAt.After(now.Add(time.Minute)) || now.Sub(verified) > recoveryMaxAge || now.Sub(manifest.CreatedAt) > recoveryMaxAge {
		component.Reason = "backup_evidence_expired"
		return nil
	}
	component.State, component.Reason = "ready", "artifact_authenticated_and_database_verified"
	return nil
}

// RegisterRecoveryArtifact is a host-local administrative operation, not an
// HTTP file-reading endpoint. Each artifact is decrypted and checked before
// storing only non-secret evidence. expectedIdentity is obtained independently
// on the source host, particularly for bundled Headscale's first import.
func (s *Store) RegisterRecoveryArtifact(ctx context.Context, kind, input, password, expectedIdentity string) error {
	var artifact recovery.Artifact
	var err error
	if kind == "center" {
		artifact, err = inspectCenterRecoveryArtifact(ctx, input, password)
	} else if kind == "agent" || kind == "headscale" {
		artifact, err = recovery.Inspect(ctx, input, password, Version)
	} else {
		return errors.New("center: unsupported recovery component")
	}
	if err != nil {
		return err
	}
	if expectedIdentity == "" || artifact.Manifest.IdentityHash != expectedIdentity || artifact.Manifest.Kind != kind {
		return errors.New("center: independently confirmed backup identity is required")
	}
	inventory, err := s.RecoveryReadiness(ctx)
	if err != nil {
		return err
	}
	var key string
	for _, component := range inventory.Components {
		if component.Kind == kind && component.ID == artifact.Manifest.ComponentID {
			if component.IdentityHash != "" && component.IdentityHash != expectedIdentity {
				return errors.New("center: backup belongs to a different or replaced identity")
			}
			if component.State == "unsupported" {
				return errors.New("center: this component requires external recovery")
			}
			key = component.Key
		}
	}
	if key == "" {
		return errors.New("center: backup component is not in this installation")
	}
	if artifact.Manifest.CreatedAt.Before(s.now().Add(-recoveryMaxAge)) {
		return errors.New("center: backup is too old to establish recovery readiness")
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO recovery_evidence(component_key, artifact_json, verified_at) VALUES(?, ?, ?)
		ON CONFLICT(component_key) DO UPDATE SET artifact_json = excluded.artifact_json, verified_at = excluded.verified_at`, key, encoded, s.now().UTC().Format(time.RFC3339Nano))
	return err
}

func inspectCenterRecoveryArtifact(ctx context.Context, input, password string) (recovery.Artifact, error) {
	if err := ValidateBackupPassword(password); err != nil {
		return recovery.Artifact{}, err
	}
	raw, err := recovery.ReadRegularFile(input, 513<<20)
	if err != nil {
		return recovery.Artifact{}, err
	}
	plain, err := backupcrypto.Decrypt(raw, password)
	if err != nil {
		return recovery.Artifact{}, err
	}
	files, err := readArchive(plain)
	if err != nil {
		return recovery.Artifact{}, err
	}
	metadata, err := verifyMetadata(files)
	if err != nil {
		return recovery.Artifact{}, err
	}
	staging, err := os.MkdirTemp("", "vastora-center-backup-check-*")
	if err != nil {
		return recovery.Artifact{}, err
	}
	defer os.RemoveAll(staging)
	captured := filepath.Join(staging, "captured.vastora")
	if err := recovery.PublishPrivateFile(captured, raw); err != nil {
		return recovery.Artifact{}, err
	}
	// Reuse the existing restore implementation, including key binding and
	// exact release/schema validation. No second Center restore format exists.
	if err := Restore(captured, filepath.Join(staging, "center"), password); err != nil {
		return recovery.Artifact{}, err
	}
	if err := ctx.Err(); err != nil {
		return recovery.Artifact{}, err
	}
	return recovery.Artifact{Digest: recovery.Digest(raw), Manifest: recovery.Manifest{FormatVersion: recovery.FormatVersion, Kind: "center", ComponentID: "center", Release: metadata.CenterVersion, ComponentVersion: metadata.CenterVersion, SchemaVersion: int(metadata.SchemaVersion), IdentityHash: recovery.Digest(files["center.key"]), CreatedAt: metadata.CreatedAt, Encryption: "scrypt-aes-256-gcm-v2", Files: metadata.Files, Dependencies: []string{}}}, nil
}

func ReadRecoveryReadiness(ctx context.Context, dataDir string) (RecoveryReadiness, error) {
	key, err := secret.LoadKey(filepath.Join(dataDir, "center.key"))
	if err != nil {
		return RecoveryReadiness{}, err
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(dataDir, "center.db"), RawQuery: "mode=ro"}).String())
	if err != nil {
		return RecoveryReadiness{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var schema int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil || schema != centerSchemaVersion {
		return RecoveryReadiness{}, errors.New("center: recovery status requires the exact database release; it will not migrate the database")
	}
	bound, err := inspectCenterDatabaseKeyBinding(ctx, db, key)
	if err != nil || !bound {
		return RecoveryReadiness{}, errors.New("center: recovery inventory database key is invalid")
	}
	store := &Store{db: db, key: key, dataDir: dataDir, now: time.Now}
	return store.RecoveryReadiness(ctx)
}

func (s *Server) handleRecoveryReadiness(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	view, err := s.store.RecoveryReadiness(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}
