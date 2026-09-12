package center

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/petauron/vastora/internal/backupcrypto"
	"github.com/petauron/vastora/internal/secret"
)

const (
	backupPasswordMinimumLength = 12
)

type backupMetadata struct {
	CenterVersion string            `json:"centerVersion"`
	SchemaVersion int64             `json:"schemaVersion"`
	CreatedAt     time.Time         `json:"createdAt"`
	Files         map[string]string `json:"files"`
}

// Backup writes a password-encrypted archive containing a transactionally
// consistent Center SQLite snapshot and its root key. It never includes Agent
// runtime data, application volumes, logs, or registry credentials in cleartext.
func (s *Store) Backup(ctx context.Context, outputPath, password string) error {
	if err := ValidateBackupPassword(password); err != nil {
		return err
	}
	if strings.TrimSpace(outputPath) == "" {
		return errors.New("center: backup output path is required")
	}
	rootKeyPath := filepath.Join(s.dataDir, "center.key")
	rootKey, err := secret.LoadKey(rootKeyPath)
	if err != nil {
		return fmt.Errorf("center: read root key for backup: %w", err)
	}
	if subtle.ConstantTimeCompare(rootKey, s.key) != 1 {
		return errors.New("center: root key changed while Center was running; backup refused")
	}
	bound, err := inspectCenterDatabaseKeyBinding(ctx, s.db, rootKey)
	if err != nil {
		return fmt.Errorf("center: verify database key binding before backup: %w", err)
	}
	if !bound {
		return errors.New("center: database is not bound to its root key; backup refused")
	}
	if err := verifyCenterEncryptedState(ctx, s.db, rootKey); err != nil {
		return fmt.Errorf("center: verify encrypted state before backup: %w", err)
	}
	schemaVersion, err := sqliteSchemaVersion(ctx, s.db)
	if err != nil {
		return err
	}
	if schemaVersion != centerSchemaVersion {
		return fmt.Errorf("center: backup requires SQLite schema %d, found %d", centerSchemaVersion, schemaVersion)
	}
	if strings.TrimSpace(Version) == "" {
		return errors.New("center: backup requires a Center release version")
	}
	snapshot, err := compactSnapshot(ctx, s.db, s.dataDir)
	if err != nil {
		return err
	}
	defer os.Remove(snapshot)
	snapshotData, err := os.ReadFile(snapshot)
	if err != nil {
		return fmt.Errorf("center: read SQLite backup snapshot: %w", err)
	}
	plain, err := archiveFiles(map[string][]byte{"center.db": snapshotData, "center.key": rootKey}, backupMetadata{
		CenterVersion: Version,
		SchemaVersion: schemaVersion,
	})
	if err != nil {
		return err
	}
	encrypted, err := backupcrypto.Encrypt(plain, password)
	if err != nil {
		return err
	}
	return writePrivateFile(outputPath, encrypted)
}

// Restore creates a new Center data directory from a password-encrypted
// backup. It refuses a non-empty destination so invoking it cannot overwrite
// a running control plane.
func Restore(backupPath, destination, password string) error {
	if err := ValidateBackupPassword(password); err != nil {
		return err
	}
	if strings.TrimSpace(destination) == "" {
		return errors.New("center: restore destination is required")
	}
	if err := requireEmptyDirectory(destination); err != nil {
		return err
	}
	raw, err := os.ReadFile(backupPath)
	if err != nil {
		return fmt.Errorf("center: read backup: %w", err)
	}
	plain, err := backupcrypto.Decrypt(raw, password)
	if err != nil {
		return err
	}
	files, err := readArchive(plain)
	if err != nil {
		return err
	}
	for _, name := range []string{"center.db", "center.key", "metadata.json"} {
		if _, ok := files[name]; !ok {
			return fmt.Errorf("center: backup is missing %s", name)
		}
	}
	if len(files) != 3 {
		return errors.New("center: backup contains unexpected files")
	}
	metadata, err := verifyMetadata(files)
	if err != nil {
		return err
	}
	if metadata.CenterVersion != Version {
		return fmt.Errorf("center: backup requires Center %s, running %s", metadata.CenterVersion, Version)
	}
	if metadata.SchemaVersion != centerSchemaVersion {
		return fmt.Errorf("center: backup schema %d is incompatible with schema %d", metadata.SchemaVersion, centerSchemaVersion)
	}
	destination = filepath.Clean(destination)
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("center: create restore parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(destination)+".restore-*")
	if err != nil {
		return fmt.Errorf("center: create restore staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	for _, name := range []string{"center.db", "center.key"} {
		if err := writePrivateFile(filepath.Join(staging, name), files[name]); err != nil {
			return fmt.Errorf("center: restore %s: %w", name, err)
		}
	}
	if err := validateRestoreStaging(staging, metadata); err != nil {
		return err
	}
	if err := expireRestoredOfficialCatalog(staging); err != nil {
		return err
	}
	if err := syncDirectory(staging); err != nil {
		return fmt.Errorf("center: sync restore staging directory: %w", err)
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("center: remove empty restore destination: %w", err)
	}
	if err := os.Rename(staging, destination); err != nil {
		return fmt.Errorf("center: publish restored directory: %w", err)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("center: sync restored directory publication: %w", err)
	}
	return nil
}

// A restored snapshot may predate a newer accepted revision. Preserve its
// replay floor and root chain, but do not authorize installs until a verified
// online refresh. This changes only local effective freshness, not signed data.
func expireRestoredOfficialCatalog(directory string) error {
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(directory, "center.db"), RawQuery: "mode=rw"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("center: open restored catalog state: %w", err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE official_catalog_trust SET expires_at = '1970-01-01T00:00:00Z'`); err != nil {
		return fmt.Errorf("center: require restored catalog refresh: %w", err)
	}
	return db.Close()
}

// ValidateBackupPassword applies the single password policy shared by the web,
// CLI, backup, and restore entry points.
func ValidateBackupPassword(password string) error {
	if utf8.RuneCountInString(strings.TrimSpace(password)) < backupPasswordMinimumLength {
		return fmt.Errorf("center: backup password must be at least %d characters", backupPasswordMinimumLength)
	}
	return nil
}

func validateRestoreStaging(directory string, metadata backupMetadata) error {
	rootKey, err := secret.LoadKey(filepath.Join(directory, "center.key"))
	if err != nil {
		return fmt.Errorf("center: validate restored root key: %w", err)
	}
	databaseDSN := (&url.URL{Scheme: "file", Path: filepath.Join(directory, "center.db"), RawQuery: "mode=ro&immutable=1"}).String()
	database, err := sql.Open("sqlite", databaseDSN)
	if err != nil {
		return fmt.Errorf("center: open restored SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `PRAGMA query_only = ON`); err != nil {
		return fmt.Errorf("center: protect restored SQLite validation: %w", err)
	}
	var integrity string
	if err := database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("center: check restored SQLite integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("center: restored SQLite integrity check failed: %s", integrity)
	}
	schemaVersion, err := sqliteSchemaVersion(ctx, database)
	if err != nil {
		return err
	}
	if schemaVersion != metadata.SchemaVersion || schemaVersion != centerSchemaVersion {
		return fmt.Errorf("center: restored SQLite schema %d does not match backup schema %d and current schema %d", schemaVersion, metadata.SchemaVersion, centerSchemaVersion)
	}
	bound, err := inspectCenterDatabaseKeyBinding(ctx, database, rootKey)
	if err != nil {
		return fmt.Errorf("center: validate restored database key binding: %w", err)
	}
	if !bound {
		return errors.New("center: restored database is not bound to its root key")
	}
	if err := verifyCenterEncryptedState(ctx, database, rootKey); err != nil {
		return fmt.Errorf("center: validate restored encrypted state: %w", err)
	}
	return nil
}

func compactSnapshot(ctx context.Context, database *sql.DB, dataDir string) (string, error) {
	snapshot := filepath.Join(dataDir, fmt.Sprintf(".backup-%d.db", time.Now().UnixNano()))
	if _, err := database.ExecContext(ctx, "VACUUM INTO ?", snapshot); err != nil {
		return "", fmt.Errorf("center: create SQLite backup snapshot: %w", err)
	}
	return snapshot, nil
}

func archiveFiles(files map[string][]byte, metadata backupMetadata) ([]byte, error) {
	metadata.CreatedAt = time.Now().UTC()
	metadata.Files = make(map[string]string, len(files))
	names := make([]string, 0, len(files))
	for name, content := range files {
		metadata.Files[name] = fileHash(content)
		names = append(names, name)
	}
	sort.Strings(names)
	metadataRaw, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("center: encode backup metadata: %w", err)
	}
	files["metadata.json"] = metadataRaw
	names = append(names, "metadata.json")
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range names {
		content := files[name]
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), ModTime: time.Unix(0, 0)}); err != nil {
			return nil, fmt.Errorf("center: write backup header: %w", err)
		}
		if _, err := writer.Write(content); err != nil {
			return nil, fmt.Errorf("center: write backup content: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("center: finalize backup archive: %w", err)
	}
	return buffer.Bytes(), nil
}

func readArchive(raw []byte) (map[string][]byte, error) {
	reader := tar.NewReader(bytes.NewReader(raw))
	files := make(map[string][]byte)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files, nil
		}
		if err != nil {
			return nil, fmt.Errorf("center: read backup archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != header.Name || header.Size < 0 || header.Size > int64(len(raw)) {
			return nil, errors.New("center: backup archive contains an invalid entry")
		}
		if _, exists := files[header.Name]; exists {
			return nil, errors.New("center: backup archive contains duplicate entries")
		}
		content, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(content)) != header.Size {
			return nil, errors.New("center: backup archive entry is truncated")
		}
		files[header.Name] = content
	}
}

func verifyMetadata(files map[string][]byte) (backupMetadata, error) {
	var metadata backupMetadata
	decoder := json.NewDecoder(bytes.NewReader(files["metadata.json"]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return backupMetadata{}, errors.New("center: backup metadata is invalid")
	}
	if strings.TrimSpace(metadata.CenterVersion) == "" || metadata.SchemaVersion <= 0 || metadata.CreatedAt.IsZero() || len(metadata.Files) != 2 {
		return backupMetadata{}, errors.New("center: backup metadata is incomplete")
	}
	for _, name := range []string{"center.db", "center.key"} {
		if metadata.Files[name] != fileHash(files[name]) {
			return backupMetadata{}, fmt.Errorf("center: backup integrity check failed for %s", name)
		}
	}
	return metadata, nil
}

func fileHash(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func requireEmptyDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("center: inspect restore destination: %w", err)
	}
	if !info.Mode().IsDir() {
		return errors.New("center: restore destination must be an empty directory path")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("center: inspect restore destination: %w", err)
	}
	if len(entries) != 0 {
		return errors.New("center: restore destination must be empty")
	}
	return nil
}

func writePrivateFile(path string, content []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".vastora-write-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
