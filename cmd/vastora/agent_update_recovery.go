package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/petauron/vastora/internal/agent"
	_ "modernc.org/sqlite"
)

const (
	hostUpdateRecoveryDirectoryName        = "pre-migration-recovery"
	hostUpdateRecoveryPartialDirectoryName = "pre-migration-recovery.partial"
	hostUpdateRecoveryManifestName         = "manifest.json"
	hostUpdateRecoveryDatabaseName         = "agent.db"
	hostUpdateRecoveryKeyName              = "agent.key"
)

type hostUpdateRecoveryFile struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type hostUpdateRecoveryManifest struct {
	Version             int                    `json:"version"`
	Phase               string                 `json:"phase"`
	SourceVersion       string                 `json:"sourceVersion"`
	TargetVersion       string                 `json:"targetVersion"`
	TargetSchemaVersion int                    `json:"targetSchemaVersion"`
	DataDir             string                 `json:"dataDir"`
	SchemaVersion       int                    `json:"schemaVersion"`
	Candidate           hostUpdateRecoveryFile `json:"candidate"`
	Database            hostUpdateRecoveryFile `json:"database"`
	Key                 hostUpdateRecoveryFile `json:"key"`
}

// prepareHostUpdateRecovery publishes a complete database/key pair before the
// candidate binary can open and migrate production state. It never restores or
// downgrades that pair automatically; the copy is explicit operator recovery
// material retained while candidate activation is still pending.
func prepareHostUpdateRecovery(ctx context.Context, operation hostUpdateOperation, directory, candidatePath string) error {
	if _, err := os.Lstat(directory); err == nil {
		return verifyHostUpdateRecovery(ctx, operation, directory, candidatePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	partialDirectory := filepath.Join(filepath.Dir(directory), hostUpdateRecoveryPartialDirectoryName)
	if err := removeHostUpdateRecovery(partialDirectory); err != nil {
		return fmt.Errorf("remove interrupted recovery point: %w", err)
	}
	dataDir, err := safeAgentDataDir(operation.DataDir)
	if err != nil {
		return err
	}
	schemaVersion, err := checkpointAgentDatabase(ctx, dataDir)
	if err != nil {
		return err
	}
	if err := os.Mkdir(partialDirectory, 0o700); err != nil {
		return err
	}
	defer removeHostUpdateRecovery(partialDirectory)
	candidate, err := hashHostUpdateRecoveryFile(candidatePath)
	if err != nil {
		return fmt.Errorf("inspect Agent update candidate: %w", err)
	}
	database, err := copyHostUpdateRecoveryFile(filepath.Join(dataDir, "agent.db"), filepath.Join(partialDirectory, hostUpdateRecoveryDatabaseName))
	if err != nil {
		return fmt.Errorf("copy Agent database recovery point: %w", err)
	}
	key, err := copyHostUpdateRecoveryFile(filepath.Join(dataDir, "agent.key"), filepath.Join(partialDirectory, hostUpdateRecoveryKeyName))
	if err != nil {
		return fmt.Errorf("copy Agent key recovery point: %w", err)
	}
	manifest := hostUpdateRecoveryManifest{
		Version: 1, Phase: "pre_migration_recovery_ready",
		SourceVersion: operation.SourceVersion, TargetVersion: operation.TargetVersion,
		TargetSchemaVersion: agent.CurrentSchemaVersion(), DataDir: dataDir, SchemaVersion: schemaVersion,
		Candidate: candidate, Database: database, Key: key,
	}
	if manifest.SchemaVersion > manifest.TargetSchemaVersion {
		return fmt.Errorf("agent schema %d is newer than candidate capability %d", manifest.SchemaVersion, manifest.TargetSchemaVersion)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if err := writeHostUpdateRecoveryFile(filepath.Join(partialDirectory, hostUpdateRecoveryManifestName), append(raw, '\n')); err != nil {
		return err
	}
	if err := syncHostUpdateDirectory(partialDirectory); err != nil {
		return err
	}
	if err := os.Rename(partialDirectory, directory); err != nil {
		return fmt.Errorf("publish Agent recovery point: %w", err)
	}
	if err := syncHostUpdateDirectory(filepath.Dir(directory)); err != nil {
		return err
	}
	return verifyHostUpdateRecovery(ctx, operation, directory, candidatePath)
}

func checkpointAgentDatabase(ctx context.Context, dataDir string) (int, error) {
	info, err := os.Lstat(dataDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return 0, errors.New("agent: Agent state directory is not a protected directory")
	}
	databasePath := filepath.Join(dataDir, "agent.db")
	keyPath := filepath.Join(dataDir, "agent.key")
	for _, path := range []string{databasePath, keyPath} {
		if err := requireHostUpdateRegularFile(path); err != nil {
			return 0, err
		}
	}
	dsn := (&url.URL{Scheme: "file", Path: databasePath, RawQuery: "mode=rw"}).String()
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, fmt.Errorf("agent: open Agent database for update checkpoint: %w", err)
	}
	database.SetMaxOpenConns(1)
	closed := false
	defer func() {
		if !closed {
			_ = database.Close()
		}
	}()
	var busy, logFrames, checkpointedFrames int
	if err := database.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return 0, fmt.Errorf("agent: checkpoint Agent database before update: %w", err)
	}
	if busy != 0 {
		return 0, errors.New("agent: Agent database is still busy after stopping the service")
	}
	var integrity string
	if err := database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		if err == nil {
			err = errors.New(integrity)
		}
		return 0, fmt.Errorf("agent: Agent database integrity check failed: %w", err)
	}
	var schemaVersion int
	if err := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schemaVersion); err != nil || schemaVersion < 1 {
		if err == nil {
			err = errors.New("invalid schema version")
		}
		return 0, fmt.Errorf("agent: inspect Agent schema before update: %w", err)
	}
	if err := database.Close(); err != nil {
		return 0, fmt.Errorf("agent: close Agent database checkpoint: %w", err)
	}
	closed = true
	if wal, err := os.Stat(databasePath + "-wal"); err == nil && wal.Size() != 0 {
		return 0, errors.New("agent: Agent database WAL was not fully checkpointed")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	return schemaVersion, nil
}

func verifyHostUpdateRecovery(ctx context.Context, operation hostUpdateOperation, directory, candidatePath string) error {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("agent: pre-migration recovery point is not a protected directory")
	}
	raw, err := readHostUpdateRecoveryFile(filepath.Join(directory, hostUpdateRecoveryManifestName), 64<<10)
	if err != nil {
		return err
	}
	var manifest hostUpdateRecoveryManifest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF || manifest.Version != 1 || manifest.Phase != "pre_migration_recovery_ready" {
		return errors.New("agent: pre-migration recovery manifest is invalid")
	}
	dataDir, err := safeAgentDataDir(operation.DataDir)
	if err != nil {
		return err
	}
	if manifest.SourceVersion != operation.SourceVersion || manifest.TargetVersion != operation.TargetVersion || manifest.TargetSchemaVersion != agent.CurrentSchemaVersion() || manifest.DataDir != dataDir || manifest.SchemaVersion < 1 || manifest.SchemaVersion > manifest.TargetSchemaVersion {
		return errors.New("agent: pre-migration recovery point belongs to another update")
	}
	candidate, err := hashHostUpdateRecoveryFile(candidatePath)
	if err != nil {
		return err
	}
	if candidate != manifest.Candidate {
		return errors.New("agent: update candidate no longer matches the recovery manifest")
	}
	for path, expected := range map[string]hostUpdateRecoveryFile{
		filepath.Join(directory, hostUpdateRecoveryDatabaseName): manifest.Database,
		filepath.Join(directory, hostUpdateRecoveryKeyName):      manifest.Key,
	} {
		actual, err := hashHostUpdateRecoveryFile(path)
		if err != nil {
			return err
		}
		if actual != expected {
			return errors.New("agent: pre-migration recovery file integrity check failed")
		}
	}
	schemaVersion, err := inspectHostUpdateRecoveryDatabase(ctx, filepath.Join(directory, hostUpdateRecoveryDatabaseName))
	if err != nil {
		return err
	}
	if schemaVersion != manifest.SchemaVersion {
		return errors.New("agent: pre-migration recovery schema does not match its manifest")
	}
	return nil
}

func inspectHostUpdateRecoveryDatabase(ctx context.Context, path string) (int, error) {
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}).String()
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, err
	}
	defer database.Close()
	var integrity string
	if err := database.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		return 0, errors.New("agent: pre-migration recovery database is invalid")
	}
	var schemaVersion int
	if err := database.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schemaVersion); err != nil {
		return 0, err
	}
	return schemaVersion, nil
}

func copyHostUpdateRecoveryFile(source, destination string) (hostUpdateRecoveryFile, error) {
	if err := requireHostUpdateRegularFile(source); err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	input, err := os.Open(source)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(output, hash), input)
	if copyErr == nil {
		copyErr = output.Sync()
	}
	if closeErr := output.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return hostUpdateRecoveryFile{}, copyErr
	}
	return hostUpdateRecoveryFile{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func hashHostUpdateRecoveryFile(path string) (hostUpdateRecoveryFile, error) {
	if err := requireHostUpdateRegularFile(path); err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return hostUpdateRecoveryFile{}, errors.New("agent: pre-migration recovery file is not protected")
	}
	file, err := os.Open(path)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return hostUpdateRecoveryFile{}, err
	}
	return hostUpdateRecoveryFile{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func readHostUpdateRecoveryFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("agent: pre-migration recovery file is not protected")
	}
	if info.Size() > limit {
		return nil, errors.New("agent: pre-migration recovery file is too large")
	}
	return os.ReadFile(path)
}

func requireHostUpdateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("agent: update state contains a non-regular file")
	}
	return nil
}

func writeHostUpdateRecoveryFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	var writeErr error
	if _, err := file.Write(content); err != nil {
		writeErr = err
	} else if err := file.Sync(); err != nil {
		writeErr = err
	}
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	return writeErr
}

func removeHostUpdateRecovery(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("agent: refusing to remove an unprotected update recovery directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	allowed := map[string]bool{
		hostUpdateRecoveryManifestName: true,
		hostUpdateRecoveryDatabaseName: true,
		hostUpdateRecoveryKeyName:      true,
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] || entry.IsDir() {
			return errors.New("agent: unexpected file in update recovery directory")
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
			return err
		}
	}
	if err := os.Remove(directory); err != nil {
		return err
	}
	return syncHostUpdateDirectory(filepath.Dir(directory))
}

func syncHostUpdateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
