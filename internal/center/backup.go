package center

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/petauron/vastora/internal/secret"
	"golang.org/x/crypto/scrypt"
)

const (
	backupMagic   = "VASTORA1"
	backupVersion = byte(1)
	saltSize      = 16
)

type backupMetadata struct {
	CreatedAt time.Time         `json:"createdAt"`
	Files     map[string]string `json:"files"`
}

// Backup writes a password-encrypted archive containing a transactionally
// consistent Center SQLite snapshot and its root key. It never includes Agent
// runtime data, application volumes, logs, or registry credentials in cleartext.
func (s *Store) Backup(ctx context.Context, outputPath, password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("center: backup password is required")
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
	snapshot, err := compactSnapshot(ctx, s.db, s.dataDir)
	if err != nil {
		return err
	}
	defer os.Remove(snapshot)
	snapshotData, err := os.ReadFile(snapshot)
	if err != nil {
		return fmt.Errorf("center: read SQLite backup snapshot: %w", err)
	}
	plain, err := archiveFiles(map[string][]byte{"center.db": snapshotData, "center.key": rootKey})
	if err != nil {
		return err
	}
	encrypted, err := encryptBackup(plain, password)
	if err != nil {
		return err
	}
	return writePrivateFile(outputPath, encrypted)
}

// Restore creates a new Center data directory from a password-encrypted
// backup. It refuses a non-empty destination so invoking it cannot overwrite
// a running control plane.
func Restore(backupPath, destination, password string) error {
	if strings.TrimSpace(password) == "" {
		return errors.New("center: backup password is required")
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
	plain, err := decryptBackup(raw, password)
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
	if err := verifyMetadata(files); err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return fmt.Errorf("center: create restore destination: %w", err)
	}
	if err := os.Chmod(destination, 0o700); err != nil {
		return fmt.Errorf("center: protect restore destination: %w", err)
	}
	for _, name := range []string{"center.db", "center.key"} {
		if err := writePrivateFile(filepath.Join(destination, name), files[name]); err != nil {
			return fmt.Errorf("center: restore %s: %w", name, err)
		}
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

func archiveFiles(files map[string][]byte) ([]byte, error) {
	metadata := backupMetadata{CreatedAt: time.Now().UTC(), Files: make(map[string]string, len(files))}
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

func verifyMetadata(files map[string][]byte) error {
	var metadata backupMetadata
	if err := json.Unmarshal(files["metadata.json"], &metadata); err != nil {
		return errors.New("center: backup metadata is invalid")
	}
	if metadata.CreatedAt.IsZero() || len(metadata.Files) != 2 {
		return errors.New("center: backup metadata is incomplete")
	}
	for _, name := range []string{"center.db", "center.key"} {
		if metadata.Files[name] != fileHash(files[name]) {
			return fmt.Errorf("center: backup integrity check failed for %s", name)
		}
	}
	return nil
}

func encryptBackup(plain []byte, password string) ([]byte, error) {
	salt := make([]byte, saltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("center: create backup salt: %w", err)
	}
	key, err := deriveBackupKey(password, salt)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	seal, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, seal.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("center: create backup nonce: %w", err)
	}
	header := append(append([]byte(backupMagic), backupVersion), salt...)
	sealed := seal.Seal(nil, nonce, plain, header)
	return append(append(header, nonce...), sealed...), nil
}

func decryptBackup(raw []byte, password string) ([]byte, error) {
	minimum := len(backupMagic) + 1 + saltSize + 12 + 16
	if len(raw) < minimum || string(raw[:len(backupMagic)]) != backupMagic || raw[len(backupMagic)] != backupVersion {
		return nil, errors.New("center: backup format is not supported")
	}
	headerEnd := len(backupMagic) + 1 + saltSize
	key, err := deriveBackupKey(password, raw[len(backupMagic)+1:headerEnd])
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	seal, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceEnd := headerEnd + seal.NonceSize()
	if len(raw) < nonceEnd+seal.Overhead() {
		return nil, errors.New("center: backup is truncated")
	}
	plain, err := seal.Open(nil, raw[headerEnd:nonceEnd], raw[nonceEnd:], raw[:headerEnd])
	if err != nil {
		return nil, errors.New("center: backup password or integrity check failed")
	}
	return plain, nil
}

func deriveBackupKey(password string, salt []byte) ([]byte, error) {
	key, err := scrypt.Key([]byte(password), salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("center: derive backup key: %w", err)
	}
	return key, nil
}

func fileHash(content []byte) string {
	sum := sha256.Sum256(content)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func requireEmptyDirectory(path string) error {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
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
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}
