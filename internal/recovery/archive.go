// Package recovery handles protected component artifacts. It does not own or
// transport application volumes and does not initialize production databases.
package recovery

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/petauron/vastora/internal/backupcrypto"
)

const FormatVersion = 1
const maxArtifactBytes = 512 << 20

// Manifest is inside the authenticated encrypted envelope. IdentityHash is a
// fingerprint, never a credential. File paths are archive-local basenames.
type Manifest struct {
	FormatVersion    int               `json:"formatVersion"`
	Kind             string            `json:"kind"`
	ComponentID      string            `json:"componentId"`
	Release          string            `json:"release"`
	ComponentVersion string            `json:"componentVersion"`
	SchemaVersion    int               `json:"schemaVersion"`
	IdentityHash     string            `json:"identityHash"`
	CreatedAt        time.Time         `json:"createdAt"`
	Encryption       string            `json:"encryption"`
	Files            map[string]string `json:"files"`
	Dependencies     []string          `json:"dependencies"`
}

type Artifact struct {
	Manifest Manifest `json:"manifest"`
	Digest   string   `json:"digest"`
}

func Digest(value []byte) string { sum := sha256.Sum256(value); return fmt.Sprintf("sha256:%x", sum) }

func ValidatePassword(password string) error {
	if utf8.RuneCountInString(strings.TrimSpace(password)) < 12 {
		return errors.New("recovery: backup password must be at least 12 characters")
	}
	return nil
}

func Write(output, password string, manifest Manifest, files map[string][]byte) (Artifact, error) {
	if err := ValidatePassword(password); err != nil {
		return Artifact{}, err
	}
	manifest.FormatVersion, manifest.Encryption = FormatVersion, "scrypt-aes-256-gcm-v2"
	manifest.CreatedAt = time.Now().UTC()
	manifest.Files = make(map[string]string, len(files))
	names := make([]string, 0, len(files))
	var total int
	for name, data := range files {
		if !safeName(name) || name == "manifest.json" {
			return Artifact{}, errors.New("recovery: invalid artifact member")
		}
		total += len(data)
		if total > maxArtifactBytes {
			return Artifact{}, errors.New("recovery: component backup exceeds the size limit")
		}
		manifest.Files[name] = Digest(data)
		names = append(names, name)
	}
	if err := validateManifest(manifest); err != nil {
		return Artifact{}, err
	}
	sort.Strings(names)
	metadata, err := json.Marshal(manifest)
	if err != nil {
		return Artifact{}, err
	}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, name := range append(names, "manifest.json") {
		data := files[name]
		if name == "manifest.json" {
			data = metadata
		}
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Unix(0, 0)}); err != nil {
			return Artifact{}, err
		}
		if _, err := writer.Write(data); err != nil {
			return Artifact{}, err
		}
	}
	if err := writer.Close(); err != nil {
		return Artifact{}, err
	}
	raw, err := backupcrypto.Encrypt(buffer.Bytes(), password)
	if err != nil {
		return Artifact{}, err
	}
	if err := PublishPrivateFile(output, raw); err != nil {
		return Artifact{}, err
	}
	return Artifact{Manifest: manifest, Digest: Digest(raw)}, nil
}

func Read(input, password string) (Artifact, map[string][]byte, error) {
	if err := ValidatePassword(password); err != nil {
		return Artifact{}, nil, err
	}
	raw, err := ReadRegularFile(input, maxArtifactBytes+(1<<20))
	if err != nil {
		return Artifact{}, nil, err
	}
	plain, err := backupcrypto.Decrypt(raw, password)
	if err != nil {
		return Artifact{}, nil, err
	}
	reader := tar.NewReader(bytes.NewReader(plain))
	files := map[string][]byte{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || !safeName(header.Name) || header.Size < 0 || header.Size > maxArtifactBytes || len(files) >= 32 {
			return Artifact{}, nil, errors.New("recovery: invalid backup archive")
		}
		if _, exists := files[header.Name]; exists {
			return Artifact{}, nil, errors.New("recovery: duplicate backup member")
		}
		data, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil || int64(len(data)) != header.Size {
			return Artifact{}, nil, errors.New("recovery: truncated backup member")
		}
		files[header.Name] = data
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(files["manifest.json"]))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Artifact{}, nil, errors.New("recovery: invalid manifest")
	}
	delete(files, "manifest.json")
	if err := validateManifest(manifest); err != nil {
		return Artifact{}, nil, err
	}
	if len(files) != len(manifest.Files) {
		return Artifact{}, nil, errors.New("recovery: incomplete artifact")
	}
	for name, expected := range manifest.Files {
		data, exists := files[name]
		if !exists || !safeName(name) || Digest(data) != expected {
			return Artifact{}, nil, errors.New("recovery: artifact integrity check failed")
		}
	}
	return Artifact{Manifest: manifest, Digest: Digest(raw)}, files, nil
}

func validateManifest(value Manifest) error {
	if value.FormatVersion != FormatVersion || (value.Kind != "agent" && value.Kind != "headscale") || value.ComponentID == "" || len(value.ComponentID) > 256 || value.Release == "" || value.ComponentVersion == "" || value.SchemaVersion < 0 || value.CreatedAt.IsZero() || value.CreatedAt.After(time.Now().Add(time.Minute)) || value.Encryption != "scrypt-aes-256-gcm-v2" || len(value.IdentityHash) != len("sha256:")+64 || !strings.HasPrefix(value.IdentityHash, "sha256:") || len(value.Files) == 0 {
		return errors.New("recovery: incomplete or unsupported manifest")
	}
	if decoded, err := hex.DecodeString(strings.TrimPrefix(value.IdentityHash, "sha256:")); err != nil || len(decoded) != sha256.Size {
		return errors.New("recovery: invalid identity fingerprint")
	}
	return nil
}

func safeName(name string) bool {
	return name != "." && name != ".." && name != "" && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\`)
}

func ReadRegularFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("recovery: required artifact file is unavailable")
	}
	if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > limit {
		return nil, errors.New("recovery: artifact file type or size is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("recovery: artifact file cannot be read")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("recovery: artifact file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("recovery: artifact file exceeds the read limit")
	}
	after, err := file.Stat()
	if err != nil || !after.ModTime().Equal(info.ModTime()) || after.Size() != info.Size() || int64(len(data)) != info.Size() {
		return nil, errors.New("recovery: artifact file changed while reading")
	}
	return data, nil
}

// PublishPrivateFile never overwrites a previous backup. Hard-link publication
// is atomic and exclusive; a retry cannot replace a completed artifact.
func PublishPrivateFile(path string, data []byte) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("recovery: output must be a clean absolute path")
	}
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("recovery: output directory must already exist")
	}
	temporary, err := os.CreateTemp(directory, ".vastora-recovery-*")
	if err != nil {
		return errors.New("recovery: cannot stage the artifact")
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary.Name(), path); err != nil {
		return errors.New("recovery: output already exists or cannot be published; choose a new filename")
	}
	return SyncDirectory(directory)
}

func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
