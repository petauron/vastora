package catalog

import (
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/theupdateframework/go-tuf/v2/metadata"
	"github.com/theupdateframework/go-tuf/v2/metadata/trustedmetadata"
)

// ValidateOfficialRootDirectory validates an independently reviewed public trust
// chain before packaging or publishing. It does not obtain trust from a download
// server: the caller must already trust the reviewed 1.root.json. Historical
// roots may be expired so a valid, authorized replacement remains possible.
func ValidateOfficialRootDirectory(directory string, now time.Time) error {
	if now.IsZero() {
		return errors.New("catalog: root validation requires a valid current time")
	}
	if directory == "" {
		return errors.New("catalog: reviewed public root directory is required")
	}
	// A trailing separator must not turn lstat on a symlink into a directory
	// lookup of its target.
	directory = filepath.Clean(directory)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("catalog: root directory must be a real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	roots := make(map[int64]string)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("catalog: root directory must not contain symlinks or non-regular files")
		}
		if entry.Name() == "README.md" {
			continue
		}
		version, err := strconv.ParseInt(strings.TrimSuffix(entry.Name(), ".root.json"), 10, 64)
		if err != nil || version < 1 || entry.Name() != fmt.Sprintf("%d.root.json", version) {
			return errors.New("catalog: root directory contains an unexpected file; only numbered public roots and README.md are allowed")
		}
		roots[version] = entry.Name()
	}
	if len(roots) == 0 {
		return errors.New("catalog: no reviewed public roots were provided")
	}
	var trusted *trustedmetadata.TrustedMetadata
	for version := int64(1); version <= int64(len(roots)); version++ {
		name, ok := roots[version]
		if !ok {
			return fmt.Errorf("catalog: public root chain is missing %d.root.json", version)
		}
		raw, err := readOfficialRootFile(filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("catalog: invalid %s: %w", name, err)
		}
		if _, err := validateOfficialRoot(raw, version); err != nil {
			return fmt.Errorf("catalog: invalid %s: %w", name, err)
		}
		if trusted == nil {
			trusted, err = trustedmetadata.New(raw)
		} else {
			_, err = trusted.UpdateRoot(raw)
		}
		if err != nil {
			return fmt.Errorf("catalog: unauthorized %s: %w", name, err)
		}
	}
	if !now.Before(trusted.Root.Signed.Expires) {
		return errors.New("catalog: final reviewed public root has expired")
	}
	return nil
}

func readOfficialRootFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > MaxEnvelopeBytes {
		return nil, errors.New("root must be a regular public file within size limits")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
		return nil, errors.New("root file changed while opening")
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxEnvelopeBytes+1))
	if err != nil || len(raw) == 0 || int64(len(raw)) > MaxEnvelopeBytes {
		return nil, errors.New("root file exceeds size limits or could not be read")
	}
	return raw, nil
}

func validateOfficialRoot(raw []byte, version int64) (*metadata.Metadata[metadata.RootType], error) {
	var root metadata.Metadata[metadata.RootType]
	if err := decodeStrictJSON(raw, &root); err != nil {
		return nil, errors.New("root is not valid strict JSON")
	}
	if root.Signed.Type != metadata.ROOT || root.Signed.Version != version || !root.Signed.ConsistentSnapshot {
		return nil, errors.New("root type, version, or consistent-snapshot policy is invalid")
	}
	// TUF permits extension fields. This public packaging format deliberately
	// does not: in particular, keyval.private must never reach a public artifact.
	if len(root.UnrecognizedFields) != 0 || len(root.Signed.UnrecognizedFields) != 0 {
		return nil, errors.New("root contains unrecognized fields or private key material")
	}
	for _, signature := range root.Signatures {
		if len(signature.UnrecognizedFields) != 0 {
			return nil, errors.New("root signature contains unrecognized fields")
		}
	}
	fingerprints := make(map[string][32]byte, len(root.Signed.Keys))
	for id, key := range root.Signed.Keys {
		if key == nil || len(key.UnrecognizedFields) != 0 || len(key.Value.UnrecognizedFields) != 0 {
			return nil, errors.New("root contains unrecognized fields or private key material")
		}
		public, err := key.ToPublicKey()
		if err != nil {
			return nil, errors.New("root contains an invalid public key")
		}
		encoded, err := x509.MarshalPKIXPublicKey(public)
		if err != nil {
			return nil, errors.New("root contains an invalid public key")
		}
		keyID, err := key.ID()
		if err != nil || keyID != id {
			return nil, errors.New("root public key identifier does not match its content")
		}
		fingerprints[id] = sha256.Sum256(encoded)
	}
	if len(root.Signed.Roles) != len(metadata.TOP_LEVEL_ROLE_NAMES) {
		return nil, errors.New("root must authorize exactly the four top-level roles")
	}
	rootKeys := make(map[[32]byte]bool)
	for _, roleName := range []string{metadata.ROOT, metadata.TARGETS, metadata.SNAPSHOT, metadata.TIMESTAMP} {
		role := root.Signed.Roles[roleName]
		if role == nil || role.Threshold < 1 || len(role.UnrecognizedFields) != 0 {
			return nil, fmt.Errorf("root has invalid %s authorization", roleName)
		}
		seen := make(map[[32]byte]bool)
		for _, id := range role.KeyIDs {
			fingerprint, ok := fingerprints[id]
			if !ok || seen[fingerprint] {
				return nil, fmt.Errorf("root has missing or duplicate %s keys", roleName)
			}
			seen[fingerprint] = true
			if roleName == metadata.ROOT {
				rootKeys[fingerprint] = true
			} else if rootKeys[fingerprint] {
				return nil, errors.New("publication keys must be separate from root keys")
			}
		}
		if role.Threshold > len(seen) {
			return nil, fmt.Errorf("root has an unsatisfiable %s threshold", roleName)
		}
	}
	return &root, nil
}
