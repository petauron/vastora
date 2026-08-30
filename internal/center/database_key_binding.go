package center

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/petauron/vastora/internal/secret"
)

const centerDatabaseKeyBindingComponent = "center"

type encryptedReferenceQuery struct {
	table   string
	columns []string
	query   string
}

func inspectCenterDatabaseKeyBinding(ctx context.Context, db *sql.DB, key []byte) (bool, error) {
	exists, err := tableHasColumns(ctx, db, "storage_key_binding", "id", "sealed")
	if err != nil || !exists {
		return false, err
	}
	var sealed []byte
	if err := db.QueryRowContext(ctx, `SELECT sealed FROM storage_key_binding WHERE id = 1`).Scan(&sealed); errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("center: database key binding is missing")
	} else if err != nil {
		return false, fmt.Errorf("center: read database key binding: %w", err)
	}
	if err := secret.VerifyDatabaseKeyBinding(key, sealed, centerDatabaseKeyBindingComponent); err != nil {
		return false, fmt.Errorf("center: verify database key binding: %w", err)
	}
	return true, nil
}

func (s *Store) initializeDatabaseKeyBinding(ctx context.Context) error {
	sealed, err := secret.SealDatabaseKeyBinding(s.key, centerDatabaseKeyBindingComponent)
	if err != nil {
		return fmt.Errorf("center: create database key binding: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO storage_key_binding(id, sealed) VALUES(1, ?) ON CONFLICT(id) DO NOTHING`, sealed); err != nil {
		return fmt.Errorf("center: save database key binding: %w", err)
	}
	bound, err := inspectCenterDatabaseKeyBinding(ctx, s.db, s.key)
	if err != nil {
		return err
	}
	if !bound {
		return errors.New("center: database key binding was not initialized")
	}
	return nil
}

func verifyCenterEncryptedState(ctx context.Context, db *sql.DB, key []byte) error {
	references := make(map[string]string)
	queries := []encryptedReferenceQuery{
		{"catalog_sources", []string{"id", "bearer_secret_id"}, `SELECT bearer_secret_id, 'catalog-source:' || id FROM catalog_sources WHERE bearer_secret_id IS NOT NULL`},
		{"registry_credentials", []string{"id", "secret_id"}, `SELECT secret_id, 'registry-credential:' || id FROM registry_credentials`},
		{"agent_enrollment_tokens", []string{"token_hash", "bootstrap_secret_id"}, `SELECT bootstrap_secret_id, 'agent-enrollment:' || lower(hex(token_hash)) FROM agent_enrollment_tokens WHERE bootstrap_secret_id IS NOT NULL`},
		{"agent_enrollment_operations", []string{"token_hash", "operation_id", "response_secret_id"}, `SELECT response_secret_id, 'agent-enrollment-operation:' || lower(hex(token_hash)) || ':' || operation_id FROM agent_enrollment_operations`},
		{"certificate_authorities", []string{"id", "secret_id"}, `SELECT secret_id, 'acme-account:' || id FROM certificate_authorities`},
		{"site_certificates", []string{"site_id", "secret_id"}, `SELECT secret_id, 'site-certificate:' || site_id FROM site_certificates WHERE secret_id IS NOT NULL`},
		{"publications", []string{"id", "certificate_secret_id"}, `SELECT certificate_secret_id, 'publication-certificate:' || id FROM publications WHERE certificate_secret_id IS NOT NULL`},
		{"network_integrations", []string{"kind", "secret_id"}, `SELECT secret_id, 'integration:' || kind FROM network_integrations WHERE secret_id IS NOT NULL`},
		{"system_endpoint_aliases", []string{"kind", "certificate_secret_id"}, `SELECT certificate_secret_id, 'system-certificate:center' FROM system_endpoint_aliases WHERE kind = 'center' AND certificate_secret_id IS NOT NULL`},
		{"application_secrets", []string{"application_id", "secret_id"}, `SELECT secret_id, 'application:' || application_id FROM application_secrets`},
		{"cloudflare_tunnels", []string{"agent_id", "token_secret_id"}, `SELECT token_secret_id, 'cloudflare-tunnel:' || agent_id FROM cloudflare_tunnels`},
		{"cloudflare_tunnel_operations", []string{"agent_id", "tunnel_secret_id"}, `SELECT tunnel_secret_id, 'cloudflare-tunnel-operation:' || agent_id FROM cloudflare_tunnel_operations`},
		{"center_remote_access", []string{"tunnel_token_secret_id"}, `SELECT tunnel_token_secret_id, 'center-remote-access-tunnel' FROM center_remote_access WHERE tunnel_token_secret_id IS NOT NULL`},
		{"deployments", []string{"id", "secret_id"}, `SELECT secret_id, 'deployment:' || id FROM deployments WHERE secret_id IS NOT NULL`},
		{"application_commands", []string{"id", "result_secret_id"}, `SELECT result_secret_id, 'application-command:' || id FROM application_commands WHERE result_secret_id IS NOT NULL`},
		{"settings", []string{"key", "value"}, `SELECT value, CASE key WHEN 'official_catalog_signing_key' THEN 'official-catalog-signing-key' WHEN 'system_center_certificate_secret_id' THEN 'system-certificate:center' END FROM settings WHERE key IN ('official_catalog_signing_key', 'system_center_certificate_secret_id')`},
	}
	for _, specification := range queries {
		exists, err := tableHasColumns(ctx, db, specification.table, specification.columns...)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		rows, err := db.QueryContext(ctx, specification.query)
		if err != nil {
			return fmt.Errorf("center: inspect encrypted references in %s: %w", specification.table, err)
		}
		for rows.Next() {
			var secretID, additionalData string
			if err := rows.Scan(&secretID, &additionalData); err != nil {
				_ = rows.Close()
				return fmt.Errorf("center: inspect encrypted references in %s: %w", specification.table, err)
			}
			if secretID == "" || additionalData == "" {
				_ = rows.Close()
				return fmt.Errorf("center: encrypted reference in %s is invalid", specification.table)
			}
			if current, ok := references[secretID]; ok && current != additionalData {
				_ = rows.Close()
				return fmt.Errorf("center: encrypted record %s has conflicting authentication contexts", secretID)
			}
			references[secretID] = additionalData
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("center: inspect encrypted references in %s: %w", specification.table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("center: close encrypted references in %s: %w", specification.table, err)
		}
	}

	secretsTable, err := tableHasColumns(ctx, db, "secrets", "id", "sealed")
	if err != nil {
		return err
	}
	if !secretsTable {
		return errors.New("center: encrypted record table is missing")
	}
	rows, err := db.QueryContext(ctx, `SELECT id, sealed FROM secrets ORDER BY id`)
	if err != nil {
		return fmt.Errorf("center: inspect encrypted records: %w", err)
	}
	for rows.Next() {
		var id string
		var sealed []byte
		if err := rows.Scan(&id, &sealed); err != nil {
			_ = rows.Close()
			return fmt.Errorf("center: inspect encrypted record: %w", err)
		}
		additionalData, ok := references[id]
		if !ok {
			_ = rows.Close()
			return fmt.Errorf("center: encrypted record %s has no verifiable owner", id)
		}
		if _, err := secret.Open(key, sealed, []byte(additionalData)); err != nil {
			_ = rows.Close()
			return fmt.Errorf("center: encrypted record %s does not match the root key: %w", id, err)
		}
		delete(references, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("center: inspect encrypted records: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("center: close encrypted records: %w", err)
	}
	if len(references) != 0 {
		return errors.New("center: encrypted reference points to a missing record")
	}
	if err := verifyCenterDirectCiphertexts(ctx, db, key); err != nil {
		return err
	}
	return nil
}

func verifyCenterDirectCiphertexts(ctx context.Context, db *sql.DB, key []byte) error {
	if exists, err := tableHasColumns(ctx, db, "three_x_ui_backups", "application_id", "revision", "sealed"); err != nil {
		return err
	} else if exists {
		rows, err := db.QueryContext(ctx, `SELECT application_id, revision, sealed FROM three_x_ui_backups WHERE sealed IS NOT NULL`)
		if err != nil {
			return fmt.Errorf("center: inspect encrypted 3x-ui restore points: %w", err)
		}
		for rows.Next() {
			var applicationID string
			var revision int64
			var sealed []byte
			if err := rows.Scan(&applicationID, &revision, &sealed); err != nil {
				_ = rows.Close()
				return err
			}
			if _, err := secret.Open(key, sealed, threeXUIBackupAAD(applicationID, revision)); err != nil {
				_ = rows.Close()
				return fmt.Errorf("center: 3x-ui restore point %s revision %d does not match the root key: %w", applicationID, revision, err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	if exists, err := tableHasColumns(ctx, db, "site_certificate_handoff", "site_id", "sealed"); err != nil {
		return err
	} else if exists {
		rows, err := db.QueryContext(ctx, `SELECT site_id, sealed FROM site_certificate_handoff`)
		if err != nil {
			return fmt.Errorf("center: inspect staged Site certificates: %w", err)
		}
		for rows.Next() {
			var siteID string
			var sealed []byte
			if err := rows.Scan(&siteID, &sealed); err != nil {
				_ = rows.Close()
				return err
			}
			if _, err := secret.Open(key, sealed, []byte("site-certificate:"+siteID)); err != nil {
				_ = rows.Close()
				return fmt.Errorf("center: staged Site certificate %s does not match the root key: %w", siteID, err)
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
	}
	return nil
}

func tableHasColumns(ctx context.Context, db *sql.DB, table string, required ...string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info("`+strings.ReplaceAll(table, `"`, `""`)+`")`)
	if err != nil {
		return false, fmt.Errorf("center: inspect table %s: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("center: inspect table %s: %w", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("center: inspect table %s: %w", table, err)
	}
	if len(columns) == 0 {
		return false, nil
	}
	for _, column := range required {
		if !columns[column] {
			return false, nil
		}
	}
	return true, nil
}
